package storage

import (
	"context"

	"github.com/redis/go-redis/v9"
)

// RedisInit creates a client and validates connectivity at startup.
func RedisInit() (*redis.Client, error) {
	rdb := redis.NewClient(&redis.Options{
		Addr: "localhost:6379",
	})

	if err := rdb.Ping(context.Background()).Err(); err != nil {
		return nil, err
	}

	return rdb, nil
}
