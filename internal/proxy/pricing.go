package proxy

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"sync"
)

type modelPrice struct {
	PromptPerToken     float64
	CompletionPerToken float64
}

var (
	pricingCache   = map[string]modelPrice{}
	pricingCacheMu sync.RWMutex
)

func LoadPricing() error {
	resp, err := http.Get("https://openrouter.ai/api/v1/models")
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("pricing fetch failed with status %d", resp.StatusCode)
	}

	var result struct {
		Data []struct {
			ID      string `json:"id"`
			Pricing struct {
				PromptPerToken     string `json:"prompt"`
				CompletionPerToken string `json:"completion"`
			} `json:"pricing"`
		} `json:"data"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return err
	}

	nextCache := make(map[string]modelPrice, len(result.Data))

	for _, model := range result.Data {
		prompt, err := strconv.ParseFloat(model.Pricing.PromptPerToken, 64)
		if err != nil {
			return fmt.Errorf("invalid prompt pricing for model %s: %w", model.ID, err)
		}
		completion, err := strconv.ParseFloat(model.Pricing.CompletionPerToken, 64)
		if err != nil {
			return fmt.Errorf("invalid completion pricing for model %s: %w", model.ID, err)
		}
		nextCache[model.ID] = modelPrice{
			PromptPerToken:     prompt,
			CompletionPerToken: completion,
		}
	}

	pricingCacheMu.Lock()
	pricingCache = nextCache
	pricingCacheMu.Unlock()

	log.Printf("loaded pricing for %d models", len(nextCache))
	return nil
}

func calculateCost(model string, promptTokens, completionTokens int) float64 {
	pricingCacheMu.RLock()
	pricing, ok := pricingCache[model]
	pricingCacheMu.RUnlock()
	if !ok {
		return 0
	}
	return float64(promptTokens)*pricing.PromptPerToken +
		float64(completionTokens)*pricing.CompletionPerToken
}
