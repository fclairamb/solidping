// Package sloghook provides a bun query hook that logs queries using slog
// and emits Prometheus histograms for query latency.
package sloghook

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/uptrace/bun"

	"github.com/fclairamb/solidping/server/internal/prommetrics"
)

// defaultSlowQueryThrottleInterval bounds how often the same normalized
// statement can log a "slow SQL query" WARN. A slow query on a hot path is
// slow on every request, so without this a pathological statement floods the
// log; with it, it produces a steady trickle carrying a suppressed count
// instead.
const defaultSlowQueryThrottleInterval = time.Minute

// QueryHook is a bun query hook that logs SQL queries via slog and
// observes query latency via Prometheus.
type QueryHook struct {
	// Verbose includes the full query in logs (may contain sensitive data).
	// Affects logging only; metrics are always emitted.
	Verbose bool

	// Backend is the label value used on Prometheus metrics
	// ("sqlite" or "postgres").
	Backend string

	// SlowThreshold logs a successful query at WARN when it takes at least
	// this long. Zero (the default zero value) disables slow-query logging
	// entirely — failures still log at WARN and everything else at DEBUG.
	SlowThreshold time.Duration

	// ThrottleInterval bounds how often the same normalized statement may
	// log a slow-query WARN. Zero (the default zero value) falls back to
	// defaultSlowQueryThrottleInterval; only tests override this.
	ThrottleInterval time.Duration

	// Now overrides the clock used for throttling (tests). Nil uses time.Now.
	Now func() time.Time

	mu          sync.Mutex
	slowQueries map[string]*slowQueryState
}

// slowQueryState tracks the last time a normalized statement logged a
// slow-query WARN, and how many occurrences were suppressed since.
type slowQueryState struct {
	lastLogged time.Time
	suppressed int
}

// New creates a new bun query hook. verbose controls whether full
// statements are written to slog; backend ("sqlite" or "postgres") is
// attached as a label to the emitted Prometheus metrics. slowThreshold logs
// a successful query at WARN once it takes at least that long; 0 disables
// slow-query logging.
func New(verbose bool, backend string, slowThreshold time.Duration) *QueryHook {
	return &QueryHook{Verbose: verbose, Backend: backend, SlowThreshold: slowThreshold}
}

// BeforeQuery is called before executing a query.
func (h *QueryHook) BeforeQuery(ctx context.Context, _ *bun.QueryEvent) context.Context {
	return ctx
}

// AfterQuery is called after executing a query. Emits the query-latency
// histogram, increments busy-retry counter on contention errors, and writes
// a slog line at DEBUG (success, below threshold), WARN (slow success), or
// WARN (failure) level.
func (h *QueryHook) AfterQuery(ctx context.Context, event *bun.QueryEvent) {
	duration := time.Since(event.StartTime)
	operation := event.Operation()

	failed := event.Err != nil && !errors.Is(event.Err, sql.ErrNoRows)

	prommetrics.RecordDBQuery(operation, h.Backend, callsiteFromContext(ctx), duration.Seconds(), !failed)

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

	switch {
	case failed:
		attrs = append(attrs, slog.String("error", event.Err.Error()))
		slog.LogAttrs(ctx, slog.LevelWarn, "SQL query failed", attrs...)
	case h.SlowThreshold > 0 && duration >= h.SlowThreshold:
		h.logSlowQuery(ctx, event, attrs)
	default:
		slog.LogAttrs(ctx, slog.LevelDebug, "SQL query", attrs...)
	}
}

// logSlowQuery emits the "slow SQL query" WARN, throttled to at most once
// per ThrottleInterval per normalized statement. The statement is always
// logged with literal values stripped (normalizeStatement), regardless of
// Verbose — Verbose only controls whether the separate raw "query" attr
// (already appended to attrs by the caller) is present, so a secret literal
// never reaches this line when Verbose is off.
func (h *QueryHook) logSlowQuery(ctx context.Context, event *bun.QueryEvent, attrs []slog.Attr) {
	statement := normalizeStatement(event.Query)
	interval := h.ThrottleInterval
	if interval <= 0 {
		interval = defaultSlowQueryThrottleInterval
	}

	now := time.Now()
	if h.Now != nil {
		now = h.Now()
	}

	h.mu.Lock()
	if h.slowQueries == nil {
		h.slowQueries = make(map[string]*slowQueryState)
	}

	state, seen := h.slowQueries[statement]
	if seen && now.Sub(state.lastLogged) < interval {
		state.suppressed++
		h.mu.Unlock()

		return
	}

	suppressed := 0
	if seen {
		suppressed = state.suppressed
	}

	h.slowQueries[statement] = &slowQueryState{lastLogged: now}
	h.mu.Unlock()

	attrs = append(attrs,
		slog.String("statement", statement),
		slog.Int("suppressed", suppressed),
	)
	slog.LogAttrs(ctx, slog.LevelWarn, "slow SQL query", attrs...)
}

// reStringLiteral and reNumberLiteral strip argument values from a SQL
// statement for logging: single-quoted string literals (with ” escapes)
// and bare numeric literals both collapse to a single placeholder. This is a
// heuristic, not a SQL parser, but it is sufficient to keep secrets and
// other row values out of the slow-query log line.
var (
	reStringLiteral = regexp.MustCompile(`'(?:[^']|'')*'`)
	reNumberLiteral = regexp.MustCompile(`\b\d+(\.\d+)?\b`)
)

// normalizeStatement strips literal values from a SQL statement so the
// result is safe to log even when the query embedded sensitive data, and so
// occurrences of the "same" query with different argument values collapse to
// one throttle key.
func normalizeStatement(query string) string {
	normalized := reStringLiteral.ReplaceAllString(query, "?")
	normalized = reNumberLiteral.ReplaceAllString(normalized, "?")

	return normalized
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
