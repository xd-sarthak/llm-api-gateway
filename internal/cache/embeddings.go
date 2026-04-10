package cache

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
)

const geminiEmbedURL = "https://generativelanguage.googleapis.com/v1beta/models/gemini-embedding-001:embedContent"

var embeddingHTTPClient = http.DefaultClient

type embeddingsRequest struct {
	Model                string  `json:"model"`
	Content              content `json:"content"`
	OutputDimensionality int     `json:"output_dimensionality"`
}

type content struct {
	Parts []part `json:"parts"`
}

type part struct {
	Text string `json:"text"`
}

type embeddingResponse struct {
	Embedding struct {
		Values []float32 `json:"values"`
	} `json:"embedding"`
}

func GetEmbedding(text string) ([]float32, error) {
	payload := embeddingsRequest{
		Model: "models/gemini-embedding-001",
		Content: content{
			Parts: []part{{Text: text}},
		},
		OutputDimensionality: 768,
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshal embedding request: %w", err)
	}
	url := fmt.Sprintf("%s?key=%s", geminiEmbedURL, os.Getenv("GEMINI_API_KEY"))

	resp, err := embeddingHTTPClient.Post(url, "application/json", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("request embedding: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		responseBody, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		log.Printf("embedding request failed: status=%d body=%q", resp.StatusCode, string(responseBody))
		return nil, fmt.Errorf("embedding request failed with status %d", resp.StatusCode)
	}

	var result embeddingResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode embedding response: %w", err)
	}
	if len(result.Embedding.Values) == 0 {
		return nil, errors.New("embedding response contained no values")
	}

	return result.Embedding.Values, nil
}
