package ratelimit

import (
	"log"
	"net/http"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/xd-sarthak/llm-api-gateway/internal/auth"
	"github.com/xd-sarthak/llm-api-gateway/internal/metrics"
)

func Middleware(rdb *redis.Client, limit int, window time.Duration) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if limit <= 0 {
				next.ServeHTTP(w, r)
				return
			}

			apiKey, ok := auth.APIKeyFromContext(r.Context())
			if !ok || apiKey.ID == "" {
				log.Printf("rate limit failed: missing api key context for %s", r.RemoteAddr)
				metrics.IncError("ratelimit_missing_api_key", nil)
				http.Error(w, "missing api key context", http.StatusInternalServerError)
				return
			}

			start := time.Now()
			decision, err := AllowRequest(r.Context(), rdb, apiKey.ID, limit, window)
			metrics.ObserveLatency("ratelimit.redis_token_bucket", time.Since(start), map[string]string{
				"allowed": strconv.FormatBool(err == nil && decision.Allowed),
			})
			if err != nil {
				log.Printf("rate limit failed: key=%s remote=%s err=%v", apiKey.ID, r.RemoteAddr, err)
				metrics.IncError("ratelimit_redis", nil)
				http.Error(w, "rate limit error", http.StatusInternalServerError)
				return
			}

			setHeaders(w, decision)

			if !decision.Allowed {
				log.Printf("rate limit exceeded: key=%s remote=%s retry_after=%s", apiKey.ID, r.RemoteAddr, decision.RetryAfter)
				metrics.IncError("ratelimit_exceeded", nil)
				if retryAfter := RetryAfterSeconds(decision.RetryAfter); retryAfter > 0 {
					w.Header().Set("Retry-After", strconv.Itoa(retryAfter))
				}
				http.Error(w, "rate limit exceeded", http.StatusTooManyRequests)
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}

func setHeaders(w http.ResponseWriter, decision Decision) {
	w.Header().Set("X-RateLimit-Limit", strconv.Itoa(decision.Limit))
	w.Header().Set("X-RateLimit-Remaining", strconv.Itoa(decision.Remaining))
}
