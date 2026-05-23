package proxy

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/xd-sarthak/llm-api-gateway/internal/auth"
	"github.com/xd-sarthak/llm-api-gateway/internal/cache"
	"github.com/xd-sarthak/llm-api-gateway/internal/metrics"
	"github.com/xd-sarthak/llm-api-gateway/internal/storage"
)

const URL = "https://openrouter.ai/api/v1/chat/completions"

const maxRequestBodyBytes int64 = 1 << 20

var httpClient = &http.Client{
	Timeout: 60 * time.Second,
}

var (
	cacheLookup = cache.Lookup
	cacheStore  = cache.Store
)

type openRouterResponse struct {
	Model string `json:"model"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
		TotalTokens      int `json:"total_tokens"`
	} `json:"usage"`
}

type chatRequest struct {
	Model    string              `json:"model"`
	Messages []map[string]string `json:"messages"`
}

func HandleChat(w http.ResponseWriter, r *http.Request) {
	start := time.Now()

	apiKey, _ := auth.APIKeyFromContext(r.Context())

	limitedBody := http.MaxBytesReader(w, r.Body, maxRequestBodyBytes)
	defer limitedBody.Close()

	bodyReadStart := time.Now()
	body, err := io.ReadAll(limitedBody)
	metrics.ObserveLatency("proxy.request_body_read", time.Since(bodyReadStart), nil)
	if err != nil {
		log.Println("body read error:", err)
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			metrics.IncError("request_body_too_large", nil)
			http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
			return
		}
		metrics.IncError("request_body_read", nil)
		http.Error(w, "failed to read request body", http.StatusBadRequest)
		return
	}

	var chatReq chatRequest
	decodeStart := time.Now()
	if err := json.Unmarshal(body, &chatReq); err != nil {
		log.Printf("request decode error for %s: %v", r.RemoteAddr, err)
		metrics.IncError("request_decode", nil)
	}
	metrics.ObserveLatency("proxy.request_decode", time.Since(decodeStart), map[string]string{"model": chatReq.Model})

	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, URL, bytes.NewReader(body))
	if err != nil {
		log.Println("request creation error:", err)
		metrics.IncError("upstream_request_create", map[string]string{"model": chatReq.Model})
		http.Error(w, "failed to create upstream request", http.StatusInternalServerError)
		return
	}

	prompt := cache.ExtractPrompt(chatReq.Messages)

	if prompt != "" {
		cacheLookupStart := time.Now()
		cached, hit, err := cacheLookup(prompt, chatReq.Model)
		metrics.ObserveLatency("proxy.cache_lookup", time.Since(cacheLookupStart), map[string]string{"model": chatReq.Model})
		if err != nil {
			log.Printf("cache lookup failed: model=%s remote=%s err=%v", chatReq.Model, r.RemoteAddr, err)
			metrics.IncError("cache_lookup", map[string]string{"model": chatReq.Model})
		} else if hit {
			log.Printf("cache hit: model=%s remote=%s prompt=%.50s", chatReq.Model, r.RemoteAddr, prompt)
			recordCachedCostSavings(chatReq.Model, cached)
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("X-Cache", "HIT")
			w.WriteHeader(http.StatusOK)
			if _, err := w.Write([]byte(cached)); err != nil {
				log.Println("cache hit response write error:", err)
			}
			return
		}
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+os.Getenv("OPENROUTER_API_KEY"))

	upstreamStart := time.Now()
	resp, err := httpClient.Do(req)
	metrics.ObserveLatency("proxy.upstream_roundtrip", time.Since(upstreamStart), map[string]string{"model": chatReq.Model})
	if err != nil {
		log.Println("forward error:", err)
		metrics.IncError("upstream_request", map[string]string{"model": chatReq.Model})
		http.Error(w, "upstream request failed", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	upstreamReadStart := time.Now()
	respBody, err := io.ReadAll(resp.Body)
	metrics.ObserveLatency("proxy.upstream_body_read", time.Since(upstreamReadStart), map[string]string{
		"model":  chatReq.Model,
		"status": strconv.Itoa(resp.StatusCode),
	})
	if err != nil {
		log.Println("response read error:", err)
		metrics.IncError("upstream_response_read", map[string]string{"model": chatReq.Model})
		http.Error(w, "failed to read upstream response", http.StatusInternalServerError)
		return
	}
	if resp.StatusCode >= http.StatusBadRequest {
		metrics.IncError("upstream_status_"+strconv.Itoa(resp.StatusCode), map[string]string{"model": chatReq.Model})
	}

	latencyMs := int(time.Since(start).Milliseconds())

	var llmResp openRouterResponse
	if err := json.Unmarshal(respBody, &llmResp); err != nil {
		log.Printf("response decode error for %s: %v", r.RemoteAddr, err)
		metrics.IncError("upstream_response_decode", map[string]string{"model": chatReq.Model})
	}
	metrics.AddBusinessValue("tokens_processed_total", float64(llmResp.Usage.TotalTokens), "tokens", map[string]string{"model": chatReq.Model})

	go func() {
		cost := calculateCost(chatReq.Model, llmResp.Usage.PromptTokens, llmResp.Usage.CompletionTokens)
		usageLogStart := time.Now()
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
			metrics.IncError("usage_log_insert", map[string]string{"model": chatReq.Model})
		}
		metrics.ObserveLatency("storage.usage_log_insert", time.Since(usageLogStart), map[string]string{"model": chatReq.Model})

		if prompt != "" && resp.StatusCode >= http.StatusOK && resp.StatusCode < http.StatusMultipleChoices {
			cacheStoreStart := time.Now()
			if err := cacheStore(prompt, string(respBody), chatReq.Model); err != nil {
				log.Printf("cache store failed: model=%s remote=%s err=%v", chatReq.Model, r.RemoteAddr, err)
				metrics.IncError("cache_store", map[string]string{"model": chatReq.Model})
			}
			metrics.ObserveLatency("proxy.cache_store_async", time.Since(cacheStoreStart), map[string]string{"model": chatReq.Model})
		} else if prompt != "" {
			log.Printf("skipping cache store for non-success upstream response: model=%s status=%d remote=%s", chatReq.Model, resp.StatusCode, r.RemoteAddr)
		}
	}()

	if contentType := resp.Header.Get("Content-Type"); contentType != "" {
		w.Header().Set("Content-Type", contentType)
	}
	w.WriteHeader(resp.StatusCode)
	if _, err := w.Write(respBody); err != nil {
		log.Println("response copy error:", err)
		metrics.IncError("response_write", map[string]string{"model": chatReq.Model})
	}
}

func recordCachedCostSavings(model string, cached string) {
	var cachedResp openRouterResponse
	if err := json.Unmarshal([]byte(cached), &cachedResp); err != nil {
		metrics.IncError("cache_hit_response_decode", map[string]string{"model": model})
		return
	}

	cost := calculateCost(model, cachedResp.Usage.PromptTokens, cachedResp.Usage.CompletionTokens)
	metrics.AddBusinessValue("estimated_upstream_cost_saved_usd", cost, "usd", map[string]string{"model": model})
	metrics.AddBusinessValue("tokens_processed_total", float64(cachedResp.Usage.TotalTokens), "tokens", map[string]string{"model": model, "source": "cache"})
}
