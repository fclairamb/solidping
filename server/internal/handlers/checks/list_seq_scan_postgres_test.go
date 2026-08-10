package checks_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/postgres"
	entcore "github.com/fclairamb/solidping/server/internal/entitlements"
	"github.com/fclairamb/solidping/server/internal/handlers/checks"
	"github.com/fclairamb/solidping/server/internal/notifier"
)

// Port for the checks-list scan-accounting suite. Distinct from every other
// _postgres_test.go port claimed in the repo (15438-15467 — see the
// port-numbering comment in db/postgres/postgres_headroom_postgres_test.go).
const portListSeqScanPG = 15468

// The fixture deliberately reproduces the organization from spec
// 2026-08-09-07: 356 checks, each with a long raw history. That shape is what
// made the planner (correctly) prefer a sequential scan for the old
// DISTINCT ON — a `check_uid IN (...)` list covering most of the table — so a
// smaller fixture would not exercise the regression this test guards.
const (
	seqScanCheckCount   = 356
	seqScanRowsPerCheck = 200
	// listWallClockBudget is a SMOKE guard, not the proof. See the comment on
	// the wall-clock assertion in the test body for why the spec's literal
	// "<100 ms" number is not asserted as such.
	listWallClockBudget = 2 * time.Second
)

// resultsTableStats is the slice of pg_stat_user_tables this test meters for
// the `results` table.
type resultsTableStats struct {
	SeqScan     int64 `bun:"seq_scan"`
	SeqTupRead  int64 `bun:"seq_tup_read"`
	IdxScan     int64 `bun:"idx_scan"`
	IdxTupFetch int64 `bun:"idx_tup_fetch"`
}

// readResultsStats reads the cumulative statistics for `results`. Postgres
// caches the stats snapshot per transaction, so the snapshot is dropped first
// — otherwise repeated reads inside one session can return identical numbers
// regardless of what happened in between.
func readResultsStats(ctx context.Context, t *testing.T, pg *postgres.Service) resultsTableStats {
	t.Helper()
	r := require.New(t)

	_, err := pg.DB().ExecContext(ctx, "SELECT pg_stat_clear_snapshot()")
	r.NoError(err)

	var stats resultsTableStats
	err = pg.DB().NewRaw(`
		SELECT seq_scan, seq_tup_read, idx_scan, idx_tup_fetch
		FROM pg_stat_user_tables
		WHERE relname = 'results'
	`).Scan(ctx, &stats)
	r.NoError(err)

	return stats
}

// waitForStatsSettled waits for the cumulative `results` statistics to (a)
// move past the given baseline and then (b) stop moving across several
// consecutive reads, and returns the settled snapshot.
//
// Both halves are load-bearing. Backends flush their pending statistics
// asynchronously, so asserting "seq_scan did not move" straight after a query
// would be satisfied trivially by a not-yet-flushed counter. And returning on
// the FIRST dimension to move would be just as unsound: a burst can be spread
// over several pooled backends, so an index-scan flush from one backend could
// end the wait while another backend's sequential-scan flush was still
// pending. Waiting for quiescence means every backend that participated has
// reported before anything is asserted.
func waitForStatsSettled(
	ctx context.Context, t *testing.T, pg *postgres.Service, baseline resultsTableStats,
) resultsTableStats {
	t.Helper()

	// Postgres forces a pending-stats flush at ~1 s intervals, so the quiet
	// window has to comfortably exceed that.
	const (
		pollInterval    = 250 * time.Millisecond
		stableReadsWant = 6
		waitDeadline    = 30 * time.Second
	)

	deadline := time.Now().Add(waitDeadline)
	var last resultsTableStats
	moved, stableReads := false, 0

	for time.Now().Before(deadline) {
		current := readResultsStats(ctx, t, pg)

		if !moved && current != baseline {
			moved = true
			last = current
			stableReads = 1
		} else if moved {
			if current == last {
				stableReads++
				if stableReads >= stableReadsWant {
					return current
				}
			} else {
				last = current
				stableReads = 1
			}
		}

		time.Sleep(pollInterval)
	}

	require.Failf(t, "statistics never settled",
		"pg_stat_user_tables for `results` never moved past the baseline and went quiet "+
			"within %s (baseline %+v, last %+v, moved=%v)", waitDeadline, baseline, last, moved)

	return last
}

