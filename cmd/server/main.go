package main

import (
	"log"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/joho/godotenv"
	"github.com/xd-sarthak/llm-api-gateway/internal/auth"
	"github.com/xd-sarthak/llm-api-gateway/internal/proxy"
	"github.com/xd-sarthak/llm-api-gateway/internal/ratelimit"
	"github.com/xd-sarthak/llm-api-gateway/internal/storage"
	"github.com/xd-sarthak/llm-api-gateway/internal/admin"
)

func main() {
	// load env
	if err := godotenv.Load(); err != nil {
		log.Printf("warning: could not load .env file: %v", err)
	}

	if os.Getenv("OPENROUTER_API_KEY") == "" {
		log.Fatal("OPENROUTER_API_KEY is required")
	}
	if os.Getenv("DATABASE_URL") == "" {
		log.Fatal("DATABASE_URL is required")
	}

	// init db
	storage.Init()
	rdb, err := storage.RedisInit()
	if err != nil {
		log.Fatal("redis not reachable:", err)
	}
	rateLimitPerMinute := getEnvInt("RATE_LIMIT_PER_MINUTE", 60)

	if err := proxy.LoadPricing(); err != nil {
		log.Printf("warning: initial pricing load failed: %v", err)
	}
	go func() {
		ticker := time.NewTicker(6 * time.Hour)
		defer ticker.Stop()

		for range ticker.C {
			if err := proxy.LoadPricing(); err != nil {
				log.Println("pricing refresh failed:", err)
			}
		}
	}()
	r := chi.NewRouter()

	// middleware
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)
	r.Use(corsMiddleware)

	r.With(
		auth.RequireAPIKey,
		ratelimit.Middleware(rdb, rateLimitPerMinute, time.Minute),
	).Post("/v1/chat/completions", proxy.HandleChat)

	// register admin routes
	admin.RegisterRoutes(r)

	// start server
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	log.Printf("server is running on port %s", port)
	log.Fatal(http.ListenAndServe(":"+port, r))
}

func getEnvInt(name string, fallback int) int {
	raw := os.Getenv(name)
	if raw == "" {
		return fallback
	}

	value, err := strconv.Atoi(raw)
	if err != nil {
		log.Printf("warning: invalid %s=%q, using default %d", name, raw, fallback)
		return fallback
	}

	return value
}

func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")

		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}

		next.ServeHTTP(w, r)
	})
}

