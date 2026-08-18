package postgres

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

// TestReapAbandonedResults_Postgres is the Postgres twin of the SQLite reaper
// tests (sync-pg-to-sqlite convention): a created-marker raw row well past its
// check's threshold is finalized to a terminal error with Abandoned=true, a
// fresh created row is left alone, a stale RUNNING row is never touched at
// all (heartbeat checks use "running" as a legitimate long-lived status), and
// a re-run does not double-reap.
//
//nolint:paralleltest // shares dev-machine resources (embedded-postgres-go's pwfile extraction) with its siblings
func TestReapAbandonedResults_Postgres(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping embedded-postgres test in -short mode")
	}

	ctx := t.Context()
	r := require.New(t)

	s, err := New(ctx, &Config{
		Embedded: true,
		Port:     portLastResult + 4,
		RunMode:  runModeTest,
	})
	if err != nil {
		t.Skipf("embedded postgres unavailable: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	if initErr := s.Initialize(ctx); initErr != nil {
		t.Skipf("embedded postgres init failed: %v", initErr)
	}

	org := models.NewOrganization("reap-pg-org", "Reap PG Org")
	r.NoError(s.CreateOrganization(ctx, org))

	check := models.NewCheck(org.UID, "reap-check", "http") // default period: 1 minute
	r.NoError(s.CreateCheck(ctx, check))

	// Drop CreateCheck's own auto-seeded marker for a deterministic candidate set.
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
	stale.PeriodStart = time.Now().Add(-2 * time.Hour)
	r.NoError(s.CreateResult(ctx, stale))

	fresh := models.NewResult(org.UID, check.UID, models.ResultStatusCreated, 0)
	fresh.PeriodStart = time.Now().Add(-time.Minute)
	r.NoError(s.CreateResult(ctx, fresh))

	staleRunning := models.NewResult(org.UID, check.UID, models.ResultStatusRunning, 0)
	staleRunning.PeriodStart = time.Now().Add(-48 * time.Hour)
	r.NoError(s.CreateResult(ctx, staleRunning))

	outcome, err := s.ReapAbandonedResults(ctx)
	r.NoError(err)
	r.Equal(2, outcome.Candidates, "only the two created rows are candidates; running is never scanned")
	r.Equal(1, outcome.Reaped, "only the stale created row is past threshold")

	var reapedRow models.Result
	r.NoError(s.db.NewSelect().Model(&reapedRow).Where("uid = ?", stale.UID).Scan(ctx))
	r.NotNil(reapedRow.Status)
	r.Equal(int(models.ResultStatusError), *reapedRow.Status)
	r.True(reapedRow.Abandoned)
	r.Equal("abandoned", reapedRow.Output["reason"])

	var freshRow models.Result
	r.NoError(s.db.NewSelect().Model(&freshRow).Where("uid = ?", fresh.UID).Scan(ctx))
	r.NotNil(freshRow.Status)
	r.Equal(int(models.ResultStatusCreated), *freshRow.Status)
	r.False(freshRow.Abandoned)

	var runningRow models.Result
	r.NoError(s.db.NewSelect().Model(&runningRow).Where("uid = ?", staleRunning.UID).Scan(ctx))
	r.NotNil(runningRow.Status)
	r.Equal(int(models.ResultStatusRunning), *runningRow.Status,
		"a running row must never be touched by the reaper, no matter how old")
	r.False(runningRow.Abandoned)

	// Re-run must not double-reap: the stale row is no longer a
	// created-marker candidate.
	outcome2, err := s.ReapAbandonedResults(ctx)
	r.NoError(err)
	r.Equal(1, outcome2.Candidates)
	r.Equal(0, outcome2.Reaped)
}
