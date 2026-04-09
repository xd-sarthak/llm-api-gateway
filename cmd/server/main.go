package main

import (
	"log"
	"net/http"
	"os"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/joho/godotenv"
	"github.com/xd-sarthak/llm-api-gateway/internal/auth"
	"github.com/xd-sarthak/llm-api-gateway/internal/proxy"
	"github.com/xd-sarthak/llm-api-gateway/internal/storage"
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

	r := chi.NewRouter()

	// middleware
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)

	r.With(auth.RequireAPIKey).Post("/v1/chat/completions", proxy.HandleChat)

	// start server
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	log.Printf("server is running on port %s", port)
	log.Fatal(http.ListenAndServe(":"+port, r))
}