// readTempBytes returns the database's cumulative temp-file spill. The former
// queries sorted every matching raw row to disk (24 MB and 17 MB of external
// merge per request in production); the rewrite sorts nothing at all.
func readTempBytes(ctx context.Context, t *testing.T, pg *postgres.Service) int64 {
	t.Helper()
	r := require.New(t)

	_, err := pg.DB().ExecContext(ctx, "SELECT pg_stat_clear_snapshot()")
	r.NoError(err)

	var row struct {
		TempBytes int64 `bun:"temp_bytes"`
	}
	r.NoError(pg.DB().NewRaw(
		"SELECT temp_bytes FROM pg_stat_database WHERE datname = current_database()",
	).Scan(ctx, &row))

	return row.TempBytes
}

// seedSeqScanFixture creates seqScanCheckCount checks, each with a recorded
// status transition and seqScanRowsPerCheck raw results. Rows go in through
// bulk statements rather than one CreateCheck/CreateResult round-trip apiece:
// ~71 000 individual inserts would dominate the test's runtime, and nothing
// here depends on the write path (which the rest of the suite covers).
func seedSeqScanFixture(ctx context.Context, t *testing.T, pg *postgres.Service, orgUID string) {
	t.Helper()
	r := require.New(t)

	rows := make([]*models.Check, 0, seqScanCheckCount)
	for i := 0; i < seqScanCheckCount; i++ {
		check := models.NewCheck(orgUID, fmt.Sprintf("check-%03d", i), "http")
		rows = append(rows, check)
	}
	_, err := pg.DB().NewInsert().Model(&rows).Exec(ctx)
	r.NoError(err)

	// Every check has a real, derived status transition on record — the value
	// `with=last_status_change` now serves.
	_, err = pg.DB().ExecContext(ctx, `
		UPDATE checks
		SET status = 3, status_streak = 1, status_changed_at = now() - interval '34 days'
		WHERE organization_uid = ?
	`, orgUID)
	r.NoError(err)

	// One raw row per (check, minute). The org's raw history is two orders of
	// magnitude larger than its check count, exactly like production.
	_, err = pg.DB().ExecContext(ctx, `
		INSERT INTO results (organization_uid, check_uid, period_type, period_start, status, duration)
		SELECT c.organization_uid, c.uid, 'raw', now() - (g * interval '1 minute'), 3, 12.5
		FROM checks c, generate_series(1, ?) AS g
		WHERE c.organization_uid = ?
	`, seqScanRowsPerCheck, orgUID)
	r.NoError(err)

	// Give the planner real statistics: without ANALYZE it works off default
	// estimates and its plan choice says nothing about production behavior.
	_, err = pg.DB().ExecContext(ctx, "ANALYZE results, checks")
	r.NoError(err)
}

