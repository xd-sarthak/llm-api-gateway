package ratelimit

import (
	"net/http"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/xd-sarthak/llm-api-gateway/internal/auth"
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
				http.Error(w, "missing api key context", http.StatusInternalServerError)
				return
			}

			decision, err := AllowRequest(r.Context(), rdb, apiKey.ID, limit, window)
			if err != nil {
				http.Error(w, "rate limit error", http.StatusInternalServerError)
				return
			}

			setHeaders(w, decision)

			if !decision.Allowed {
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
