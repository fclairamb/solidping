package checks_test

import (
	"context"
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

// waitForStatsActivity polls until the cumulative stats for `results` move
// past the given baseline in ANY dimension, or the deadline passes. Backends
// flush their pending statistics asynchronously (up to ~1 s after the
// statement), so asserting "seq_scan did not move" straight after a query
// would otherwise be satisfied trivially by a not-yet-flushed counter.
// Returning only once the just-executed work is visible is what makes the
// negative assertion meaningful.
func waitForStatsActivity(
	ctx context.Context, t *testing.T, pg *postgres.Service, baseline resultsTableStats,
) resultsTableStats {
	t.Helper()

	deadline := time.Now().Add(10 * time.Second)
	var stats resultsTableStats
	for time.Now().Before(deadline) {
		stats = readResultsStats(ctx, t, pg)
		if stats.SeqScan > baseline.SeqScan ||
			stats.IdxScan > baseline.IdxScan ||
			stats.SeqTupRead > baseline.SeqTupRead ||
			stats.IdxTupFetch > baseline.IdxTupFetch {
			return stats
		}
		time.Sleep(100 * time.Millisecond)
	}

	require.Failf(t, "statistics never moved",
		"no change in pg_stat_user_tables for `results` within the deadline (baseline %+v)", baseline)

	return stats
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

// TestListChecks_DoesNotScanResults_Postgres is the acceptance test for spec
// 2026-08-09-07: `GET /checks?with=last_result,last_status_change` must
// perform ZERO sequential scans of `results`, and must read a bounded number
// of tuples from it — one per check, not the organization's whole raw history.
//
// Both halves matter. The seq_scan assertion is the spec's literal criterion,
// but on a small fixture the planner might avoid a sequential scan even for a
// bad query, so the tuple budget is the assertion that fails against the old
// implementation regardless of the plan the planner picks: the former
// DISTINCT ON read (and sorted) every raw row of the organization, and the
// former LAG query read them a second time.
//
// The test carries its own positive control — a deliberate sequential scan
// that MUST be observed — so a broken or stalled statistics view can never
// make the negative assertion pass vacuously.
//
// extraction) with the other embedded suites
//
//nolint:paralleltest // shares dev-machine resources (embedded-postgres-go's pwfile
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

	// A fleet whose raw history dwarfs the check count — the shape that made
	// the planner (correctly) prefer a sequential scan in production.
	const (
		checkCount   = 12
		rowsPerCheck = 250
		tupleBudget  = 4 * checkCount // generous: the index descent costs ~1 tuple per check
	)

	for i := 0; i < checkCount; i++ {
		check := models.NewCheck(org.UID, "check-"+string(rune('a'+i)), "http")
		r.NoError(dbSvc.CreateCheck(ctx, check))

		changedAt := time.Now().Add(-time.Duration(i+1) * 24 * time.Hour)
		r.NoError(dbSvc.UpdateCheckStatusAndClocks(
			ctx, check.UID, models.CheckStatusUp, 1, &changedAt, models.IncidentClockUpdate{},
		))

		base := time.Now().Add(-time.Duration(rowsPerCheck) * time.Minute)
		for j := 0; j < rowsPerCheck; j++ {
			result := models.NewResult(org.UID, check.UID, models.ResultStatusUp, float32(10+j%40))
			result.PeriodStart = base.Add(time.Duration(j) * time.Minute)
			r.NoError(dbSvc.CreateResult(ctx, result))
		}
	}

	// Give the planner real statistics: without ANALYZE it works off default
	// estimates and its plan choice says nothing about production behavior.
	_, err = dbSvc.DB().ExecContext(ctx, "ANALYZE results")
	r.NoError(err)

	// --- Positive control -------------------------------------------------
	// A query that can only be answered by a sequential scan MUST show up in
	// the counter. If it doesn't, the meter is broken and every assertion
	// below would be meaningless.
	control := readResultsStats(ctx, t, dbSvc)
	_, err = dbSvc.DB().ExecContext(ctx, "SELECT count(*) FROM results")
	r.NoError(err)

	after := waitForStatsActivity(ctx, t, dbSvc, control)
	r.Greater(after.SeqScan, control.SeqScan,
		"positive control: a deliberate full scan must be counted by pg_stat_user_tables")

	// --- The assertion ----------------------------------------------------
	before := readResultsStats(ctx, t, dbSvc)
	tempBefore := readTempBytes(ctx, t, dbSvc)

	const bursts = 5
	for i := 0; i < bursts; i++ {
		resp, listErr := svc.ListChecks(ctx, org.Slug, checks.ListChecksOptions{
			IncludeLastResult:       true,
			IncludeLastStatusChange: true,
			Limit:                   100,
		})
		r.NoError(listErr)
		r.Len(resp.Data, checkCount)
		// Sanity: the embeds really were produced, so the burst exercised
		// the code paths this test is metering.
		r.NotNil(resp.Data[0].LastResult)
		r.NotNil(resp.Data[0].LastStatusChange)
	}

	got := waitForStatsActivity(ctx, t, dbSvc, before)

	r.Equal(before.SeqScan, got.SeqScan,
		"a checks list with last_result + last_status_change must not sequentially scan `results`")
	r.Equal(before.SeqTupRead, got.SeqTupRead,
		"no tuples may be read through a sequential scan of `results`")

	r.Equal(tempBefore, readTempBytes(ctx, t, dbSvc),
		"steady-state dashboard polling must not spill any temp files: nothing in this path sorts")

	tuplesRead := got.IdxTupFetch - before.IdxTupFetch
	r.LessOrEqual(tuplesRead, int64(bursts*tupleBudget),
		"each list must read ~one raw row per check (%d checks × %d rows of history each), "+
			"never the organization's whole raw history", checkCount, rowsPerCheck)
}
