package postgres

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect/pgdialect"
	"github.com/uptrace/bun/driver/pgdriver"
)

// Ports 15438-15444 below are distinct from every other _postgres_test.go
// file in this repo (5433 is the app default; 15434/15437 are already used
// by costdist/checkjobsvc's embedded-Postgres tests) and from each other, so
// every test in this file gets its own embedded instance with no port
// collision. Tests still don't call t.Parallel() (see the //nolint on each):
// running 7 real embedded-Postgres subprocesses concurrently starved this
// dev machine and made most of them self-skip via the "unavailable" path —
// resource contention, not a correctness issue — so they run sequentially.
const (
	portHeadroomWarns           = 15438
	portHeadroomUnlimited       = 15439
	portHeadroomComfortable     = 15440
	portHeadroomUnbounded       = 15441
	portHeadroomPermissionError = 15442
	portIdleTimeReaps           = 15443
	portIdleTimeZeroMeansNoReap = 15444
)

// startEmbeddedForHeadroomTest boots one embedded PostgreSQL instance for the
// calling test (each CREATE ROLE runs against it) and returns the superuser
// Service plus a DSN builder for connecting as another role. Self-skips
// exactly like costdist_postgres_test.go: -short mode or any embedded-startup
// error, so it only runs when the full suite is invoked without -short.
func startEmbeddedForHeadroomTest(t *testing.T, port uint32) (*Service, func(role, password string) string) {
	t.Helper()

	if testing.Short() {
		t.Skip("skipping embedded-postgres test in -short mode")
	}

	ctx := context.Background()

	dbSvc, err := New(ctx, &Config{
		Embedded: true,
		Port:     port,
		RunMode:  "test",
	})
	if err != nil {
		t.Skipf("embedded postgres unavailable: %v", err)
	}
	t.Cleanup(func() { _ = dbSvc.Close() })

	if initErr := dbSvc.Initialize(ctx); initErr != nil {
		t.Skipf("embedded postgres init failed: %v", initErr)
	}

	dsnFor := func(role, password string) string {
		return fmt.Sprintf(
			"postgres://%s:%s@localhost:%d/solidping_test?sslmode=disable", role, password, port,
		)
	}

	return dbSvc, dsnFor
}

// connectAs opens a fresh bun.DB against the embedded instance as the given
// role/password, mirroring the same sql.OpenDB/bun.NewDB construction New()
// uses for a real (non-embedded) connection.
func connectAs(t *testing.T, dsn string) *bun.DB {
	t.Helper()

	sqldb := sql.OpenDB(pgdriver.NewConnector(pgdriver.WithDSN(dsn)))
	t.Cleanup(func() { _ = sqldb.Close() })

	bunDB := bun.NewDB(sqldb, pgdialect.New())
	t.Cleanup(func() { _ = bunDB.Close() })

	return bunDB
}

// captureWarnLogs swaps in a buffer-backed slog handler at slog.LevelWarn for
// the duration of the test and restores the previous default afterward.
func captureWarnLogs(t *testing.T) *bytes.Buffer {
	t.Helper()

	var buf bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn})))
	t.Cleanup(func() { slog.SetDefault(previous) })

	return &buf
}

// createRoleWithConnLimit creates a LOGIN role with the given CONNECTION
// LIMIT against the superuser Service, granting it read access implied by
// Postgres defaults (pg_roles.rolconnlimit and one's own pg_stat_activity
// rows are readable by any role without an explicit GRANT).
func createRoleWithConnLimit(t *testing.T, dbSvc *Service, role string, connLimit int) {
	t.Helper()

	_, err := dbSvc.DB().ExecContext(context.Background(),
		fmt.Sprintf(`CREATE ROLE %s LOGIN PASSWORD 'testpass' CONNECTION LIMIT %d`, role, connLimit))
	require.NoError(t, err)
}

