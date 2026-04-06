package db

import (
	"context"
	"database/sql"
	"log"
	"strings"
	"time"
)

// Default query timeout for all database operations.
const DefaultQueryTimeout = 30 * time.Second

// Lock retry intervals (milliseconds), escalating backoff.
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

// ExecWithRetry executes a statement with lock retry.
func ExecWithRetry(ctx context.Context, db *sql.DB, query string, args ...interface{}) (sql.Result, error) {
	ctx, cancel := ensureTimeout(ctx)
	defer cancel()

	result, err := db.ExecContext(ctx, query, args...)
	if err == nil || !isLockError(err) {
		return result, err
	}

	return retryExec(ctx, db, query, args, err)
}

func retryExec(ctx context.Context, db *sql.DB, query string, args []interface{}, lastErr error) (sql.Result, error) {
	for _, interval := range lockRetryIntervals {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(interval):
		}

		result, err := db.ExecContext(ctx, query, args...)
		if err == nil || !isLockError(err) {
			if err == nil {
				log.Printf("[db] lock retry succeeded after backoff")
			}
			return result, err
		}
		lastErr = err
	}
	return nil, lastErr
}

// QueryRowWithRetry executes a single-row query with lock retry.
func QueryRowWithRetry(ctx context.Context, db *sql.DB, query string, args ...interface{}) *sql.Row {
	ctx, cancel := ensureTimeout(ctx)
	defer cancel()
	return db.QueryRowContext(ctx, query, args...)
}

// ensureTimeout adds DefaultQueryTimeout if no deadline is set on the context.
func ensureTimeout(ctx context.Context) (context.Context, context.CancelFunc) {
	if _, ok := ctx.Deadline(); ok {
		return ctx, func() {} // already has a deadline
	}
	return context.WithTimeout(ctx, DefaultQueryTimeout)
}
