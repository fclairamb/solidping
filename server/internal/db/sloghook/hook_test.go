package sloghook

import (
	"bytes"
	"database/sql"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/uptrace/bun"
)

// errTestConnectionRefused is a static test-only error (err113 forbids
// dynamic errors.New at the call site).
var errTestConnectionRefused = errors.New("connection refused")

// logCaptureMu serializes every test below that swaps the process-wide slog
// default: they all declare t.Parallel() (required by the paralleltest
// linter), but slog.Default() is global state, so two of these tests
// actually running concurrently would corrupt each other's captured output.
// The mutex makes that impossible without dropping t.Parallel().
var logCaptureMu sync.Mutex //nolint:gochecknoglobals // test-only serialization lock

// captureLogs swaps the default slog handler for the duration of the test and
// returns the accumulated output. Mirrors internal/app's saas_test.go helper,
// plus the logCaptureMu serialization above.
func captureLogs(t *testing.T) *bytes.Buffer {
	t.Helper()

	logCaptureMu.Lock()

	buf := &bytes.Buffer{}
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() {
		slog.SetDefault(previous)
		logCaptureMu.Unlock()
	})

	return buf
}

// TestSlowQueryLogsWARN is table-driven across both backends: a query slower
// than SlowThreshold produces exactly one WARN line naming the statement and
// its duration.
func TestSlowQueryLogsWARN(t *testing.T) {
	t.Parallel()

	for _, backend := range []string{"postgres", "sqlite"} {
		t.Run(backend, func(t *testing.T) {
			t.Parallel()

			r := require.New(t)

			buf := captureLogs(t)
			hook := &QueryHook{Backend: backend, SlowThreshold: 50 * time.Millisecond}

			event := &bun.QueryEvent{
				Query:     "SELECT * FROM results WHERE organization_uid = 'org-1'",
				StartTime: time.Now().Add(-100 * time.Millisecond),
			}
			hook.AfterQuery(t.Context(), event)

			logs := buf.String()
			r.Contains(logs, "slow SQL query")
			r.Contains(logs, "duration=")
			r.Contains(logs, "statement=")
		})
	}
}

// TestFastQueryDoesNotLogWARN is the positive control for TestSlowQueryLogsWARN:
// a query below the threshold must never produce a WARN line.
func TestFastQueryDoesNotLogWARN(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	buf := captureLogs(t)
	hook := &QueryHook{Backend: "postgres", SlowThreshold: 50 * time.Millisecond}

	event := &bun.QueryEvent{
		Query:     "SELECT 1",
		StartTime: time.Now(),
	}
	hook.AfterQuery(t.Context(), event)

	r.NotContains(buf.String(), "level=WARN")
}

// TestZeroThresholdDisablesSlowQueryLogging proves SlowThreshold=0 (the zero
// value) never logs "slow SQL query", however slow the query actually was —
// the documented "0 disables" behavior.
func TestZeroThresholdDisablesSlowQueryLogging(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	buf := captureLogs(t)
	hook := &QueryHook{Backend: "postgres"} // SlowThreshold left at zero value

	event := &bun.QueryEvent{
		Query:     "SELECT 1",
		StartTime: time.Now().Add(-10 * time.Second),
	}
	hook.AfterQuery(t.Context(), event)

	r.NotContains(buf.String(), "slow SQL query")
}

// TestSlowQueryRedactsSecretWithVerboseFalse is the acceptance test the spec
// calls for: with Verbose=false, a statement carrying a secret literal is
// logged without that literal ever reaching the line — normalizeStatement
// strips it unconditionally, regardless of Verbose.
func TestSlowQueryRedactsSecretWithVerboseFalse(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	buf := captureLogs(t)
	hook := &QueryHook{Backend: "postgres", Verbose: false, SlowThreshold: 10 * time.Millisecond}

	const secret = "sk_live_supersecret12345"

	event := &bun.QueryEvent{
		Query:     "UPDATE users SET password_hash = '" + secret + "' WHERE id = 42",
		StartTime: time.Now().Add(-time.Second),
	}
	hook.AfterQuery(t.Context(), event)

	logs := buf.String()
	r.Contains(logs, "slow SQL query")
	r.NotContains(logs, secret)
	// The normalized statement attr must still be present (proves redaction
	// replaced the literal rather than dropping the statement entirely).
	r.Contains(logs, "UPDATE users SET password_hash")
}

