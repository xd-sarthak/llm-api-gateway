package ratelimit

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
)

type Decision struct {
	Allowed    bool
	Limit      int
	Remaining  int
	RetryAfter time.Duration
}

var (
	ErrNilRedisClient    = errors.New("ratelimit: redis client is nil")
	ErrInvalidBucketSize = errors.New("ratelimit: bucket capacity must be positive")
	ErrInvalidWindow     = errors.New("ratelimit: refill window must be positive")
)

var tokenBucketScript = redis.NewScript(`
local bucket_key = KEYS[1]
local capacity = tonumber(ARGV[1])
local refill_rate = tonumber(ARGV[2])
local ttl_ms = tonumber(ARGV[3])

local now = redis.call("TIME")
local now_ms = now[1] * 1000 + math.floor(now[2] / 1000)

local bucket = redis.call("HMGET", bucket_key, "tokens", "updated_at_ms")
local tokens = tonumber(bucket[1])
local updated_at_ms = tonumber(bucket[2])

if not tokens or not updated_at_ms then
	tokens = capacity
	updated_at_ms = now_ms
end

local elapsed_ms = math.max(0, now_ms - updated_at_ms)
tokens = math.min(capacity, tokens + (elapsed_ms * refill_rate / 1000.0))

local allowed = 0
local retry_after_ms = 0

if tokens >= 1 then
	allowed = 1
	tokens = tokens - 1
else
	retry_after_ms = math.ceil(((1 - tokens) / refill_rate) * 1000)
end

redis.call("HSET", bucket_key, "tokens", tokens, "updated_at_ms", now_ms)
redis.call("PEXPIRE", bucket_key, ttl_ms)

return {allowed, math.floor(tokens), retry_after_ms}
`)

func AllowRequest(ctx context.Context, rdb *redis.Client, key string, limit int, window time.Duration) (Decision, error) {
	if rdb == nil {
		return Decision{}, ErrNilRedisClient
	}
	if limit <= 0 {
		return Decision{}, ErrInvalidBucketSize
	}
	if window <= 0 {
		return Decision{}, ErrInvalidWindow
	}

	refillRatePerSecond := float64(limit) / window.Seconds()
	ttl := maxDuration(2*window, time.Second)
	redisKey := fmt.Sprintf("ratelimit:%s", key)

	result, err := tokenBucketScript.Run(ctx, rdb, []string{redisKey},
		limit,
		refillRatePerSecond,
		ttl.Milliseconds(),
	).Result()
	if err != nil {
		return Decision{}, err
	}

	values, ok := result.([]interface{})
	if !ok || len(values) != 3 {
		return Decision{}, fmt.Errorf("ratelimit: unexpected redis response %T", result)
	}

	allowed, err := toInt64(values[0])
	if err != nil {
		return Decision{}, err
	}
	remaining, err := toInt64(values[1])
	if err != nil {
		return Decision{}, err
	}
	retryAfterMs, err := toInt64(values[2])
	if err != nil {
		return Decision{}, err
	}

	return Decision{
		Allowed:    allowed == 1,
		Limit:      limit,
		Remaining:  maxInt(0, int(remaining)),
		RetryAfter: time.Duration(retryAfterMs) * time.Millisecond,
	}, nil
}

func RetryAfterSeconds(d time.Duration) int {
	if d <= 0 {
		return 0
	}
	return int(math.Ceil(d.Seconds()))
}

func toInt64(value interface{}) (int64, error) {
	switch v := value.(type) {
	case int64:
		return v, nil
	case string:
		return strconv.ParseInt(v, 10, 64)
	default:
		return 0, fmt.Errorf("ratelimit: unexpected redis scalar type %T", value)
	}
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func maxDuration(a, b time.Duration) time.Duration {
	if a > b {
		return a
	}
	return b
}
