package sqlite

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

// TestReapAbandonedResults_FinalizesStaleMarkerOnly seeds a check with its
// default 1-minute period (threshold = 5 * (1min + 30s) = 7.5 min, see
// models.AbandonedResultThreshold) and three raw rows: a stale created marker
// well past the threshold, a fresh created marker comfortably inside it, and
// a stale RUNNING row that is never a candidate at all — heartbeat checks use
// "running" as a legitimate long-lived status the reaper must never touch
// regardless of age (spec 2026-08-18-03, narrowed from created+running to
// created-only after this exact scenario broke handlers/heartbeat's tests).
// Only the stale created row may be reaped.
func TestReapAbandonedResults_FinalizesStaleMarkerOnly(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := t.Context()

	s, err := New(ctx, Config{InMemory: true})
	r.NoError(err)
	r.NoError(s.Initialize(ctx))
	t.Cleanup(func() { _ = s.Close() })

	org := models.NewOrganization("reap-org", "Reap Org")
	r.NoError(s.CreateOrganization(ctx, org))

	check := models.NewCheck(org.UID, "reap-check", "http") // default period: 1 minute
	r.NoError(s.CreateCheck(ctx, check))

	// CreateCheck already wrote one "created" marker at time.Now() — old
	// enough to be borderline depending on test speed, so drop it and seed
	// deterministic fixtures instead.
	seeded, err := s.ListResults(ctx, &models.ListResultsFilter{
		OrganizationUID: org.UID, CheckUIDs: []string{check.UID}, Limit: 10,
	})
	r.NoError(err)

	seededUIDs := make([]string, 0, len(seeded.Results))
	for _, row := range seeded.Results {
		seededUIDs = append(seededUIDs, row.UID)
	}

	if len(seededUIDs) > 0 {
		_, delErr := s.DeleteResults(ctx, org.UID, seededUIDs)
		r.NoError(delErr)
	}

	stale := models.NewResult(org.UID, check.UID, models.ResultStatusCreated, 0)
	stale.PeriodStart = time.Now().Add(-2 * time.Hour) // far past the ~7.5 min threshold
	r.NoError(s.CreateResult(ctx, stale))

	fresh := models.NewResult(org.UID, check.UID, models.ResultStatusCreated, 0)
	fresh.PeriodStart = time.Now().Add(-time.Minute) // inside the ~7.5 min threshold
	r.NoError(s.CreateResult(ctx, fresh))

	staleRunning := models.NewResult(org.UID, check.UID, models.ResultStatusRunning, 0)
	staleRunning.PeriodStart = time.Now().Add(-48 * time.Hour) // far past threshold, but never a candidate
	r.NoError(s.CreateResult(ctx, staleRunning))

	outcome, err := s.ReapAbandonedResults(ctx)
	r.NoError(err)
	r.Equal(2, outcome.Candidates, "only the two created rows are candidates; running is never scanned")
	r.Equal(1, outcome.Reaped, "only the stale created row is past threshold")

	var reapedRow models.Result
	r.NoError(s.DB().NewSelect().Model(&reapedRow).Where("uid = ?", stale.UID).Scan(ctx))
	r.NotNil(reapedRow.Status)
	r.Equal(int(models.ResultStatusAbandoned), *reapedRow.Status,
		"reaped row becomes the dedicated terminal abandoned status, not a fake error")
	r.Contains(reapedRow.Output, "error", "output names what happened")
	r.Contains(reapedRow.Output, "reason")
	r.Equal("abandoned", reapedRow.Output["reason"])

	var freshRow models.Result
	r.NoError(s.DB().NewSelect().Model(&freshRow).Where("uid = ?", fresh.UID).Scan(ctx))
	r.NotNil(freshRow.Status)
	r.Equal(int(models.ResultStatusCreated), *freshRow.Status, "fresh row must survive the sweep untouched")

	var runningRow models.Result
	r.NoError(s.DB().NewSelect().Model(&runningRow).Where("uid = ?", staleRunning.UID).Scan(ctx))
	r.NotNil(runningRow.Status)
	r.Equal(int(models.ResultStatusRunning), *runningRow.Status,
		"a running row must never be touched by the reaper, no matter how old")

	// A second sweep must be a no-op: the stale row is no longer a
	// created-marker candidate (its status is now error), so it is not
	// re-processed, and nothing else has become newly eligible.
	outcome2, err := s.ReapAbandonedResults(ctx)
	r.NoError(err)
	r.Equal(1, outcome2.Candidates, "only the still-fresh created row remains a candidate")
	r.Equal(0, outcome2.Reaped)
}

// TestReapAbandonedResults_SkipsOrphanedCheck seeds a lifecycle-marker raw row
// then hard-removes its check underneath it — `results.check_uid` normally
// has an ON DELETE CASCADE foreign key, so this bypasses PRAGMA foreign_keys
// specifically to simulate the legacy-data edge case that guard exists for
// (e.g. a database that predates the constraint) — and confirms the sweep
// leaves the now check-less row alone rather than erroring or panicking on
// the missing period.
func TestReapAbandonedResults_SkipsOrphanedCheck(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := t.Context()

	s, err := New(ctx, Config{InMemory: true})
	r.NoError(err)
	r.NoError(s.Initialize(ctx))
	t.Cleanup(func() { _ = s.Close() })

	org := models.NewOrganization("reap-orphan-org", "Reap Orphan Org")
	r.NoError(s.CreateOrganization(ctx, org))

	check := models.NewCheck(org.UID, "orphan-check", "http")
	r.NoError(s.CreateCheck(ctx, check))

	// Drop CreateCheck's own auto-seeded marker so exactly one candidate row
	// exists below.
	seeded, err := s.ListResults(ctx, &models.ListResultsFilter{
		OrganizationUID: org.UID, CheckUIDs: []string{check.UID}, Limit: 10,
	})
	r.NoError(err)

	seededUIDs := make([]string, 0, len(seeded.Results))
	for _, row := range seeded.Results {
		seededUIDs = append(seededUIDs, row.UID)
	}

	if len(seededUIDs) > 0 {
		_, delErr := s.DeleteResults(ctx, org.UID, seededUIDs)
		r.NoError(delErr)
	}

	orphan := models.NewResult(org.UID, check.UID, models.ResultStatusCreated, 0)
	orphan.PeriodStart = time.Now().Add(-24 * time.Hour)
	r.NoError(s.CreateResult(ctx, orphan))

	_, err = s.DB().ExecContext(ctx, "PRAGMA foreign_keys = OFF")
	r.NoError(err)
	_, err = s.DB().NewDelete().Model((*models.Check)(nil)).Where("uid = ?", check.UID).Exec(ctx)
	r.NoError(err)
	_, err = s.DB().ExecContext(ctx, "PRAGMA foreign_keys = ON")
	r.NoError(err)

	outcome, err := s.ReapAbandonedResults(ctx)
	r.NoError(err)
	r.Equal(1, outcome.Candidates)
	r.Equal(0, outcome.Reaped, "a row whose check no longer exists must be left alone")

	var row models.Result
	r.NoError(s.DB().NewSelect().Model(&row).Where("uid = ?", orphan.UID).Scan(ctx))
	r.NotNil(row.Status)
	r.Equal(int(models.ResultStatusCreated), *row.Status, "orphaned row must be untouched")
}
