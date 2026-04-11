package admin

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"fmt"

	"github.com/xd-sarthak/llm-api-gateway/internal/storage"
)


func writeJSON(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(data)
}

//GET /admin/keys

func ListKeys(w http.ResponseWriter, r *http.Request) {
	rows, err := storage.DB.Query("SELECT id,name, is_active,created_at FROM api_keys ORDER BY created_at DESC")

	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	defer rows.Close()

	type key struct {
		ID    string `json:"id"`
		Name  string `json:"name"`
		IsActive bool   `json:"is_active"`
		CreatedAt string `json:"created_at"`
	}

	keys := []key{}
	for rows.Next() {
		var k key
		rows.Scan(&k.ID, &k.Name, &k.IsActive, &k.CreatedAt)
		keys = append(keys, k)
	}

	writeJSON(w, http.StatusOK, keys)
}

//POST /admin/keys
func CreateKey(w http.ResponseWriter, r *http.Request) {
	var body struct {
	Name string `json:"name"`
	}

	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Name == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Invalid request body"})
		return
	}

	raw := make([]byte, 32)
	rand.Read(raw)
	plaintext := "sk_" + hex.EncodeToString(raw)

	//store hash
	hash := fmt.Sprintf("%x", sha256.Sum256([]byte(plaintext)))

	var id string
	err := storage.DB.QueryRow(`
		INSERT INTO api_keys (key, name) VALUES ($1, $2) RETURNING id`, hash, body.Name).Scan(&id)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	writeJSON(w, http.StatusCreated, map[string]string{"id": id, "key": plaintext, "note": "save the key now, it won't be shown again"})
}

// DELETE /admin/keys/{id}
func DeactivateKey(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Missing key ID"})
		return
	}

	result, err := storage.DB.Exec(`UPDATE api_keys SET is_active = false WHERE id = $1`, id)

	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	rows, _ := result.RowsAffected()
	if rows == 0 {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "Key not found"})
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"message": "Key deactivated"})
}

//GET /admin/usage
func GetUsage(w http.ResponseWriter, r *http.Request){
	rows,err := storage.DB.Query(`
	SELECT
		api_key,
		model,
		COUNT(*) AS requests,
		SUM(total_tokens) AS total_tokens,
		SUM(cost_usd) AS total_cost,
		AVG(latency_ms) AS avg_latency_ms
	FROM usage_logs
	GROUP BY api_key, model
	ORDER BY total_tokens DESC
		
		`)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	defer rows.Close()

	type usageRow struct {
		APIKey string `json:"api_key"`
		Model string `json:"model"`
		Requests int `json:"requests"`
		TotalTokens int `json:"total_tokens"`
		TotalCost float64 `json:"total_cost_usd"`
		AvgLatencyMs float64 `json:"avg_latency_ms"`
	}

	usage := []usageRow{}
	for rows.Next() {
		var u usageRow
		rows.Scan(&u.APIKey, &u.Model, &u.Requests, &u.TotalTokens, &u.TotalCost, &u.AvgLatencyMs)
		usage = append(usage, u)
	}

	writeJSON(w, http.StatusOK, usage)
}

// GET /admin/stats
func GetStats(w http.ResponseWriter, r *http.Request) {
	var stats struct {
		TotalRequests  int     `json:"total_requests"`
		TotalTokens    int     `json:"total_tokens"`
		TotalCostUSD   float64 `json:"total_cost_usd"`
		AvgLatencyMs   float64 `json:"avg_latency_ms"`
		CacheEntries   int     `json:"cache_entries"`
	}

	storage.DB.QueryRow(`
		SELECT
			COUNT(*),
			COALESCE(SUM(total_tokens), 0),
			COALESCE(SUM(cost_usd), 0),
			COALESCE(AVG(latency_ms), 0)
		FROM usage_logs
	`).Scan(&stats.TotalRequests, &stats.TotalTokens, &stats.TotalCostUSD, &stats.AvgLatencyMs)

	storage.DB.QueryRow(`SELECT COUNT(*) FROM semantic_cache`).Scan(&stats.CacheEntries)

	writeJSON(w, http.StatusOK, stats)
}