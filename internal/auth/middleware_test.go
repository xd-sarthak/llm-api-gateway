package auth

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/xd-sarthak/llm-api-gateway/internal/storage"
)

/* ------------------------------------------------------------------ */
/*  Fake SQL driver for auth tests                                     */
/* ------------------------------------------------------------------ */

type authQueryResponse struct {
	columns []string
	rows    [][]driver.Value
	err     error
}

type authQueryStub struct {
	mu        sync.Mutex
	responses []authQueryResponse
}

func (s *authQueryStub) nextResponse() authQueryResponse {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.responses) == 0 {
		return authQueryResponse{err: sql.ErrNoRows}
	}
	resp := s.responses[0]
	s.responses = s.responses[1:]
	return resp
}

type authDriver struct{ stub *authQueryStub }
type authConn struct{ stub *authQueryStub }
type authRows struct {
	columns []string
	rows    [][]driver.Value
	index   int
}

func (d authDriver) Open(name string) (driver.Conn, error) {
	return authConn{stub: d.stub}, nil
}
func (c authConn) Prepare(string) (driver.Stmt, error)  { return nil, fmt.Errorf("not implemented") }
func (c authConn) Close() error                         { return nil }
func (c authConn) Begin() (driver.Tx, error)             { return nil, fmt.Errorf("not implemented") }

func (c authConn) QueryContext(_ context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	resp := c.stub.nextResponse()
	if resp.err != nil {
		return nil, resp.err
	}
	return &authRows{columns: resp.columns, rows: resp.rows}, nil
}

func (c authConn) CheckNamedValue(*driver.NamedValue) error { return nil }

func (r *authRows) Columns() []string { return r.columns }
func (r *authRows) Close() error      { return nil }
func (r *authRows) Next(dest []driver.Value) error {
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

func openAuthDB(t *testing.T, stub *authQueryStub) *sql.DB {
	t.Helper()
	driverName := "authdb_" + t.Name()
	sql.Register(driverName, authDriver{stub: stub})
	db, err := sql.Open(driverName, "")
	if err != nil {
		t.Fatalf("open auth db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

/* ------------------------------------------------------------------ */
/*  Context helpers                                                    */
/* ------------------------------------------------------------------ */

func TestWithAPIKeyRoundTrip(t *testing.T) {
	ctx := WithAPIKey(context.Background(), APIKey{
		ID:        "test-id",
		HashedKey: "abc123",
		IsActive:  true,
	})

	k, ok := APIKeyFromContext(ctx)
	if !ok {
		t.Fatal("expected api key in context")
	}
	if k.ID != "test-id" || k.HashedKey != "abc123" || !k.IsActive {
		t.Fatalf("unexpected api key: %+v", k)
	}
}

func TestAPIKeyFromContextMissing(t *testing.T) {
	_, ok := APIKeyFromContext(context.Background())
	if ok {
		t.Fatal("expected no api key in empty context")
	}
}

/* ------------------------------------------------------------------ */
/*  RequireAPIKey middleware                                            */
/* ------------------------------------------------------------------ */

func newRequest(auth string) *http.Request {
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	if auth != "" {
		req.Header.Set("Authorization", auth)
	}
	return req
}

func TestRequireAPIKeyMissingHeader(t *testing.T) {
	rec := httptest.NewRecorder()
	RequireAPIKey(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("next should not be called")
	})).ServeHTTP(rec, newRequest(""))

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
}

func TestRequireAPIKeyInvalidFormat(t *testing.T) {
	rec := httptest.NewRecorder()
	RequireAPIKey(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("next should not be called")
	})).ServeHTTP(rec, newRequest("Token xyz"))

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
}

func TestRequireAPIKeyUnknownKey(t *testing.T) {
	original := storage.DB
	t.Cleanup(func() { storage.DB = original })

	stub := &authQueryStub{
		responses: []authQueryResponse{
			{err: sql.ErrNoRows},
		},
	}
	storage.DB = openAuthDB(t, stub)

	rec := httptest.NewRecorder()
	RequireAPIKey(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("next should not be called")
	})).ServeHTTP(rec, newRequest("Bearer unknown-key"))

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
}

func TestRequireAPIKeyInactiveKey(t *testing.T) {
	original := storage.DB
	t.Cleanup(func() { storage.DB = original })

	stub := &authQueryStub{
		responses: []authQueryResponse{
			{
				columns: []string{"key", "is_active"},
				rows:    [][]driver.Value{{"hashed-key", false}},
			},
		},
	}
	storage.DB = openAuthDB(t, stub)

	rec := httptest.NewRecorder()
	RequireAPIKey(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("next should not be called")
	})).ServeHTTP(rec, newRequest("Bearer my-key"))

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
}

func TestRequireAPIKeyDBError(t *testing.T) {
	original := storage.DB
	t.Cleanup(func() { storage.DB = original })

	stub := &authQueryStub{
		responses: []authQueryResponse{
			{err: fmt.Errorf("connection refused")},
		},
	}
	storage.DB = openAuthDB(t, stub)

	rec := httptest.NewRecorder()
	RequireAPIKey(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("next should not be called")
	})).ServeHTTP(rec, newRequest("Bearer some-key"))

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d", rec.Code)
	}
}

func TestRequireAPIKeyValidKey(t *testing.T) {
	original := storage.DB
	t.Cleanup(func() { storage.DB = original })

	stub := &authQueryStub{
		responses: []authQueryResponse{
			{
				columns: []string{"key", "is_active"},
				rows:    [][]driver.Value{{"hashed-key-value", true}},
			},
		},
	}
	storage.DB = openAuthDB(t, stub)

	nextCalled := false
	rec := httptest.NewRecorder()
	RequireAPIKey(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		nextCalled = true
		k, ok := APIKeyFromContext(r.Context())
		if !ok {
			t.Fatal("expected api key in context")
		}
		if !k.IsActive {
			t.Fatal("expected active key")
		}
		w.WriteHeader(http.StatusOK)
	})).ServeHTTP(rec, newRequest("Bearer valid-key"))

	if !nextCalled {
		t.Fatal("expected next handler to be called")
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
}
