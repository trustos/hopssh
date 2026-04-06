package db

import (
	"context"
	"database/sql"
	"log"
	"strings"
	"time"
)

// Lock retry intervals, escalating backoff.
// Modeled after PocketBase's approach.
var lockRetryIntervals = []time.Duration{
	50 * time.Millisecond,
	100 * time.Millisecond,
	150 * time.Millisecond,
	200 * time.Millisecond,
	300 * time.Millisecond,
	400 * time.Millisecond,
	500 * time.Millisecond,
	700 * time.Millisecond,
	1000 * time.Millisecond,
	1500 * time.Millisecond,
	2000 * time.Millisecond,
	3000 * time.Millisecond,
}

// isLockError returns true if the error is a SQLite lock contention error.
func isLockError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "database is locked") ||
		strings.Contains(msg, "table is locked")
}

// ResilientDB wraps a *sql.DB (or *sql.Tx) with lock retry on write operations.
// It implements the sqlc DBTX interface so it can be passed to dbsqlc.New().
//
// Query timeouts are handled at the HTTP layer (per-route http.TimeoutHandler)
// and by SQLite's busy_timeout pragma (10s), not here.
type ResilientDB struct {
	db interface {
		ExecContext(context.Context, string, ...interface{}) (sql.Result, error)
		PrepareContext(context.Context, string) (*sql.Stmt, error)
		QueryContext(context.Context, string, ...interface{}) (*sql.Rows, error)
		QueryRowContext(context.Context, string, ...interface{}) *sql.Row
	}
}

// WrapDB wraps a *sql.DB with lock retry.
func WrapDB(db *sql.DB) *ResilientDB {
	return &ResilientDB{db: db}
}

// WrapTx wraps a *sql.Tx with lock retry.
func WrapTx(tx *sql.Tx) *ResilientDB {
	return &ResilientDB{db: tx}
}

func (r *ResilientDB) ExecContext(ctx context.Context, query string, args ...interface{}) (sql.Result, error) {
	result, err := r.db.ExecContext(ctx, query, args...)
	if err == nil || !isLockError(err) {
		return result, err
	}
	return r.retryExec(ctx, query, args)
}

func (r *ResilientDB) PrepareContext(ctx context.Context, query string) (*sql.Stmt, error) {
	return r.db.PrepareContext(ctx, query)
}

func (r *ResilientDB) QueryContext(ctx context.Context, query string, args ...interface{}) (*sql.Rows, error) {
	rows, err := r.db.QueryContext(ctx, query, args...)
	if err == nil || !isLockError(err) {
		return rows, err
	}
	return r.retryQuery(ctx, query, args)
}

func (r *ResilientDB) QueryRowContext(ctx context.Context, query string, args ...interface{}) *sql.Row {
	// QueryRow cannot be retried because *sql.Row defers error until Scan().
	// Lock errors on reads are rare (WAL mode) and handled by busy_timeout pragma.
	return r.db.QueryRowContext(ctx, query, args...)
}

func (r *ResilientDB) retryExec(ctx context.Context, query string, args []interface{}) (sql.Result, error) {
	var lastErr error
	for _, interval := range lockRetryIntervals {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(interval):
		}
		result, err := r.db.ExecContext(ctx, query, args...)
		if err == nil {
			log.Printf("[db] lock retry succeeded for exec")
			return result, nil
		}
		if !isLockError(err) {
			return result, err
		}
		lastErr = err
	}
	return nil, lastErr
}

func (r *ResilientDB) retryQuery(ctx context.Context, query string, args []interface{}) (*sql.Rows, error) {
	var lastErr error
	for _, interval := range lockRetryIntervals {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(interval):
		}
		rows, err := r.db.QueryContext(ctx, query, args...)
		if err == nil {
			log.Printf("[db] lock retry succeeded for query")
			return rows, nil
		}
		if !isLockError(err) {
			return rows, err
		}
		lastErr = err
	}
	return nil, lastErr
}
