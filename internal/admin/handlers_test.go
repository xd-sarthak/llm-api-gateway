package admin

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/xd-sarthak/llm-api-gateway/internal/storage"
)

/* ------------------------------------------------------------------ */
/*  Fake SQL driver                                                    */
/* ------------------------------------------------------------------ */

type adminQueryResponse struct {
	columns []string
	rows    [][]driver.Value
	err     error
}

type adminStub struct {
	mu        sync.Mutex
	responses []adminQueryResponse
	execResp  *adminExecResponse
}

type adminExecResponse struct {
	rowsAffected int64
	err          error
}

func (s *adminStub) nextResponse() adminQueryResponse {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.responses) == 0 {
		return adminQueryResponse{err: sql.ErrNoRows}
	}
	resp := s.responses[0]
	s.responses = s.responses[1:]
	return resp
}

type adminDriver struct{ stub *adminStub }
type adminConn struct{ stub *adminStub }
type adminRows struct {
	columns []string
	rows    [][]driver.Value
	index   int
}

func (d adminDriver) Open(string) (driver.Conn, error) {
	return adminConn{stub: d.stub}, nil
}
func (c adminConn) Prepare(string) (driver.Stmt, error) { return nil, fmt.Errorf("not implemented") }
func (c adminConn) Close() error                        { return nil }
func (c adminConn) Begin() (driver.Tx, error)            { return nil, fmt.Errorf("not implemented") }

func (c adminConn) QueryContext(_ context.Context, _ string, _ []driver.NamedValue) (driver.Rows, error) {
	resp := c.stub.nextResponse()
	if resp.err != nil {
		return nil, resp.err
	}
	return &adminRows{columns: resp.columns, rows: resp.rows}, nil
}

func (c adminConn) ExecContext(_ context.Context, _ string, _ []driver.NamedValue) (driver.Result, error) {
	c.stub.mu.Lock()
	defer c.stub.mu.Unlock()
	if c.stub.execResp != nil && c.stub.execResp.err != nil {
		return nil, c.stub.execResp.err
	}
	affected := int64(1)
	if c.stub.execResp != nil {
		affected = c.stub.execResp.rowsAffected
	}
	return driver.RowsAffected(affected), nil
}

func (c adminConn) CheckNamedValue(*driver.NamedValue) error { return nil }

