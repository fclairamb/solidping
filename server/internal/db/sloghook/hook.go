// Package sloghook provides a bun query hook that logs queries using slog
// and emits Prometheus histograms for query latency.
package sloghook

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"strings"
	"time"

	"github.com/uptrace/bun"

	"github.com/fclairamb/solidping/server/internal/prommetrics"
)

// QueryHook is a bun query hook that logs SQL queries via slog and
// observes query latency via Prometheus.
type QueryHook struct {
	// Verbose includes the full query in logs (may contain sensitive data).
	// Affects logging only; metrics are always emitted.
	Verbose bool

	// Backend is the label value used on Prometheus metrics
	// ("sqlite" or "postgres").
	Backend string
}

// New creates a new bun query hook. verbose controls whether full
// statements are written to slog; backend ("sqlite" or "postgres") is
// attached as a label to the emitted Prometheus metrics.
func New(verbose bool, backend string) *QueryHook {
	return &QueryHook{Verbose: verbose, Backend: backend}
}

// BeforeQuery is called before executing a query.
func (h *QueryHook) BeforeQuery(ctx context.Context, _ *bun.QueryEvent) context.Context {
	return ctx
}

// AfterQuery is called after executing a query. Emits the query-latency
// histogram, increments busy-retry counter on contention errors, and
// writes a slog line at DEBUG (success) or WARN (failure) level.
func (h *QueryHook) AfterQuery(ctx context.Context, event *bun.QueryEvent) {
	duration := time.Since(event.StartTime)
	operation := event.Operation()

	failed := event.Err != nil && !errors.Is(event.Err, sql.ErrNoRows)

	prommetrics.RecordDBQuery(operation, h.Backend, duration.Seconds(), !failed)

	if failed && isContentionError(event.Err) {
		prommetrics.RecordDBBusyRetry(h.Backend)
	}

	attrs := []slog.Attr{
		slog.Duration("duration", duration),
		slog.String("operation", operation),
	}

	if h.Verbose {
		attrs = append(attrs, slog.String("query", event.Query))
	}

	if failed {
		attrs = append(attrs, slog.String("error", event.Err.Error()))
		slog.LogAttrs(ctx, slog.LevelWarn, "SQL query failed", attrs...)

		return
	}

	slog.LogAttrs(ctx, slog.LevelDebug, "SQL query", attrs...)
}

// isContentionError returns true for SQLite SQLITE_BUSY / SQLITE_LOCKED
// and Postgres serialization-failure / deadlock errors. Heuristic
// string-match because the bun-wrapped error doesn't reliably expose
// the underlying driver-specific error type across both backends.
func isContentionError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	switch {
	case strings.Contains(msg, "database is locked"),
		strings.Contains(msg, "sqlite_busy"),
		strings.Contains(msg, "sqlite_locked"),
		strings.Contains(msg, "serialization_failure"),
		strings.Contains(msg, "deadlock detected"),
		strings.Contains(msg, "could not serialize access"):
		return true
	}
	return false
}