// TestCheckRoleConnLimitHeadroom_WarnsWhenMaxOpenConnsExceedsLimit covers
// acceptance criterion 3 (spec 2026-07-05-09, D3): a role whose
// rolconnlimit is at or below the configured MaxOpenConns triggers the
// startup WARN, naming both numbers plus current usage.
//
//nolint:paralleltest // shares dev-machine resources with sibling embedded-postgres tests in this file
func TestCheckRoleConnLimitHeadroom_WarnsWhenMaxOpenConnsExceedsLimit(t *testing.T) {
	dbSvc, dsnFor := startEmbeddedForHeadroomTest(t, portHeadroomWarns)
	createRoleWithConnLimit(t, dbSvc, "sp_headroom_low", 2)

	buf := captureWarnLogs(t)

	limitedDB := connectAs(t, dsnFor("sp_headroom_low", "testpass"))
	// MaxOpenConns (5) >= rolconnlimit (2): must warn.
	checkRoleConnLimitHeadroom(context.Background(), limitedDB, 5)

	logOutput := buf.String()
	require.Contains(t, logOutput, roleConnLimitHeadroomWarning)
	require.Contains(t, logOutput, "maxOpenConns=5")
	require.Contains(t, logOutput, "rolConnLimit=2")
	require.Contains(t, logOutput, "currentUsage=")
}

// TestCheckRoleConnLimitHeadroom_UnlimitedRoleSilent is the negative case for
// acceptance criterion 3: rolconnlimit = -1 (unlimited) must never emit the
// warning, however large MaxOpenConns is.
//
//nolint:paralleltest // shares dev-machine resources with sibling embedded-postgres tests in this file
func TestCheckRoleConnLimitHeadroom_UnlimitedRoleSilent(t *testing.T) {
	dbSvc, dsnFor := startEmbeddedForHeadroomTest(t, portHeadroomUnlimited)
	createRoleWithConnLimit(t, dbSvc, "sp_headroom_unlimited", -1)

	buf := captureWarnLogs(t)

	unlimitedDB := connectAs(t, dsnFor("sp_headroom_unlimited", "testpass"))
	checkRoleConnLimitHeadroom(context.Background(), unlimitedDB, 100)

	require.Empty(t, buf.String(), "unlimited role (rolconnlimit=-1) must never emit the headroom warning")
}

// TestCheckRoleConnLimitHeadroom_FitsComfortablySilent covers the "fits"
// branch of acceptance criterion 3: rolconnlimit strictly greater than
// MaxOpenConns must stay silent — the warning is only for the >= case.
//
//nolint:paralleltest // shares dev-machine resources with sibling embedded-postgres tests in this file
func TestCheckRoleConnLimitHeadroom_FitsComfortablySilent(t *testing.T) {
	dbSvc, dsnFor := startEmbeddedForHeadroomTest(t, portHeadroomComfortable)
	createRoleWithConnLimit(t, dbSvc, "sp_headroom_comfortable", 50)

	buf := captureWarnLogs(t)

	comfortableDB := connectAs(t, dsnFor("sp_headroom_comfortable", "testpass"))
	checkRoleConnLimitHeadroom(context.Background(), comfortableDB, 8)

	require.Empty(t, buf.String(), "rolconnlimit (50) comfortably above MaxOpenConns (8) must stay silent")
}

// TestCheckRoleConnLimitHeadroom_UnboundedPoolSilent covers the maxOpenConns
// <= 0 early return: an unbounded local pool has nothing meaningful to
// compare against rolconnlimit, so it must never warn either.
//
//nolint:paralleltest // shares dev-machine resources with sibling embedded-postgres tests in this file
func TestCheckRoleConnLimitHeadroom_UnboundedPoolSilent(t *testing.T) {
	dbSvc, dsnFor := startEmbeddedForHeadroomTest(t, portHeadroomUnbounded)
	createRoleWithConnLimit(t, dbSvc, "sp_headroom_unbounded", 1)

	buf := captureWarnLogs(t)

	unboundedDB := connectAs(t, dsnFor("sp_headroom_unbounded", "testpass"))
	checkRoleConnLimitHeadroom(context.Background(), unboundedDB, 0)

	require.Empty(t, buf.String(), "MaxOpenConns <= 0 (unbounded) must never warn")
}

