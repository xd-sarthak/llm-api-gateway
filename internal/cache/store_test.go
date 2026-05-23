package cache

import (
	"bytes"
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"

	"github.com/xd-sarthak/llm-api-gateway/internal/storage"
)

type queryCall struct {
	query string
	args  []driver.NamedValue
}

type queryResponse struct {
	columns []string
	rows    [][]driver.Value
	err     error
}

type queryStub struct {
	mu        sync.Mutex
	responses []queryResponse
	calls     []queryCall
}

func (s *queryStub) nextResponse() queryResponse {
	s.mu.Lock()
	defer s.mu.Unlock()

	if len(s.responses) == 0 {
		return queryResponse{err: sql.ErrNoRows}
	}

	response := s.responses[0]
	s.responses = s.responses[1:]
	return response
}

func (s *queryStub) addCall(query string, args []driver.NamedValue) {
	s.mu.Lock()
	defer s.mu.Unlock()

	clonedArgs := make([]driver.NamedValue, len(args))
	copy(clonedArgs, args)
	s.calls = append(s.calls, queryCall{query: query, args: clonedArgs})
}

func (s *queryStub) recordedCalls() []queryCall {
	s.mu.Lock()
	defer s.mu.Unlock()

	calls := make([]queryCall, len(s.calls))
	copy(calls, s.calls)
	return calls
}

type queryDriver struct {
	stub *queryStub
}

type queryConn struct {
	stub *queryStub
}

type queryRows struct {
	columns []string
	rows    [][]driver.Value
	index   int
}

func (d queryDriver) Open(name string) (driver.Conn, error) {
	return queryConn{stub: d.stub}, nil
}

func (c queryConn) Prepare(query string) (driver.Stmt, error) {
	return nil, errors.New("prepare not implemented")
}

func (c queryConn) Close() error {
	return nil
}

func (c queryConn) Begin() (driver.Tx, error) {
	return nil, errors.New("transactions not implemented")
}

func (c queryConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	c.stub.addCall(query, args)
	response := c.stub.nextResponse()
	if response.err != nil {
		return nil, response.err
	}
	return &queryRows{columns: response.columns, rows: response.rows}, nil
}

func (r *queryRows) Columns() []string {
	return r.columns
}

func (r *queryRows) Close() error {
	return nil
}

func (r *queryRows) Next(dest []driver.Value) error {
	if r.index >= len(r.rows) {
		return io.EOF
	}

	row := r.rows[r.index]
	r.index++
	for i := range row {
		dest[i] = row[i]
	}
	return nil
}

func openQueryDB(t *testing.T, stub *queryStub) *sql.DB {
	t.Helper()

	driverName := "cachequerydb_" + t.Name()
	sql.Register(driverName, queryDriver{stub: stub})

	db, err := sql.Open(driverName, "")
	if err != nil {
		t.Fatalf("open query db: %v", err)
	}

	t.Cleanup(func() {
		_ = db.Close()
	})

	return db
}

func TestLookupUsesPromptHashAndModelForExactHits(t *testing.T) {
	originalDB := storage.DB
	t.Cleanup(func() {
		storage.DB = originalDB
	})

	stub := &queryStub{
		responses: []queryResponse{
			{
				columns: []string{"response"},
				rows:    [][]driver.Value{{`{"cached":true}`}},
			},
		},
	}
	storage.DB = openQueryDB(t, stub)

	response, hit, err := Lookup("hello world", "model-a")
	if err != nil {
		t.Fatalf("lookup error: %v", err)
	}
	if !hit {
		t.Fatal("expected exact cache hit")
	}
	if response != `{"cached":true}` {
		t.Fatalf("unexpected response %q", response)
	}

	calls := stub.recordedCalls()
	if len(calls) != 1 {
		t.Fatalf("expected 1 query call, got %d", len(calls))
	}
	if !strings.Contains(calls[0].query, "WHERE prompt_hash = $1") {
		t.Fatalf("expected exact lookup to filter by prompt_hash, got %q", calls[0].query)
	}
	if len(calls[0].args) != 1 {
		t.Fatalf("expected exact lookup to use 1 arg, got %d", len(calls[0].args))
	}
}

