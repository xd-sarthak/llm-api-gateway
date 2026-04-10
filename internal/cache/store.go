package cache

import (
	"crypto/sha256"
	"database/sql"
	"fmt"
	"log"
	"os"
	"strconv"

	"github.com/pgvector/pgvector-go"
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
	hash := HashPrompt(prompt)

	var response string
	err := storage.DB.QueryRow(
		"SELECT response FROM semantic_cache WHERE prompt_hash = $1",
		hash,
	).Scan(&response)

	if err == nil {
		log.Printf("cache exact hit: model=%s hash=%s", model, hash)
		return response, true, nil
	}

	if err != sql.ErrNoRows {
		log.Printf("cache exact lookup failed: model=%s hash=%s err=%v", model, hash, err)
		return "", false, err
	}

	log.Printf("cache exact miss: model=%s hash=%s", model, hash)

	embedding, err := getEmbedding(prompt)
	if err != nil {
		log.Printf("cache embedding generation failed during lookup: model=%s hash=%s err=%v", model, hash, err)
		return "", false, err
	}

	vec := pgvector.NewVector(embedding)
	threshold := similarityThreshold()
	var similarity float64
	err = storage.DB.QueryRow(
		`
		SELECT response, 1 - (embedding <=> $1) AS similarity
		FROM semantic_cache
		ORDER BY embedding <=> $1
		LIMIT 1
		`,
		vec,
	).Scan(&response, &similarity)

	if err == sql.ErrNoRows {
		log.Printf("cache semantic miss: model=%s hash=%s", model, hash)
		return "", false, nil
	}

	if err != nil {
		log.Printf("cache semantic lookup failed: model=%s hash=%s err=%v", model, hash, err)
		return "", false, err
	}

	if similarity < threshold {
		log.Printf("cache semantic miss: model=%s hash=%s similarity=%.4f threshold=%.2f", model, hash, similarity, threshold)
		return "", false, nil
	}

	log.Printf("cache semantic hit: model=%s hash=%s similarity=%.4f threshold=%.2f", model, hash, similarity, threshold)
	return response, true, nil
}

func Store(prompt, response, model string) error {
	hash := HashPrompt(prompt)

	embedding, err := getEmbedding(prompt)
	if err != nil {
		log.Printf("cache embedding generation failed during store: model=%s hash=%s err=%v", model, hash, err)
		return err
	}

	vec := pgvector.NewVector(embedding)
	_, err = storage.DB.Exec(`
		INSERT INTO semantic_cache (prompt_hash, embedding, prompt, response, model)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (prompt_hash) DO NOTHING`,
		hash, vec, prompt, response, model,
	)
	if err != nil {
		log.Printf("cache store query failed: model=%s hash=%s err=%v", model, hash, err)
		return err
	}

	log.Printf("cache store success: model=%s hash=%s", model, hash)
	return nil
}