func (r *adminRows) Columns() []string { return r.columns }
func (r *adminRows) Close() error      { return nil }
func (r *adminRows) Next(dest []driver.Value) error {
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

func openAdminDB(t *testing.T, stub *adminStub) *sql.DB {
	t.Helper()
	name := "admindb_" + t.Name()
	sql.Register(name, adminDriver{stub: stub})
	db, err := sql.Open(name, "")
	if err != nil {
		t.Fatalf("open admin db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

/* ------------------------------------------------------------------ */
/*  Tests                                                              */
/* ------------------------------------------------------------------ */

func TestGetStatsReturnsAggregatedJSON(t *testing.T) {
	original := storage.DB
	t.Cleanup(func() { storage.DB = original })

	stub := &adminStub{
		responses: []adminQueryResponse{
			// usage_logs aggregate
			{columns: []string{"count", "tokens", "cost", "latency"}, rows: [][]driver.Value{{int64(42), int64(10000), 1.23, 350.5}}},
			// semantic_cache count
			{columns: []string{"count"}, rows: [][]driver.Value{{int64(7)}}},
		},
	}
	storage.DB = openAdminDB(t, stub)

	rec := httptest.NewRecorder()
	GetStats(rec, httptest.NewRequest(http.MethodGet, "/admin/stats", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("expected json content type, got %q", ct)
	}

	var result map[string]interface{}
	if err := json.NewDecoder(rec.Body).Decode(&result); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if int(result["total_requests"].(float64)) != 42 {
		t.Fatalf("unexpected total_requests: %v", result["total_requests"])
	}
}

func TestGetUsageReturnsUsageRows(t *testing.T) {
	original := storage.DB
	t.Cleanup(func() { storage.DB = original })

	stub := &adminStub{
		responses: []adminQueryResponse{
			{
				columns: []string{"api_key", "model", "requests", "prompt_tokens", "completion_tokens", "total_tokens", "total_cost", "avg_latency_ms"},
				rows: [][]driver.Value{
					{"hashed-key", "gpt-4o", int64(10), int64(500), int64(200), int64(700), 0.05, 1200.0},
				},
			},
		},
	}
	storage.DB = openAdminDB(t, stub)

	rec := httptest.NewRecorder()
	GetUsage(rec, httptest.NewRequest(http.MethodGet, "/admin/usage", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var rows []map[string]interface{}
	if err := json.NewDecoder(rec.Body).Decode(&rows); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(rows))
	}
	if rows[0]["model"] != "gpt-4o" {
		t.Fatalf("unexpected model: %v", rows[0]["model"])
	}
}

func TestListKeysReturnsKeysList(t *testing.T) {
	original := storage.DB
	t.Cleanup(func() { storage.DB = original })

	stub := &adminStub{
		responses: []adminQueryResponse{
			{
				columns: []string{"id", "name", "is_active", "created_at"},
				rows: [][]driver.Value{
					{"uuid-1", "production", true, "2026-01-01T00:00:00Z"},
					{"uuid-2", "staging", false, "2026-02-01T00:00:00Z"},
				},
			},
		},
	}
	storage.DB = openAdminDB(t, stub)

	rec := httptest.NewRecorder()
	ListKeys(rec, httptest.NewRequest(http.MethodGet, "/admin/keys", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var keys []map[string]interface{}
	if err := json.NewDecoder(rec.Body).Decode(&keys); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(keys) != 2 {
		t.Fatalf("expected 2 keys, got %d", len(keys))
	}
	if keys[0]["name"] != "production" {
		t.Fatalf("unexpected name: %v", keys[0]["name"])
	}
}

func TestCreateKeyReturns201WithKeyPrefix(t *testing.T) {
	original := storage.DB
	t.Cleanup(func() { storage.DB = original })

	stub := &adminStub{
		responses: []adminQueryResponse{
			{
				columns: []string{"id"},
				rows:    [][]driver.Value{{"new-uuid-1"}},
			},
		},
	}
	storage.DB = openAdminDB(t, stub)

	body := strings.NewReader(`{"name":"my-test-key"}`)
	rec := httptest.NewRecorder()
	CreateKey(rec, httptest.NewRequest(http.MethodPost, "/admin/keys", body))

	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d", rec.Code)
	}

	var result map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&result); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if result["id"] != "new-uuid-1" {
		t.Fatalf("unexpected id: %v", result["id"])
	}
	if !strings.HasPrefix(result["key"], "sk_") {
		t.Fatalf("expected key to start with sk_, got %q", result["key"])
	}
	if result["note"] == "" {
		t.Fatal("expected note to be set")
	}
}

func TestCreateKeyEmptyNameReturns400(t *testing.T) {
	body := strings.NewReader(`{"name":""}`)
	rec := httptest.NewRecorder()
	CreateKey(rec, httptest.NewRequest(http.MethodPost, "/admin/keys", body))

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
}

func TestCreateKeyInvalidJSONReturns400(t *testing.T) {
	body := strings.NewReader(`not json`)
	rec := httptest.NewRecorder()
	CreateKey(rec, httptest.NewRequest(http.MethodPost, "/admin/keys", body))

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
}

func TestDeactivateKeySuccess(t *testing.T) {
	original := storage.DB
	t.Cleanup(func() { storage.DB = original })

	stub := &adminStub{
		execResp: &adminExecResponse{rowsAffected: 1},
	}
	storage.DB = openAdminDB(t, stub)

	req := httptest.NewRequest(http.MethodDelete, "/admin/keys/some-uuid", nil)
	req.SetPathValue("id", "some-uuid")
	rec := httptest.NewRecorder()
	DeactivateKey(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
}

func TestDeactivateKeyNotFound(t *testing.T) {
	original := storage.DB
	t.Cleanup(func() { storage.DB = original })

	stub := &adminStub{
		execResp: &adminExecResponse{rowsAffected: 0},
	}
	storage.DB = openAdminDB(t, stub)

	req := httptest.NewRequest(http.MethodDelete, "/admin/keys/missing-uuid", nil)
	req.SetPathValue("id", "missing-uuid")
	rec := httptest.NewRecorder()
	DeactivateKey(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rec.Code)
	}
}

func TestDeactivateKeyMissingID(t *testing.T) {
	req := httptest.NewRequest(http.MethodDelete, "/admin/keys/", nil)
	// do not set path value — simulates missing id
	rec := httptest.NewRecorder()
	DeactivateKey(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
}