// TestCheckRoleConnLimitHeadroom_PermissionErrorSilent covers the "skip
// silently on permission errors" requirement (D3): querying a nonexistent
// role name or a connection that fails mid-query must not warn and — the
// property that matters most — must never propagate an error to the caller,
// since New() calls this without checking a return value.
//
//nolint:paralleltest // shares dev-machine resources with sibling embedded-postgres tests in this file
func TestCheckRoleConnLimitHeadroom_PermissionErrorSilent(t *testing.T) {
	_, dsnFor := startEmbeddedForHeadroomTest(t, portHeadroomPermissionError)

	buf := captureWarnLogs(t)

	// A closed DB makes every query fail, standing in for a permission error
	// without needing a managed-Postgres instance that actually restricts
	// pg_roles — the code path (query fails -> return silently) is identical.
	closedDB := connectAs(t, dsnFor("postgres", "postgres"))
	require.NoError(t, closedDB.Close())

	require.NotPanics(t, func() {
		checkRoleConnLimitHeadroom(context.Background(), closedDB, 5)
	})
	require.Empty(t, buf.String(), "a failing headroom query must skip silently, never warn or panic")
}

// TestApplyPoolLimits_ConnMaxIdleTime_Reaps proves D2 end to end through
// applyPoolLimits itself: a connection idle past ConnMaxIdleTime is actually
// closed by database/sql, observable via sql.DB.Stats().MaxIdleTimeClosed.
// That is a behavioral assertion (a connection was really reaped), not just
// a check that the setter was called.
//
//nolint:paralleltest // shares dev-machine resources with sibling embedded-postgres tests in this file
func TestApplyPoolLimits_ConnMaxIdleTime_Reaps(t *testing.T) {
	_, dsnFor := startEmbeddedForHeadroomTest(t, portIdleTimeReaps)
	ctx := context.Background()
	r := require.New(t)

	sqldb := sql.OpenDB(pgdriver.NewConnector(pgdriver.WithDSN(dsnFor("postgres", "postgres"))))
	t.Cleanup(func() { _ = sqldb.Close() })

	applyPoolLimits(sqldb, &Config{
		MaxOpenConns:    5,
		MaxIdleConns:    5,
		ConnMaxIdleTime: 50 * time.Millisecond, // short so the test doesn't wait for the 5m production default
	})

	// Establish a real connection, then let it go idle. Deliberately do NOT
	// touch the pool again after this: database/sql's idle-connection
	// cleaner runs on its own background ticker and reaps based on each
	// connection's last-returned-to-pool timestamp, so pinging again would
	// reset that timestamp and the connection would never accumulate enough
	// idle time to be reaped.
	r.NoError(sqldb.PingContext(ctx))
	r.Positive(sqldb.Stats().Idle)

	deadline := time.Now().Add(5 * time.Second)
	for sqldb.Stats().MaxIdleTimeClosed == 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}

	r.Positive(sqldb.Stats().MaxIdleTimeClosed,
		"expected at least one connection closed for exceeding ConnMaxIdleTime")
}

// TestApplyPoolLimits_ConnMaxIdleTime_ZeroMeansNoReap confirms the zero-value
// guard: ConnMaxIdleTime left at 0 must not reap idle connections (matches
// the existing MaxOpenConns/MaxIdleConns/ConnMaxLifetime "0 = leave
// database/sql's default" convention).
//
//nolint:paralleltest // shares dev-machine resources with sibling embedded-postgres tests in this file
func TestApplyPoolLimits_ConnMaxIdleTime_ZeroMeansNoReap(t *testing.T) {
	_, dsnFor := startEmbeddedForHeadroomTest(t, portIdleTimeZeroMeansNoReap)
	ctx := context.Background()
	r := require.New(t)

	sqldb := sql.OpenDB(pgdriver.NewConnector(pgdriver.WithDSN(dsnFor("postgres", "postgres"))))
	t.Cleanup(func() { _ = sqldb.Close() })

	applyPoolLimits(sqldb, &Config{MaxOpenConns: 5, MaxIdleConns: 5}) // ConnMaxIdleTime left at zero value

	r.NoError(sqldb.PingContext(ctx))
	time.Sleep(200 * time.Millisecond)
	r.NoError(sqldb.PingContext(ctx))

	r.Zero(sqldb.Stats().MaxIdleTimeClosed, "ConnMaxIdleTime=0 must never reap idle connections")
}
