package storage

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"sync"
	"testing"
)

/* ------------------------------------------------------------------ */
/*  Fake SQL driver for usage insert tests                             */
/* ------------------------------------------------------------------ */

type usageExecCall struct {
	query string
	args  []driver.NamedValue
}

type usageStub struct {
	mu    sync.Mutex
	calls []usageExecCall
	err   error
}

func (s *usageStub) recordCall(query string, args []driver.NamedValue) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cloned := make([]driver.NamedValue, len(args))
	copy(cloned, args)
	s.calls = append(s.calls, usageExecCall{query: query, args: cloned})
}

func (s *usageStub) recorded() []usageExecCall {
	s.mu.Lock()
	defer s.mu.Unlock()
	c := make([]usageExecCall, len(s.calls))
	copy(c, s.calls)
	return c
}

type usageDriver struct{ stub *usageStub }
type usageConn struct{ stub *usageStub }

func (d usageDriver) Open(string) (driver.Conn, error) {
	return usageConn{stub: d.stub}, nil
}
func (c usageConn) Prepare(string) (driver.Stmt, error) { return nil, fmt.Errorf("not implemented") }
func (c usageConn) Close() error                        { return nil }
func (c usageConn) Begin() (driver.Tx, error)            { return nil, fmt.Errorf("not implemented") }

func (c usageConn) ExecContext(_ context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	c.stub.recordCall(query, args)
	if c.stub.err != nil {
		return nil, c.stub.err
	}
	return driver.RowsAffected(1), nil
}

func (c usageConn) CheckNamedValue(*driver.NamedValue) error { return nil }

func openUsageDB(t *testing.T, stub *usageStub) *sql.DB {
	t.Helper()
	name := "usagedb_" + t.Name()
	sql.Register(name, usageDriver{stub: stub})
	db, err := sql.Open(name, "")
	if err != nil {
		t.Fatalf("open usage db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

/* ------------------------------------------------------------------ */
/*  InsertUsageLog tests                                               */
/* ------------------------------------------------------------------ */

func TestInsertUsageLogNilDB(t *testing.T) {
	original := DB
	t.Cleanup(func() { DB = original })

	DB = nil
	err := InsertUsageLog(UsageLog{APIKey: "k", Model: "m"})
	if err != ErrNilDB {
		t.Fatalf("expected ErrNilDB, got %v", err)
	}
}

func TestInsertUsageLogSuccess(t *testing.T) {
	original := DB
	t.Cleanup(func() { DB = original })

	stub := &usageStub{}
	DB = openUsageDB(t, stub)

	log := UsageLog{
		APIKey:           "hashed-key",
		Model:            "gpt-4o",
		PromptTokens:     100,
		CompletionTokens: 50,
		TotalTokens:      150,
		LatencyMs:        1200,
		Cost:             0.005,
		StatusCode:       200,
	}

	err := InsertUsageLog(log)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	calls := stub.recorded()
	if len(calls) != 1 {
		t.Fatalf("expected 1 exec call, got %d", len(calls))
	}
	if len(calls[0].args) != 8 {
		t.Fatalf("expected 8 args, got %d", len(calls[0].args))
	}
	if calls[0].args[0].Value != "hashed-key" {
		t.Fatalf("expected api_key arg %q, got %v", "hashed-key", calls[0].args[0].Value)
	}
	if calls[0].args[1].Value != "gpt-4o" {
		t.Fatalf("expected model arg %q, got %v", "gpt-4o", calls[0].args[1].Value)
	}
}

func TestInsertUsageLogDBError(t *testing.T) {
	original := DB
	t.Cleanup(func() { DB = original })

	stub := &usageStub{err: fmt.Errorf("disk full")}
	DB = openUsageDB(t, stub)

	err := InsertUsageLog(UsageLog{APIKey: "k", Model: "m"})
	if err == nil {
		t.Fatal("expected error")
	}
}