func TestLookupSemanticQueryUsesSimilarityThreshold(t *testing.T) {
	originalDB := storage.DB
	originalGetEmbedding := getEmbedding
	t.Cleanup(func() {
		storage.DB = originalDB
		getEmbedding = originalGetEmbedding
	})

	getEmbedding = func(text string) ([]float32, error) {
		return []float32{0.1, 0.2, 0.3}, nil
	}

	stub := &queryStub{
		responses: []queryResponse{
			{err: sql.ErrNoRows},
			{
				columns: []string{"response", "similarity"},
				rows:    [][]driver.Value{{`{"semantic":true}`, 0.91}},
			},
		},
	}
	storage.DB = openQueryDB(t, stub)

	response, hit, err := Lookup("hello world", "model-b")
	if err != nil {
		t.Fatalf("lookup error: %v", err)
	}
	if !hit {
		t.Fatal("expected semantic cache hit")
	}
	if response != `{"semantic":true}` {
		t.Fatalf("unexpected response %q", response)
	}

	calls := stub.recordedCalls()
	if len(calls) != 2 {
		t.Fatalf("expected 2 query calls, got %d", len(calls))
	}
	semanticQuery := calls[1]
	if !strings.Contains(semanticQuery.query, "SELECT response, 1 - (embedding <=> $1) AS similarity") {
		t.Fatalf("expected semantic lookup to fetch nearest similarity, got %q", semanticQuery.query)
	}
	if len(semanticQuery.args) != 1 {
		t.Fatalf("expected semantic lookup to use 1 arg, got %d", len(semanticQuery.args))
	}
}

func TestLookupLogsStructuredSemanticMissEvent(t *testing.T) {
	originalDB := storage.DB
	originalGetEmbedding := getEmbedding
	originalWriter := cacheEventWriter
	t.Cleanup(func() {
		storage.DB = originalDB
		getEmbedding = originalGetEmbedding
		cacheEventWriter = originalWriter
	})

	getEmbedding = func(text string) ([]float32, error) {
		return []float32{0.1, 0.2, 0.3}, nil
	}

	stub := &queryStub{
		responses: []queryResponse{
			{err: sql.ErrNoRows},
			{
				columns: []string{"response", "similarity"},
				rows:    [][]driver.Value{{`{"semantic":true}`, 0.61}},
			},
		},
	}
	storage.DB = openQueryDB(t, stub)

	var buf bytes.Buffer
	cacheEventWriter = &buf

	response, hit, err := Lookup("What is a cache hit?", "model-c")
	if err != nil {
		t.Fatalf("lookup error: %v", err)
	}
	if hit {
		t.Fatal("expected miss when similarity is below threshold")
	}
	if response != "" {
		t.Fatalf("expected empty response, got %q", response)
	}

	var event cacheLookupEvent
	if err := json.NewDecoder(&buf).Decode(&event); err != nil {
		t.Fatalf("decode event: %v", err)
	}
	if event.HitType != "miss" {
		t.Fatalf("expected miss event, got %q", event.HitType)
	}
	if event.SimilarityScore == nil || *event.SimilarityScore != 0.61 {
		t.Fatalf("expected similarity 0.61, got %#v", event.SimilarityScore)
	}
	if event.PromptBucket != "factual" {
		t.Fatalf("expected factual bucket, got %q", event.PromptBucket)
	}
	if event.ModelScope != "global" || event.ScopedPerModel {
		t.Fatalf("unexpected model scope fields: %+v", event)
	}
}

func TestLookupLogsStructuredExactHitEvent(t *testing.T) {
	originalDB := storage.DB
	originalWriter := cacheEventWriter
	t.Cleanup(func() {
		storage.DB = originalDB
		cacheEventWriter = originalWriter
	})

	stub := &queryStub{
		responses: []queryResponse{
			{
				columns: []string{"response"},
				rows:    [][]driver.Value{{`{"cached":true}`}},
			},
		},
	}
	storage.DB = openQueryDB(t, stub)

	var buf bytes.Buffer
	cacheEventWriter = &buf

	response, hit, err := Lookup("package main\nfunc main() {}", "model-d")
	if err != nil {
		t.Fatalf("lookup error: %v", err)
	}
	if !hit {
		t.Fatal("expected exact hit")
	}
	if response != `{"cached":true}` {
		t.Fatalf("unexpected response %q", response)
	}

	var event cacheLookupEvent
	if err := json.NewDecoder(&buf).Decode(&event); err != nil {
		t.Fatalf("decode event: %v", err)
	}
	if event.HitType != "exact" {
		t.Fatalf("expected exact hit event, got %q", event.HitType)
	}
	if event.SimilarityScore == nil || *event.SimilarityScore != 1 {
		t.Fatalf("expected similarity 1.0, got %#v", event.SimilarityScore)
	}
	if event.PromptBucket != "code" {
		t.Fatalf("expected code bucket, got %q", event.PromptBucket)
	}
	if event.EmbeddingMs != 0 || event.SemanticLookupMs != 0 {
		t.Fatalf("expected no semantic timings on exact hit, got %+v", event)
	}
}
