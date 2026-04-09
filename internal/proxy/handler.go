package proxy

import (
	"bytes"
	"io"
	"net/http"
	"os"
	"log"
)

const URL = "https://openrouter.ai/api/v1/chat/completions"

func HandleChat(w http.ResponseWriter, r *http.Request) {
	//read incoming request
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "Failed to read request body", http.StatusInternalServerError)
		return
	}

	defer r.Body.Close()

	//forward request to OpenAI API
	req, err := http.NewRequest("POST", URL, bytes.NewBuffer(body))
	if err != nil {
		http.Error(w, "Failed to create request", http.StatusInternalServerError)
		return
	}

	//forward headers
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+os.Getenv("OPENROUTER_API_KEY"))

	//http call
	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
	log.Println("Forward error:", err)
	http.Error(w, err.Error(), http.StatusInternalServerError)
	return
}
	defer resp.Body.Close()

	//read response from OpenAI API
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	io.Copy(w, resp.Body)
}