// TestSlowQueryVerboseAlsoRedactsStatementAttr proves the "statement" attr
// specifically is always normalized, even when Verbose=true adds the
// separate raw "query" attr alongside it.
func TestSlowQueryVerboseAlsoRedactsStatementAttr(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	buf := captureLogs(t)
	hook := &QueryHook{Backend: "postgres", Verbose: true, SlowThreshold: 10 * time.Millisecond}

	const secret = "sk_live_supersecret12345"

	event := &bun.QueryEvent{
		Query:     "UPDATE users SET password_hash = '" + secret + "' WHERE id = 42",
		StartTime: time.Now().Add(-time.Second),
	}
	hook.AfterQuery(t.Context(), event)

	logs := buf.String()
	// Verbose=true still exposes the raw query verbatim via the separate
	// "query" attr — that half of the existing contract is unchanged.
	r.Contains(logs, "query=")

	// But the normalized "statement" attr never carries the literal.
	statementIdx := strings.Index(logs, "statement=")
	r.GreaterOrEqual(statementIdx, 0)
	r.NotContains(logs[statementIdx:], secret)
}

// TestSlowQueryThrottleSuppressesRepeats proves repeated slow occurrences of
// the same normalized statement inside the throttle interval collapse to one
// WARN line, and that the next line (once the interval elapses) carries the
// suppressed count.
func TestSlowQueryThrottleSuppressesRepeats(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	buf := captureLogs(t)

	now := time.Now()
	hook := &QueryHook{
		Backend:          "postgres",
		SlowThreshold:    10 * time.Millisecond,
		ThrottleInterval: time.Minute,
		Now:              func() time.Time { return now },
	}

	// StartTime is based on the real wall clock (duration = time.Since(StartTime)
	// always reads real elapsed time), independent of hook.Now — which only
	// drives the throttle bucketing below. Advancing the fake `now` into the
	// future must not make the query look instantaneous (or negative-duration).
	newEvent := func() *bun.QueryEvent {
		return &bun.QueryEvent{
			Query:     "SELECT * FROM results WHERE check_uid = 'c1'",
			StartTime: time.Now().Add(-time.Second),
		}
	}

	hook.AfterQuery(t.Context(), newEvent()) // logs
	hook.AfterQuery(t.Context(), newEvent()) // suppressed
	hook.AfterQuery(t.Context(), newEvent()) // suppressed

	r.Equal(1, strings.Count(buf.String(), "slow SQL query"),
		"repeated slow occurrences within the throttle interval must collapse to one line")
	r.NotContains(buf.String(), "suppressed=1")
	r.NotContains(buf.String(), "suppressed=2")

	// Advance past the throttle interval and log again: the new line must
	// carry how many occurrences were suppressed in between.
	now = now.Add(2 * time.Minute)
	buf.Reset()
	hook.AfterQuery(t.Context(), newEvent())

	r.Equal(1, strings.Count(buf.String(), "slow SQL query"))
	r.Contains(buf.String(), "suppressed=2")
}

// TestFailedQueryStillLogsWARNRegardlessOfThreshold proves the existing
// failure path is unchanged: it always logs at WARN, even when SlowThreshold
// is unset, and even when the failure was fast.
func TestFailedQueryStillLogsWARNRegardlessOfThreshold(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	buf := captureLogs(t)
	hook := &QueryHook{Backend: "postgres"}

	event := &bun.QueryEvent{
		Query:     "SELECT 1",
		StartTime: time.Now(),
		Err:       errTestConnectionRefused,
	}
	hook.AfterQuery(t.Context(), event)

	r.Contains(buf.String(), "SQL query failed")
}

// TestErrNoRowsIsNotAFailure proves sql.ErrNoRows never logs at "SQL query
// failed" — it must never be treated as a query failure.
func TestErrNoRowsIsNotAFailure(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	buf := captureLogs(t)
	hook := &QueryHook{Backend: "postgres"}

	event := &bun.QueryEvent{
		Query:     "SELECT 1",
		StartTime: time.Now(),
		Err:       sql.ErrNoRows,
	}
	hook.AfterQuery(t.Context(), event)

	r.NotContains(buf.String(), "SQL query failed")
}
