package sqlite

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// aggregationHardeningMigrationSQL is section (1) of the consolidated
// 006_v0_5_0 migration — the results-table orphan purge + NULL-region dedupe +
// unique-index rebuild. Migration 006 is no longer just this block (it also
// carries the deported-agents and org-default-escalation-policy DDL from the
// same release cycle, per wiki/conventions/database.md "one migration per
// release"), so tests that re-run this specific idempotent block after
// Initialize() has already applied the full file inline it here rather than
// reading the file — reading the whole file a second time would re-run
// non-idempotent CREATE TABLE / ADD COLUMN statements from the other two
// sections and fail. Keep this in sync with migrations/006_v0_5_0.up.sql §1.
const aggregationHardeningMigrationSQL = `
delete from results
where check_uid not in (select uid from checks);

delete from results
where uid in (
  select uid from (
    select uid,
      row_number() over (
        partition by organization_uid, check_uid, coalesce(region, ''), period_type, period_start
        order by coalesce(total_checks, -1) desc, created_at asc, uid asc
      ) as rn
    from results
    where period_type != 'raw'
  ) ranked
  where rn > 1
);

drop index if exists results_aggregated_unique_idx;

create unique index results_aggregated_unique_idx
  on results (organization_uid, check_uid, coalesce(region, ''), period_type, period_start)
  where period_type != 'raw';
`

// TestMigrationDedupesAggregatedResults verifies the v0.5.0 migration (spec
// 2026-07-11-16 §3): duplicate NULL-region aggregated rows for the same bucket
// are collapsed to a single survivor (highest total_checks, tie-break earliest
// created_at) and the NULL-proof unique index is then created successfully —
// which is only possible if the dedupe left no duplicates.
func TestMigrationDedupesAggregatedResults(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	r := require.New(t)

	svc, err := New(ctx, Config{InMemory: true})
	r.NoError(err)
	t.Cleanup(func() { _ = svc.Close() })
	r.NoError(svc.Initialize(ctx))

	// Simulate the pre-006 state: drop the NULL-proof unique index so the
	// NULL-region duplicates the old region-based index allowed can be seeded.
	_, err = svc.db.ExecContext(ctx, "DROP INDEX results_aggregated_unique_idx")
	r.NoError(err)

	orgUID := uuid.New().String()
	checkUID := uuid.New().String()
	_, err = svc.db.NewRaw(
		"INSERT INTO organizations (uid, slug, name) VALUES (?, ?, ?)",
		orgUID, "dedupe-org", "Dedupe Org",
	).Exec(ctx)
	r.NoError(err)
	_, err = svc.db.NewRaw(
		"INSERT INTO checks (uid, organization_uid, slug, type, period) VALUES (?, ?, ?, ?, ?)",
		checkUID, orgUID, "dedupe-check", "http", "1m0s",
	).Exec(ctx)
	r.NoError(err)

	pollutedStart := time.Date(2026, 7, 10, 14, 0, 0, 0, time.UTC).Format("2006-01-02 15:04:05")

	type seedRow struct {
		uid         string
		totalChecks any
		createdAt   string
	}
	// Five duplicates of one NULL-region bucket. The survivor must be the
	// highest total_checks (7); the 7-vs-7 tie breaks to the earliest created_at.
	seeds := []seedRow{
		{uuid.New().String(), 0, "2026-07-10 15:00:00"},
		{uuid.New().String(), 7, "2026-07-10 15:05:00"}, // survivor
		{uuid.New().String(), 7, "2026-07-10 15:10:00"}, // same count, later → dropped
		{uuid.New().String(), 3, "2026-07-10 15:02:00"},
		{uuid.New().String(), nil, "2026-07-10 15:01:00"}, // NULL count ranks lowest
	}
	survivor := seeds[1].uid

	for _, seed := range seeds {
		_, execErr := svc.db.NewRaw(
			"INSERT INTO results "+
				"(uid, organization_uid, check_uid, period_type, period_start, region, total_checks, created_at) "+
				"VALUES (?, ?, ?, 'hour', ?, NULL, ?, ?)",
			seed.uid, orgUID, checkUID, pollutedStart, seed.totalChecks, seed.createdAt,
		).Exec(ctx)
		r.NoError(execErr)
	}

	// A distinct bucket (different period_start) that must be left untouched.
	otherUID := uuid.New().String()
	otherStart := time.Date(2026, 7, 10, 15, 0, 0, 0, time.UTC).Format("2006-01-02 15:04:05")
	_, err = svc.db.NewRaw(
		"INSERT INTO results "+
			"(uid, organization_uid, check_uid, period_type, period_start, region, total_checks, created_at) "+
			"VALUES (?, ?, ?, 'hour', ?, NULL, 4, '2026-07-10 16:00:00')",
		otherUID, orgUID, checkUID, otherStart,
	).Exec(ctx)
	r.NoError(err)

	// Run the real 006 up migration's aggregation-hardening block: dedupe, then
	// rebuild the NULL-proof index. Index creation fails if any duplicate
	// survives — so a NoError here is itself proof the dedupe worked.
	_, err = svc.db.ExecContext(ctx, aggregationHardeningMigrationSQL)
	r.NoError(err, "dedupe + NULL-proof index creation must succeed")

	type resultRow struct {
		UID         string `bun:"uid"`
		TotalChecks *int   `bun:"total_checks"`
	}
	var pollutedRows []resultRow
	r.NoError(svc.db.NewRaw(
		"SELECT uid, total_checks FROM results WHERE period_start = ?", pollutedStart,
	).Scan(ctx, &pollutedRows))
	r.Len(pollutedRows, 1, "the polluted bucket must collapse to a single survivor")
	r.Equal(survivor, pollutedRows[0].UID,
		"survivor must be the highest total_checks / earliest created_at row")
	r.NotNil(pollutedRows[0].TotalChecks)
	r.Equal(7, *pollutedRows[0].TotalChecks)

	var otherCount int
	r.NoError(svc.db.NewRaw(
		"SELECT count(*) FROM results WHERE period_start = ?", otherStart,
	).Scan(ctx, &otherCount))
	r.Equal(1, otherCount, "the untouched bucket must be preserved")
}
