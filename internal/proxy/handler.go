package proxy

import (
	"io"
	"log"
	"net/http"
	"os"
	"time"
)

const URL = "https://openrouter.ai/api/v1/chat/completions"

const maxRequestBodyBytes int64 = 1 << 20

var httpClient = &http.Client{
	Timeout: 60 * time.Second,
}

func HandleChat(w http.ResponseWriter, r *http.Request) {
	limitedBody := http.MaxBytesReader(w, r.Body, maxRequestBodyBytes)
	defer limitedBody.Close()

	// Forward the request stream upstream while honoring client cancellation.
	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, URL, limitedBody)
	if err != nil {
		log.Printf("proxy failed: could not create upstream request for %s: %v", r.RemoteAddr, err)
		http.Error(w, "Failed to create request", http.StatusInternalServerError)
		return
	}

	// forward headers
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+os.Getenv("OPENROUTER_API_KEY"))

	resp, err := httpClient.Do(req)
	if err != nil {
		log.Println("forward error:", err)
		http.Error(w, "upstream request failed", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	if contentType := resp.Header.Get("Content-Type"); contentType != "" {
		w.Header().Set("Content-Type", contentType)
	}
	w.WriteHeader(resp.StatusCode)
	if _, err := io.Copy(w, resp.Body); err != nil {
		log.Println("response copy error:", err)
	}
}
