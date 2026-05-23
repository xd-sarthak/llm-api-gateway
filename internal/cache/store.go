package cache

import (
	"crypto/sha256"
	"database/sql"
	"fmt"
	"log"
	"os"
	"strconv"
	"time"

	"github.com/pgvector/pgvector-go"
	"github.com/xd-sarthak/llm-api-gateway/internal/metrics"
	"github.com/xd-sarthak/llm-api-gateway/internal/storage"
)

const defaultSimilarityThreshold = 0.80

var getEmbedding = GetEmbedding

func similarityThreshold() float64 {
	raw := os.Getenv("SEMANTIC_CACHE_SIMILARITY_THRESHOLD")
	if raw == "" {
		return defaultSimilarityThreshold
	}

	value, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		log.Printf("invalid SEMANTIC_CACHE_SIMILARITY_THRESHOLD=%q, using default %.2f", raw, defaultSimilarityThreshold)
		return defaultSimilarityThreshold
	}

	if value <= 0 || value >= 1 {
		log.Printf("out-of-range SEMANTIC_CACHE_SIMILARITY_THRESHOLD=%q, using default %.2f", raw, defaultSimilarityThreshold)
		return defaultSimilarityThreshold
	}

	return value
}

func ExtractPrompt(messages []map[string]string) string {
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i]["role"] == "user" {
			return messages[i]["content"]
		}
	}
	return ""
}

func HashPrompt(prompt string) string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(prompt)))
}

func Lookup(prompt, model string) (string, bool, error) {
	start := time.Now()
	hash := HashPrompt(prompt)
	event := cacheLookupEvent{
		Event:          "cache_lookup",
		Model:          model,
		ModelScope:     "global",
		PromptBucket:   bucketPrompt(prompt),
		Threshold:      similarityThreshold(),
		ScopedPerModel: false,
	}
	defer func() {
		event.TotalLookupMs = time.Since(start).Milliseconds()
		logCacheLookupEvent(event)
		metrics.ObserveLatency("cache.lookup.total", time.Since(start), map[string]string{
			"model":    model,
			"hit_type": event.HitType,
		})
		metrics.RecordCacheLookup(event.HitType == "exact" || event.HitType == "semantic", event.Error != "", map[string]string{
			"model":    model,
			"hit_type": event.HitType,
		})
	}()

	var response string
	exactLookupStart := time.Now()
	err := storage.DB.QueryRow(
		"SELECT response FROM semantic_cache WHERE prompt_hash = $1",
		hash,
	).Scan(&response)
	exactLookupDuration := time.Since(exactLookupStart)
	event.ExactLookupMs = exactLookupDuration.Milliseconds()
	metrics.ObserveLatency("cache.lookup.exact_db", exactLookupDuration, map[string]string{"model": model})

	if err == nil {
		log.Printf("cache exact hit: model=%s hash=%s", model, hash)
		event.HitType = "exact"
		event.SimilarityScore = floatPtr(1)
		return response, true, nil
	}

	if err != sql.ErrNoRows {
		log.Printf("cache exact lookup failed: model=%s hash=%s err=%v", model, hash, err)
		event.HitType = "miss"
		event.Error = err.Error()
		return "", false, err
	}

	log.Printf("cache exact miss: model=%s hash=%s", model, hash)

	embeddingStart := time.Now()
	embedding, err := getEmbedding(prompt)
	embeddingDuration := time.Since(embeddingStart)
	event.EmbeddingMs = embeddingDuration.Milliseconds()
	metrics.ObserveLatency("cache.lookup.embedding", embeddingDuration, map[string]string{"model": model})
	if err != nil {
		log.Printf("cache embedding generation failed during lookup: model=%s hash=%s err=%v", model, hash, err)
		event.HitType = "miss"
		event.Error = err.Error()
		return "", false, err
	}

	vec := pgvector.NewVector(embedding)
	var similarity float64
	semanticLookupStart := time.Now()
	err = storage.DB.QueryRow(
		`
		SELECT response, 1 - (embedding <=> $1) AS similarity
		FROM semantic_cache
		ORDER BY embedding <=> $1
		LIMIT 1
		`,
		vec,
	).Scan(&response, &similarity)
	semanticLookupDuration := time.Since(semanticLookupStart)
	event.SemanticLookupMs = semanticLookupDuration.Milliseconds()
	metrics.ObserveLatency("cache.lookup.semantic_db", semanticLookupDuration, map[string]string{"model": model})

	if err == sql.ErrNoRows {
		log.Printf("cache semantic miss: model=%s hash=%s", model, hash)
		event.HitType = "miss"
		return "", false, nil
	}

	if err != nil {
		log.Printf("cache semantic lookup failed: model=%s hash=%s err=%v", model, hash, err)
		event.HitType = "miss"
		event.Error = err.Error()
		return "", false, err
	}

	event.SimilarityScore = floatPtr(similarity)
	if similarity < event.Threshold {
		log.Printf("cache semantic miss: model=%s hash=%s similarity=%.4f threshold=%.2f", model, hash, similarity, event.Threshold)
		event.HitType = "miss"
		return "", false, nil
	}

	log.Printf("cache semantic hit: model=%s hash=%s similarity=%.4f threshold=%.2f", model, hash, similarity, event.Threshold)
	event.HitType = "semantic"
	return response, true, nil
}

func Store(prompt, response, model string) error {
	hash := HashPrompt(prompt)

	embeddingStart := time.Now()
	embedding, err := getEmbedding(prompt)
	metrics.ObserveLatency("cache.store.embedding", time.Since(embeddingStart), map[string]string{"model": model})
	if err != nil {
		log.Printf("cache embedding generation failed during store: model=%s hash=%s err=%v", model, hash, err)
		metrics.IncError("cache_store_embedding", map[string]string{"model": model})
		return err
	}

	vec := pgvector.NewVector(embedding)
	storeStart := time.Now()
	_, err = storage.DB.Exec(`
		INSERT INTO semantic_cache (prompt_hash, embedding, prompt, response, model)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (prompt_hash) DO NOTHING`,
		hash, vec, prompt, response, model,
	)
	metrics.ObserveLatency("cache.store.db", time.Since(storeStart), map[string]string{"model": model})
	if err != nil {
		log.Printf("cache store query failed: model=%s hash=%s err=%v", model, hash, err)
		metrics.IncError("cache_store_db", map[string]string{"model": model})
		return err
	}

	log.Printf("cache store success: model=%s hash=%s", model, hash)
	metrics.IncCounter("cache.store.success", map[string]string{"model": model})
	return nil
}
