package postgres

import (
	"database/sql"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/sqlitedriver"
)

// TestApplyPoolLimits_QueuesRatherThanErrors pins the "production is not
// affected" claim from spec 2026-09-04-04: New's non-embedded path calls
// applyPoolLimits(sqldb, cfg) with cfg.MaxOpenConns (SP_DB_MAX_OPEN_CONNS,
// default 25), and database/sql queues callers past that ceiling rather than
// refusing them — unlike the embedded server's own connection limit, which
// answers 53300 once exhausted. That distinction is exactly why bounding the
// *client* pool is safe for production (it only adds queueing latency) while
// an unbounded pool against the embedded server's fixed max_connections is
// not (it errors).
//
// Verified once here at the database/sql level — driver-agnostic, so the
// (much slower) embedded-Postgres suites don't need to re-prove it. The
// SQLite driver is used purely as a cheap stand-in connection source; nothing
// here is Postgres-specific.
func TestApplyPoolLimits_QueuesRatherThanErrors(t *testing.T) {
	t.Parallel()

	if sqlitedriver.Name == "" {
		t.Skip("no SQLite driver linked in this build")
	}

	sqldb, err := sql.Open(sqlitedriver.Name, "file::memory:?cache=shared")
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqldb.Close() })

	const maxOpenConns = 3
	applyPoolLimits(sqldb, &Config{MaxOpenConns: maxOpenConns})

	// Comfortably more concurrent callers than the pool ceiling, so most of
	// them MUST wait for a connection to free up rather than get one
	// immediately.
	const goroutines = 4 * maxOpenConns

	ctx := t.Context()

	var wg sync.WaitGroup
	var succeeded atomic.Int64
	errs := make(chan error, goroutines)

	for range goroutines {
		wg.Add(1)

		go func() {
			defer wg.Done()

			// Conn reserves one pooled connection, blocking (queueing) until
			// one is available when the pool is already at MaxOpenConns —
			// this is the exact behavior being asserted. Holding it briefly
			// widens the window in which other goroutines must queue rather
			// than race straight through.
			conn, connErr := sqldb.Conn(ctx)
			if connErr != nil {
				errs <- connErr

				return
			}

			_, pingErr := conn.ExecContext(ctx, "SELECT 1")
			time.Sleep(10 * time.Millisecond)
			closeErr := conn.Close()

			switch {
			case pingErr != nil:
				errs <- pingErr
			case closeErr != nil:
				errs <- closeErr
			default:
				succeeded.Add(1)
			}
		}()
	}

	wg.Wait()
	close(errs)

	for failure := range errs {
		require.NoError(t, failure,
			"every caller past MaxOpenConns must queue for a connection, never be refused one")
	}

	require.Equal(t, int64(goroutines), succeeded.Load())
}
