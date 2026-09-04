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

// portEmbeddedPoolBounded is distinct from every other embedded-Postgres port
// in this package (see the numbering notes in org_usage_counter_postgres_test.go
// and postgres_headroom_postgres_test.go).
const portEmbeddedPoolBounded = 15521

// TestNewEmbedded_PoolBoundedBelowNonSuperuserConnectionLimit is a permanent,
// cheap guard against the regression spec 2026-09-04-04 fixed: NewEmbedded
// pins the embedded server at max_connections=10, 3 of which Postgres
// reserves for superusers, leaving 7 available to this process's own pool.
// If a future change ever widened or removed the applyPoolLimits(sqldb,
// &Config{MaxOpenConns: 5, ...}) call in NewEmbedded, this test fails fast —
// without needing to run a full 50-goroutine concurrency test and wait for a
// 53300 to surface.
//
//nolint:paralleltest // shares dev-machine embedded-postgres resources with its siblings
func TestNewEmbedded_PoolBoundedBelowNonSuperuserConnectionLimit(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping embedded-postgres test in -short mode")
	}

	const nonSuperuserConnectionLimit = 7 // max_connections=10 minus 3 reserved for superusers

	ctx := t.Context()

	s, err := New(ctx, &Config{Embedded: true, Port: portEmbeddedPoolBounded, RunMode: runModeTest})
	if err != nil {
		t.Skipf("embedded postgres unavailable: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	maxOpenConns := s.DB().Stats().MaxOpenConnections
	require.Positive(t, maxOpenConns,
		"NewEmbedded must bound the pool (0 means database/sql's unlimited default)")
	require.Less(t, maxOpenConns, nonSuperuserConnectionLimit,
		"the embedded pool's MaxOpenConns must stay comfortably under the server's non-superuser "+
			"connection limit, or concurrent callers get a hard 53300 instead of queueing")
}
