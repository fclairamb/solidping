// Package sloghook provides a bun query hook that logs queries using slog.
package sloghook

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"time"

	"github.com/uptrace/bun"
)

// QueryHook is a bun query hook that logs SQL queries using slog.
type QueryHook struct {
	// Verbose includes the full query in logs (may contain sensitive data)
	Verbose bool
}

// New creates a new slog query hook.
func New(verbose bool) *QueryHook {
	return &QueryHook{Verbose: verbose}
}

// BeforeQuery is called before executing a query.
func (h *QueryHook) BeforeQuery(ctx context.Context, _ *bun.QueryEvent) context.Context {
	return ctx
}

// AfterQuery is called after executing a query.
func (h *QueryHook) AfterQuery(ctx context.Context, event *bun.QueryEvent) {
	duration := time.Since(event.StartTime)

	attrs := []slog.Attr{
		slog.Duration("duration", duration),
		slog.String("operation", event.Operation()),
	}

	if h.Verbose {
		attrs = append(attrs, slog.String("query", event.Query))
	}

	if event.Err != nil && !errors.Is(event.Err, sql.ErrNoRows) {
		attrs = append(attrs, slog.String("error", event.Err.Error()))
		slog.LogAttrs(ctx, slog.LevelWarn, "SQL query failed", attrs...)

		return
	}

	slog.LogAttrs(ctx, slog.LevelDebug, "SQL query", attrs...)
}
