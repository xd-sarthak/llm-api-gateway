package storage

import "errors"

var ErrNilDB = errors.New("storage: db is nil")

type UsageLog struct {
	APIKey           string
	Model            string
	PromptTokens     int
	CompletionTokens int
	TotalTokens      int
	LatencyMs        int
	Cost             float64
	StatusCode       int
}

func InsertUsageLog(log UsageLog) error {
	if DB == nil {
		return ErrNilDB
	}

	_, err := DB.Exec(`
	INSERT INTO usage_logs (api_key, model, prompt_tokens, completion_tokens, total_tokens, latency_ms, cost_usd, status_code)
	VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
		log.APIKey,
		log.Model,
		log.PromptTokens,
		log.CompletionTokens,
		log.TotalTokens,
		log.LatencyMs,
		log.Cost,
		log.StatusCode,
	)
	return err
}
