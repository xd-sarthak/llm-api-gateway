package proxy

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xd-sarthak/llm-api-gateway/internal/auth"
	"github.com/xd-sarthak/llm-api-gateway/internal/storage"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return fn(req)
}

type execCall struct {
	query string
	args  []driver.NamedValue
}

type recordingDriver struct{}
type recordingConn struct{}

var (
	recordingDriverOnce sync.Once
	execCalls           chan execCall
)

func (recordingDriver) Open(name string) (driver.Conn, error) {
	return recordingConn{}, nil
}

func (recordingConn) Prepare(query string) (driver.Stmt, error) {
	return nil, fmt.Errorf("prepare not implemented")
}

func (recordingConn) Close() error {
	return nil
}

func (recordingConn) Begin() (driver.Tx, error) {
	return nil, fmt.Errorf("transactions not implemented")
}

func (recordingConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	select {
	case execCalls <- execCall{query: query, args: args}:
	default:
	}
	return driver.RowsAffected(1), nil
}

func (recordingConn) CheckNamedValue(value *driver.NamedValue) error {
	return nil
}

func openRecordingDB(t *testing.T) *sql.DB {
	t.Helper()

	recordingDriverOnce.Do(func() {
		sql.Register("recordingdb", recordingDriver{})
	})

	db, err := sql.Open("recordingdb", "")
	if err != nil {
		t.Fatalf("open recording db: %v", err)
	}

	t.Cleanup(func() {
		_ = db.Close()
	})

	return db
}

func assertIntValue(t *testing.T, got any, want int64, name string) {
	t.Helper()

	switch value := got.(type) {
	case int:
		if int64(value) != want {
			t.Fatalf("expected %s %d, got %#v", name, want, got)
		}
	case int64:
		if value != want {
			t.Fatalf("expected %s %d, got %#v", name, want, got)
		}
	default:
		t.Fatalf("expected %s %d, got %#v", name, want, got)
	}
}

func TestHandleChatWritesUpstreamBody(t *testing.T) {
	originalClient := httpClient
	originalDB := storage.DB
	originalCacheLookup := cacheLookup
	originalCacheStore := cacheStore
	t.Cleanup(func() {
		httpClient = originalClient
		storage.DB = originalDB
		cacheLookup = originalCacheLookup
		cacheStore = originalCacheStore
	})

	execCalls = make(chan execCall, 1)
	storage.DB = openRecordingDB(t)
	cacheLookup = func(prompt, model string) (string, bool, error) {
		return "", false, nil
	}
	cacheStore = func(prompt, response, model string) error {
		return nil
	}

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
				Body:       io.NopCloser(strings.NewReader(`{"ok":true,"usage":{"prompt_tokens":11,"completion_tokens":7,"total_tokens":18}}`)),
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
	if body := rec.Body.String(); body != `{"ok":true,"usage":{"prompt_tokens":11,"completion_tokens":7,"total_tokens":18}}` {
		t.Fatalf("expected response body to be forwarded, got %q", body)
	}
	if got := rec.Header().Get("Content-Type"); got != "application/json" {
		t.Fatalf("expected content type to be forwarded, got %q", got)
	}

	select {
	case call := <-execCalls:
		if !strings.Contains(call.query, "INSERT INTO usage_logs") {
			t.Fatalf("expected usage_logs insert query, got %q", call.query)
		}
		if len(call.args) != 8 {
			t.Fatalf("expected 8 insert args, got %d", len(call.args))
		}
		if got := call.args[0].Value; got != "hashed-key" {
			t.Fatalf("expected api key arg %q, got %#v", "hashed-key", got)
		}
		if got := call.args[1].Value; got != "test-model" {
			t.Fatalf("expected model arg %q, got %#v", "test-model", got)
		}
		assertIntValue(t, call.args[2].Value, 11, "prompt tokens")
		assertIntValue(t, call.args[3].Value, 7, "completion tokens")
		assertIntValue(t, call.args[4].Value, 18, "total tokens")
		if got := call.args[6].Value; got == nil {
			t.Fatal("expected cost arg to be populated")
		}
		assertIntValue(t, call.args[7].Value, http.StatusAccepted, "status code")
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for usage log insert")
	}
}

func TestHandleChatRejectsOversizedBody(t *testing.T) {
	originalClient := httpClient
	originalCacheLookup := cacheLookup
	originalCacheStore := cacheStore
	t.Cleanup(func() {
		httpClient = originalClient
		cacheLookup = originalCacheLookup
		cacheStore = originalCacheStore
	})

	cacheLookup = func(prompt, model string) (string, bool, error) {
		return "", false, nil
	}
	cacheStore = func(prompt, response, model string) error {
		return nil
	}

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

func TestHandleChatReturnsCachedResponseWithoutCallingUpstream(t *testing.T) {
	originalClient := httpClient
	originalCacheLookup := cacheLookup
	originalCacheStore := cacheStore
	t.Cleanup(func() {
		httpClient = originalClient
		cacheLookup = originalCacheLookup
		cacheStore = originalCacheStore
	})

	cacheLookup = func(prompt, model string) (string, bool, error) {
		if prompt != "cached prompt" {
			t.Fatalf("expected prompt %q, got %q", "cached prompt", prompt)
		}
		if model != "test-model" {
			t.Fatalf("expected model %q, got %q", "test-model", model)
		}
		return `{"cached":true}`, true, nil
	}
	cacheStore = func(prompt, response, model string) error {
		t.Fatal("cache store should not be called on cache hit")
		return nil
	}

	httpClient = &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			t.Fatal("upstream should not be called on cache hit")
			return nil, nil
		}),
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"test-model","messages":[{"role":"user","content":"cached prompt"}]}`))
	rec := httptest.NewRecorder()

	HandleChat(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, rec.Code)
	}
	if body := rec.Body.String(); body != `{"cached":true}` {
		t.Fatalf("expected cached response body, got %q", body)
	}
	if got := rec.Header().Get("X-Cache"); got != "HIT" {
		t.Fatalf("expected X-Cache HIT, got %q", got)
	}
}

func TestHandleChatSkipsCachingFailedUpstreamResponses(t *testing.T) {
	originalClient := httpClient
	originalDB := storage.DB
	originalCacheLookup := cacheLookup
	originalCacheStore := cacheStore
	t.Cleanup(func() {
		httpClient = originalClient
		storage.DB = originalDB
		cacheLookup = originalCacheLookup
		cacheStore = originalCacheStore
	})

	execCalls = make(chan execCall, 1)
	storage.DB = openRecordingDB(t)

	cacheLookup = func(prompt, model string) (string, bool, error) {
		return "", false, nil
	}

	cacheStoreCalls := make(chan struct{}, 1)
	cacheStore = func(prompt, response, model string) error {
		cacheStoreCalls <- struct{}{}
		return nil
	}

	httpClient = &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusTooManyRequests,
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body:       io.NopCloser(strings.NewReader(`{"error":"rate limited"}`)),
			}, nil
		}),
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"test-model","messages":[{"role":"user","content":"cache me"}]}`))
	req = req.WithContext(auth.WithAPIKey(req.Context(), auth.APIKey{ID: "hashed-key"}))
	rec := httptest.NewRecorder()

	HandleChat(rec, req)

	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("expected status %d, got %d", http.StatusTooManyRequests, rec.Code)
	}

	select {
	case <-cacheStoreCalls:
		t.Fatal("cache store should not be called for failed upstream responses")
	case call := <-execCalls:
		if !strings.Contains(call.query, "INSERT INTO usage_logs") {
			t.Fatalf("expected usage log insert query, got %q", call.query)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for async handler work")
	}
}
