package storage

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"io"
	"sync"
	"testing"
)

/* ------------------------------------------------------------------ */
/*  Fake SQL driver for storage query tests                            */
/* ------------------------------------------------------------------ */

type storageQueryResponse struct {
	columns []string
	rows    [][]driver.Value
	err     error
}

type storageQueryStub struct {
	mu        sync.Mutex
	responses []storageQueryResponse
}

func (s *storageQueryStub) nextResponse() storageQueryResponse {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.responses) == 0 {
		return storageQueryResponse{err: sql.ErrNoRows}
	}
	resp := s.responses[0]
	s.responses = s.responses[1:]
	return resp
}

type storageDriver struct{ stub *storageQueryStub }
type storageConn struct{ stub *storageQueryStub }
type storageRows struct {
	columns []string
	rows    [][]driver.Value
	index   int
}

func (d storageDriver) Open(string) (driver.Conn, error) {
	return storageConn{stub: d.stub}, nil
}
func (c storageConn) Prepare(string) (driver.Stmt, error) { return nil, fmt.Errorf("not implemented") }
func (c storageConn) Close() error                        { return nil }
func (c storageConn) Begin() (driver.Tx, error)            { return nil, fmt.Errorf("not implemented") }

func (c storageConn) QueryContext(_ context.Context, _ string, _ []driver.NamedValue) (driver.Rows, error) {
	resp := c.stub.nextResponse()
	if resp.err != nil {
		return nil, resp.err
	}
	return &storageRows{columns: resp.columns, rows: resp.rows}, nil
}

func (c storageConn) CheckNamedValue(*driver.NamedValue) error { return nil }

func (r *storageRows) Columns() []string { return r.columns }
func (r *storageRows) Close() error      { return nil }
func (r *storageRows) Next(dest []driver.Value) error {
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

func openStorageDB(t *testing.T, stub *storageQueryStub) *sql.DB {
	t.Helper()
	name := "storagedb_" + t.Name()
	sql.Register(name, storageDriver{stub: stub})
	db, err := sql.Open(name, "")
	if err != nil {
		t.Fatalf("open storage db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

/* ------------------------------------------------------------------ */
/*  GetAPIKeyByHash tests                                              */
/* ------------------------------------------------------------------ */

func TestGetAPIKeyByHashFound(t *testing.T) {
	original := DB
	t.Cleanup(func() { DB = original })

	stub := &storageQueryStub{
		responses: []storageQueryResponse{
			{
				columns: []string{"key", "is_active"},
				rows:    [][]driver.Value{{"hashed-key-abc", true}},
			},
		},
	}
	DB = openStorageDB(t, stub)

	record, err := GetAPIKeyByHash(context.Background(), "hashed-key-abc")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if record == nil {
		t.Fatal("expected record")
	}
	if record.Key != "hashed-key-abc" {
		t.Fatalf("unexpected key: %q", record.Key)
	}
	if !record.IsActive {
		t.Fatal("expected active")
	}
}

func TestGetAPIKeyByHashNotFound(t *testing.T) {
	original := DB
	t.Cleanup(func() { DB = original })

	stub := &storageQueryStub{
		responses: []storageQueryResponse{
			{err: sql.ErrNoRows},
		},
	}
	DB = openStorageDB(t, stub)

	record, err := GetAPIKeyByHash(context.Background(), "nonexistent")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if record != nil {
		t.Fatalf("expected nil record, got %+v", record)
	}
}

func TestGetAPIKeyByHashDBError(t *testing.T) {
	original := DB
	t.Cleanup(func() { DB = original })

	stub := &storageQueryStub{
		responses: []storageQueryResponse{
			{err: fmt.Errorf("connection refused")},
		},
	}
	DB = openStorageDB(t, stub)

	record, err := GetAPIKeyByHash(context.Background(), "some-hash")
	if err == nil {
		t.Fatal("expected error")
	}
	if record != nil {
		t.Fatal("expected nil record on error")
	}
}
