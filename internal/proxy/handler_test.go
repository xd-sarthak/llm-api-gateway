package proxy

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/xd-sarthak/llm-api-gateway/internal/auth"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return fn(req)
}

func TestHandleChatWritesUpstreamBody(t *testing.T) {
	originalClient := httpClient
	t.Cleanup(func() {
		httpClient = originalClient
	})

	httpClient = &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			body, err := io.ReadAll(req.Body)
			if err != nil {
				t.Fatalf("read request body: %v", err)
			}
			if got := string(body); got != `{"model":"test-model"}` {
				t.Fatalf("unexpected upstream body %q", got)
			}

			return &http.Response{
				StatusCode: http.StatusAccepted,
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body:       io.NopCloser(strings.NewReader(`{"ok":true}`)),
			}, nil
		}),
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"test-model"}`))
	req = req.WithContext(auth.WithAPIKey(req.Context(), auth.APIKey{ID: "hashed-key"}))
	rec := httptest.NewRecorder()

	HandleChat(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected status %d, got %d", http.StatusAccepted, rec.Code)
	}
	if body := rec.Body.String(); body != `{"ok":true}` {
		t.Fatalf("expected response body to be forwarded, got %q", body)
	}
	if got := rec.Header().Get("Content-Type"); got != "application/json" {
		t.Fatalf("expected content type to be forwarded, got %q", got)
	}
}

func TestHandleChatRejectsOversizedBody(t *testing.T) {
	originalClient := httpClient
	t.Cleanup(func() {
		httpClient = originalClient
	})

	httpClient = &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			t.Fatal("upstream should not be called for oversized body")
			return nil, nil
		}),
	}

	body := strings.Repeat("a", int(maxRequestBodyBytes)+1)
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	rec := httptest.NewRecorder()

	HandleChat(rec, req)

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("expected status %d, got %d", http.StatusRequestEntityTooLarge, rec.Code)
	}
}
