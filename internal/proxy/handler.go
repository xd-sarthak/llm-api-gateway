package proxy

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/xd-sarthak/llm-api-gateway/internal/auth"
	"github.com/xd-sarthak/llm-api-gateway/internal/storage"
)

const URL = "https://openrouter.ai/api/v1/chat/completions"

const maxRequestBodyBytes int64 = 1 << 20

var httpClient = &http.Client{
	Timeout: 60 * time.Second,
}

type openRouterResponse struct {
	Model string `json:"model"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
		TotalTokens      int `json:"total_tokens"`
	} `json:"usage"`
}

type chatRequest struct {
	Model string `json:"model"`
}

func HandleChat(w http.ResponseWriter, r *http.Request) {
	start := time.Now()

	apiKey, _ := auth.APIKeyFromContext(r.Context())

	limitedBody := http.MaxBytesReader(w, r.Body, maxRequestBodyBytes)
	defer limitedBody.Close()

	body, err := io.ReadAll(limitedBody)
	if err != nil {
		log.Println("body read error:", err)
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
			return
		}
		http.Error(w, "failed to read request body", http.StatusBadRequest)
		return
	}

	var chatReq chatRequest
	if err := json.Unmarshal(body, &chatReq); err != nil {
		log.Printf("request decode error for %s: %v", r.RemoteAddr, err)
	}

	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, URL, bytes.NewReader(body))
	if err != nil {
		log.Println("request creation error:", err)
		http.Error(w, "failed to create upstream request", http.StatusInternalServerError)
		return
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+os.Getenv("OPENROUTER_API_KEY"))

	resp, err := httpClient.Do(req)
	if err != nil {
		log.Println("forward error:", err)
		http.Error(w, "upstream request failed", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		log.Println("response read error:", err)
		http.Error(w, "failed to read upstream response", http.StatusInternalServerError)
		return
	}

	latencyMs := int(time.Since(start).Milliseconds())

	var llmResp openRouterResponse
	if err := json.Unmarshal(respBody, &llmResp); err != nil {
		log.Printf("response decode error for %s: %v", r.RemoteAddr, err)
	}

	go func() {
		cost := calculateCost(chatReq.Model, llmResp.Usage.PromptTokens, llmResp.Usage.CompletionTokens)
		if err := storage.InsertUsageLog(storage.UsageLog{
			APIKey:           apiKey.ID,
			Model:            chatReq.Model,
			PromptTokens:     llmResp.Usage.PromptTokens,
			CompletionTokens: llmResp.Usage.CompletionTokens,
			TotalTokens:      llmResp.Usage.TotalTokens,
			LatencyMs:        latencyMs,
			StatusCode:       resp.StatusCode,
			Cost:             cost,
		}); err != nil {
			log.Println("failed to log usage:", err)
		}
	}()

	if contentType := resp.Header.Get("Content-Type"); contentType != "" {
		w.Header().Set("Content-Type", contentType)
	}
	w.WriteHeader(resp.StatusCode)
	if _, err := w.Write(respBody); err != nil {
		log.Println("response copy error:", err)
	}
}
