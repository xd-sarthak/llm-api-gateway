package storage

import (
	"context"
	"github.com/redis/go-redis/v9"
)

var ctx = context.Background()

//initialize redis client
func RedisInit() *redis.Client {
	rdb := redis.NewClient(&redis.Options{
		Addr: "localhost:6379",
	})

	return rdb
}