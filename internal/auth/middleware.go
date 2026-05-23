package auth

import (
	"crypto/sha256"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/xd-sarthak/llm-api-gateway/internal/metrics"
	"github.com/xd-sarthak/llm-api-gateway/internal/storage"
)

func RequireAPIKey(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		defer func() {
			metrics.ObserveLatency("auth.api_key", time.Since(start), nil)
		}()

		authHeader := r.Header.Get("Authorization")
		if authHeader == "" {
			log.Printf("auth failed: missing authorization header from %s", r.RemoteAddr)
			metrics.IncError("auth_missing_authorization", nil)
			http.Error(w, "missing authorization header", http.StatusUnauthorized)
			return
		}

		parts := strings.SplitN(authHeader, " ", 2)
		if len(parts) != 2 || parts[0] != "Bearer" {
			log.Printf("auth failed: invalid authorization format from %s", r.RemoteAddr)
			metrics.IncError("auth_invalid_authorization_format", nil)
			http.Error(w, "invalid authorization format", http.StatusUnauthorized)
			return
		}

		key := parts[1]
		hash := fmt.Sprintf("%x", sha256.Sum256([]byte(key)))

		apiKey, err := storage.GetAPIKeyByHash(r.Context(), hash)
		if err != nil {
			log.Printf("auth failed: database lookup error for %s: %v", r.RemoteAddr, err)
			metrics.IncError("auth_database_lookup", nil)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		if apiKey == nil || !apiKey.IsActive {
			log.Printf("auth failed: invalid or inactive api key from %s", r.RemoteAddr)
			metrics.IncError("auth_invalid_or_inactive_key", nil)
			http.Error(w, "invalid or inactive api key", http.StatusUnauthorized)
			return
		}

		next.ServeHTTP(w, r.WithContext(WithAPIKey(r.Context(), APIKey{
			ID:        apiKey.Key,
			HashedKey: apiKey.Key,
			IsActive:  apiKey.IsActive,
		})))
	})
}
