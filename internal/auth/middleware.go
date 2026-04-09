package auth

import (
	"crypto/sha256"
	"fmt"
	"net/http"
	"strings"

	"github.com/xd-sarthak/llm-api-gateway/internal/storage"
)

func RequireAPIKey(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {

		authHeader := r.Header.Get("Authorization")
		if authHeader == "" {
			http.Error(w, "missing authorization header", http.StatusUnauthorized)
			return
		}

		parts := strings.SplitN(authHeader, " ", 2)
		if len(parts) != 2 || parts[0] != "Bearer" {
			http.Error(w, "invalid authorization format", http.StatusUnauthorized)
			return
		}

		key := parts[1]
		hash := fmt.Sprintf("%x", sha256.Sum256([]byte(key)))

		valid, err := storage.IsValidAPIKey(hash)
		if err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		if !valid {
			http.Error(w, "invalid or inactive api key", http.StatusUnauthorized)
			return
		}

		next.ServeHTTP(w, r)
	})
}
