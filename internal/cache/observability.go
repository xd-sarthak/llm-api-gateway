package cache

import (
	"encoding/json"
	"io"
	"os"
	"strings"
	"sync"
)

type cacheLookupEvent struct {
	Event            string   `json:"event"`
	HitType          string   `json:"hit_type"`
	SimilarityScore  *float64 `json:"similarity_score"`
	PromptBucket     string   `json:"prompt_bucket"`
	Model            string   `json:"model"`
	ModelScope       string   `json:"model_scope"`
	ScopedPerModel   bool     `json:"scoped_per_model"`
	Threshold        float64  `json:"similarity_threshold"`
	ExactLookupMs    int64    `json:"exact_lookup_ms"`
	EmbeddingMs      int64    `json:"embedding_ms"`
	SemanticLookupMs int64    `json:"semantic_lookup_ms"`
	TotalLookupMs    int64    `json:"total_lookup_ms"`
	Error            string   `json:"error,omitempty"`
}

var (
	cacheEventWriterMu sync.Mutex
	cacheEventWriter   io.Writer = os.Stdout
)

func logCacheLookupEvent(event cacheLookupEvent) {
	cacheEventWriterMu.Lock()
	defer cacheEventWriterMu.Unlock()

	_ = json.NewEncoder(cacheEventWriter).Encode(event)
}

func floatPtr(value float64) *float64 {
	return &value
}

func bucketPrompt(prompt string) string {
	text := strings.ToLower(strings.TrimSpace(prompt))
	if text == "" {
		return "conversational"
	}

	codeHints := []string{
		"```", "stack trace", "exception", "panic:", "function", "func ", "class ", "method",
		"variable", "compile", "compiler", "typescript", "javascript", "python", "golang",
		"sql", "regex", "api", "json", "yaml", "docker", "kubernetes",
	}
	for _, hint := range codeHints {
		if strings.Contains(text, hint) {
			return "code"
		}
	}

	factualPrefixes := []string{
		"what", "when", "where", "who", "why", "which", "how many", "define", "explain",
		"summarize", "compare", "list", "tell me about",
	}
	for _, prefix := range factualPrefixes {
		if strings.HasPrefix(text, prefix+" ") || text == prefix {
			return "factual"
		}
	}

	if strings.Contains(text, "?") {
		return "factual"
	}

	return "conversational"
}