// TestListChecks_DoesNotScanResults_Postgres is the acceptance test for spec
// 2026-08-09-07: `GET /checks?with=last_result,last_status_change` must
// perform ZERO sequential scans of `results`, must spill no temp files, and
// must read a bounded number of tuples — one per check, not the
// organization's whole raw history.
//
// All three assertions matter. seq_scan is the spec's literal criterion; the
// tuple budget is the one that fails against the old implementation whatever
// plan the planner happens to pick (the old DISTINCT ON read and sorted every
// raw row of the org, and the old LAG query read them a second time).
// Measured by restoring the old DISTINCT ON against this fixture: it reads
// 213 600 tuples where the budget is 4 272 — and on THIS fixture it reached
// them through an index scan, i.e. seq_scan alone would not have caught it.
// That is precisely why the tuple budget is here and not just the counter the
// acceptance criterion names.
//
// The test carries its own positive control — a deliberate sequential scan
// that MUST be observed — so a broken or stalled statistics view can never
// make the negative assertions pass vacuously.
//
// Not parallel: it boots its own embedded Postgres, and the embedded suites
// share one extraction directory, so they run one at a time.
//
//nolint:paralleltest // see the note just above
func TestListChecks_DoesNotScanResults_Postgres(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping embedded-postgres test in -short mode")
	}

	r := require.New(t)
	ctx := t.Context()

	dbSvc, err := postgres.New(ctx, &postgres.Config{
		Embedded: true, Port: portListSeqScanPG, RunMode: "test",
	})
	if err != nil {
		t.Skipf("embedded postgres unavailable: %v", err)
	}
	t.Cleanup(func() { _ = dbSvc.Close() })

	if initErr := dbSvc.Initialize(ctx); initErr != nil {
		t.Skipf("embedded postgres init failed: %v", initErr)
	}

	entSvc := entcore.NewService(dbSvc, entcore.DefaultsFor(config.DeploymentModeSelfHosted), 0)
	svc := checks.NewService(dbSvc, notifier.NewLocalEventNotifier(), disabledCreds(t), entSvc)

	org := models.NewOrganization("seqscan-org", "Seq Scan Org")
	r.NoError(dbSvc.CreateOrganization(ctx, org))
	seedSeqScanFixture(ctx, t, dbSvc, org.UID)

	// --- Positive control -------------------------------------------------
	// A query that can only be answered by a sequential scan MUST show up in
	// the counter. If it doesn't, the meter is broken and every assertion
	// below would be meaningless.
	control := readResultsStats(ctx, t, dbSvc)
	_, err = dbSvc.DB().ExecContext(ctx, "SELECT count(*) FROM results")
	r.NoError(err)

	after := waitForStatsSettled(ctx, t, dbSvc, control)
	r.Greater(after.SeqScan, control.SeqScan,
		"positive control: a deliberate full scan must be counted by pg_stat_user_tables")

	// --- The assertion ----------------------------------------------------
	before := readResultsStats(ctx, t, dbSvc)
	tempBefore := readTempBytes(ctx, t, dbSvc)

	const bursts = 3
	fastest := time.Duration(0)
	for i := 0; i < bursts; i++ {
		start := time.Now()
		resp, listErr := svc.ListChecks(ctx, org.Slug, checks.ListChecksOptions{
			IncludeLastResult:       true,
			IncludeLastStatusChange: true,
			Limit:                   seqScanCheckCount + 1,
		})
		elapsed := time.Since(start)
		r.NoError(listErr)
		r.Len(resp.Data, seqScanCheckCount)
		// Sanity: the embeds really were produced, so the burst exercised
		// the code paths this test is metering.
		r.NotNil(resp.Data[0].LastResult)
		r.NotNil(resp.Data[0].LastStatusChange)

		if fastest == 0 || elapsed < fastest {
			fastest = elapsed
		}
	}

	settled := waitForStatsSettled(ctx, t, dbSvc, before)

	r.Equal(before.SeqScan, settled.SeqScan,
		"a checks list with last_result + last_status_change must not sequentially scan `results`")
	r.Equal(before.SeqTupRead, settled.SeqTupRead,
		"no tuples may be read through a sequential scan of `results`")

	r.Equal(tempBefore, readTempBytes(ctx, t, dbSvc),
		"steady-state dashboard polling must not spill any temp files: nothing in this path sorts")

	// One index descent per check, not the org's raw history. With 356 checks
	// × 200 raw rows each, the old implementation had to touch all ~71 200
	// rows per request; this budget is ~4 tuples per check.
	tuplesRead := settled.IdxTupFetch - before.IdxTupFetch
	r.LessOrEqual(tuplesRead, int64(bursts*4*seqScanCheckCount),
		"each list must read ~one raw row per check (%d checks × %d rows of history each), "+
			"never the organization's whole raw history", seqScanCheckCount, seqScanRowsPerCheck)

	// --- On the spec's "<100 ms for a 356-check org" --------------------
	// The spec measured 47 ms (new) against 1690 ms (old) on the production
	// database — a 443 MB hot table, its own hardware, its own cache state.
	// Asserting that 100 ms constant here would measure the CI runner and the
	// embedded-Postgres cold cache, not the query, so it would either flake or
	// have to be padded until it proved nothing. What actually generalizes
	// from the 47 ms figure is the WORK the query does, and that is asserted
	// above, exactly: zero sequential scans, zero temp spill, and ~one tuple
	// per check on a fixture with this spec's own 356-check shape.
	//
	// The bound below exists only as a coarse smoke guard for a catastrophic
	// plan regression; it is intentionally ~20× the spec's budget so a loaded
	// runner can never trip it. The measured duration is logged so a human
	// reading a CI run can still see the real number.
	t.Logf("ListChecks over %d checks (%d raw rows each): fastest of %d bursts = %s",
		seqScanCheckCount, seqScanRowsPerCheck, bursts, fastest)
	r.Less(fastest, listWallClockBudget,
		"listing %d checks with both embeds took %s — far past anything a per-check index "+
			"descent can cost, so the plan has regressed", seqScanCheckCount, fastest)
}
