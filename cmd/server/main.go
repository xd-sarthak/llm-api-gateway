package main

import (
	"fmt"
	"log"
	"net/http"
	"os"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/joho/godotenv"
	"github.com/xd-sarthak/llm-api-gateway/internal/proxy"
)

func main(){
	//load env
	if err := godotenv.Load(); err != nil {
		log.Fatal("Error loading .env file")
	}

	r := chi.NewRouter()

	//middleware
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)

	//routes
	r.Post("/v1/chat/completions", proxy.HandleChat)
	
	//start server
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	fmt.Printf("Server is running on port %s\n", port)
	log.Fatal(http.ListenAndServe(":"+port, r))
}