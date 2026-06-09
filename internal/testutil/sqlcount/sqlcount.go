// Package sqlcount provides a SQLite driver shim that counts every
// QueryContext + ExecContext call routed through *sql.DB. Tests use it
// to assert that a repository method issues exactly N database
// statements per call regardless of input size — the workout-list
// batched hydration SOW pins ListByUser at 3 statements, ListAll at 3,
// and a future regression to per-row nested fetches would fail loudly.
//
// Usage:
//
//	db, counter, err := sqlcount.Open(":memory:")
//	defer db.Close()
//	// ... set up schema, seed data ...
//	counter.Reset()
//	// ... call repo method under test ...
//	if got := counter.N(); got != 3 {
//	    t.Fatalf("statement count = %d, want 3", got)
//	}
//
// The shim wraps github.com/mattn/go-sqlite3 by implementing the
// database/sql/driver Conn-level interfaces and forwarding everything
// except QueryContext / ExecContext through embedding. Every call to
// the overridden methods bumps an atomic counter before delegating.
//
// Prepare-based call paths (db.Prepare + stmt.Query) are NOT counted by
// this shim. The repos in this codebase go through QueryContext +
// ExecContext directly, so that gap doesn't matter today; if a future
// caller uses prepared statements the count would under-report and we
// can revisit then.
package sqlcount

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"sync/atomic"

	"github.com/mattn/go-sqlite3"
)

// Counter holds the per-DB statement count. Safe for concurrent use.
type Counter struct {
	n atomic.Int64
}

// N returns the current statement count.
func (c *Counter) N() int64 { return c.n.Load() }

// Reset zeroes the counter — call this immediately before the code
// under test so setup/seed statements don't contaminate the count.
func (c *Counter) Reset() { c.n.Store(0) }

// inc bumps the counter. Internal to the shim.
func (c *Counter) inc() { c.n.Add(1) }

// nextID guarantees a process-unique driver name per Open call so
// tests can run in parallel without colliding on a global registration.
var nextID atomic.Int64

// Open is a drop-in alternative to sql.Open("sqlite3", dsn) that also
// returns a Counter. Each call registers a fresh driver under a unique
// name so callers don't have to coordinate.
func Open(dsn string) (*sql.DB, *Counter, error) {
	counter := &Counter{}
	name := fmt.Sprintf("sqlite3-sqlcount-%d", nextID.Add(1))
	sql.Register(name, &countingDriver{
		base:    &sqlite3.SQLiteDriver{},
		counter: counter,
	})
	db, err := sql.Open(name, dsn)
	if err != nil {
		return nil, nil, err
	}
	return db, counter, nil
}

// countingDriver wraps a base sqlite3.SQLiteDriver. The Open method is
// the only interception point at the driver level; the rest of the
// counting work happens on the returned Conn.
type countingDriver struct {
	base    driver.Driver
	counter *Counter
}

func (d *countingDriver) Open(name string) (driver.Conn, error) {
	conn, err := d.base.Open(name)
	if err != nil {
		return nil, err
	}
	return &countingConn{Conn: conn, counter: d.counter}, nil
}

// countingConn embeds driver.Conn so methods we don't override (Prepare,
// Close, Begin) are forwarded automatically. The interfaces we DO need
// to intercept (QueryerContext, ExecerContext, ConnBeginTx,
// ConnPrepareContext) require explicit methods because go-sqlite3
// implements them as separate optional interfaces — database/sql
// type-asserts the conn for each one, so our wrapper has to too.
type countingConn struct {
	driver.Conn
	counter *Counter
}

// QueryContext intercepts. Count, then delegate to the embedded conn's
// QueryerContext implementation (which sqlite3.SQLiteConn provides).
func (c *countingConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	c.counter.inc()
	qc, ok := c.Conn.(driver.QueryerContext)
	if !ok {
		// Defensive — sqlite3 implements QueryerContext, so we never
		// fall back here. driver.ErrSkip tells database/sql to use the
		// Prepare+Query path instead, which would double-count.
		return nil, driver.ErrSkip
	}
	return qc.QueryContext(ctx, query, args)
}

// ExecContext intercepts. Same shape as QueryContext.
func (c *countingConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	c.counter.inc()
	ec, ok := c.Conn.(driver.ExecerContext)
	if !ok {
		return nil, driver.ErrSkip
	}
	return ec.ExecContext(ctx, query, args)
}

// PrepareContext forwards without counting — prepared statements get
// counted when the caller invokes Query/Exec on the returned Stmt,
// which database/sql routes through QueryContext/ExecContext when the
// driver supports those (it does).
func (c *countingConn) PrepareContext(ctx context.Context, query string) (driver.Stmt, error) {
	cp, ok := c.Conn.(driver.ConnPrepareContext)
	if !ok {
		// Explicit c.Conn rather than c.Prepare so the fallback to the
		// non-context legacy interface is obvious to a reader.
		return c.Conn.Prepare(query) //nolint:staticcheck // QF1008: intentional explicit selector
	}
	return cp.PrepareContext(ctx, query)
}

// BeginTx forwards to the underlying ConnBeginTx so transactional
// repository methods (writes that wrap multiple Exec calls) still
// get the modern context-aware path. The counter increments per
// Exec/Query inside the tx, not on the BeginTx call itself.
func (c *countingConn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	bt, ok := c.Conn.(driver.ConnBeginTx)
	if !ok {
		// Drivers that haven't implemented ConnBeginTx still expose the
		// pre-Go-1.8 Begin; we must call it to support them. staticcheck
		// flags this as deprecated but the fallback is the whole point.
		return c.Conn.Begin() //nolint:staticcheck // SA1019: intentional legacy fallback
	}
	return bt.BeginTx(ctx, opts)
}
