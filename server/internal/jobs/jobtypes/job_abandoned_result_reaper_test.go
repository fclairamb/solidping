package jobtypes_test

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/app/services"
	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/jobs/jobdef"
	"github.com/fclairamb/solidping/server/internal/jobs/jobsvc"
	"github.com/fclairamb/solidping/server/internal/jobs/jobtypes"
	"github.com/fclairamb/solidping/server/internal/notifier"
	"github.com/fclairamb/solidping/server/internal/prommetrics"
)

// newAbandonedReaperTestContext wires an in-memory SQLite DB, a real job
// service, and a services registry so the reaper runner exercises its full
// path (ReapAbandonedResults, metrics, self-reschedule) — same shape as
// newReaperTestContext in job_stuck_job_reaper_test.go.
func newAbandonedReaperTestContext(t *testing.T) (*jobdef.JobContext, db.Service) {
	t.Helper()

	ctx := t.Context()
	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	require.NoError(t, err)
	require.NoError(t, dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	jobSvc := jobsvc.NewService(dbSvc.DB(), dbSvc, notifier.NewLocalEventNotifier(), nil)

	jctx := &jobdef.JobContext{
		Services:  &services.Registry{Jobs: jobSvc},
		DB:        dbSvc.DB(),
		DBService: dbSvc,
		Logger:    slog.Default(),
	}

	return jctx, dbSvc
}

// seedStaleMarker creates an org + check (default 1-minute period) and one
// raw lifecycle-marker row far enough in the past to be past
// models.AbandonedResultThreshold, dropping CreateCheck's own auto-seeded
// marker first so exactly one candidate exists.
func seedStaleMarker(t *testing.T, dbSvc db.Service) *models.Result {
	t.Helper()

	ctx := t.Context()
	r := require.New(t)

	org := models.NewOrganization("abandoned-reaper-org", "Abandoned Reaper Org")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	check := models.NewCheck(org.UID, "abandoned-reaper-check", "http")
	r.NoError(dbSvc.CreateCheck(ctx, check))

	seeded, err := dbSvc.ListResults(ctx, &models.ListResultsFilter{
		OrganizationUID: org.UID, CheckUIDs: []string{check.UID}, Limit: 10,
	})
	r.NoError(err)

	seededUIDs := make([]string, 0, len(seeded.Results))
	for _, row := range seeded.Results {
		seededUIDs = append(seededUIDs, row.UID)
	}

	if len(seededUIDs) > 0 {
		_, delErr := dbSvc.DeleteResults(ctx, org.UID, seededUIDs)
		r.NoError(delErr)
	}

	stale := models.NewResult(org.UID, check.UID, models.ResultStatusCreated, 0)
	stale.PeriodStart = time.Now().Add(-2 * time.Hour)
	r.NoError(dbSvc.CreateResult(ctx, stale))

	return stale
}

// TestAbandonedResultReaperRunReapsAndReschedules verifies the runner reaps a
// stale lifecycle-marker row, bumps the reaped metric, and schedules its next
// run.
func TestAbandonedResultReaperRunReapsAndReschedules(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	jctx, dbSvc := newAbandonedReaperTestContext(t)
	stale := seedStaleMarker(t, dbSvc)

	before := testutil.ToFloat64(prommetrics.ResultsReaped)

	run := &jobtypes.AbandonedResultReaperJobRun{}
	r.NoError(run.Run(t.Context(), jctx))

	var row models.Result
	r.NoError(dbSvc.DB().NewSelect().Model(&row).Where("uid = ?", stale.UID).Scan(t.Context()))
	r.NotNil(row.Status)
	r.Equal(int(models.ResultStatusError), *row.Status)
	r.True(row.Abandoned)

	after := testutil.ToFloat64(prommetrics.ResultsReaped)
	r.InDelta(before+1, after, 0.0001)

	pending, err := countPendingAbandonedReaperJobs(t.Context(), dbSvc)
	r.NoError(err)
	r.Equal(1, pending, "reaper should reschedule exactly one follow-up run")
}

// TestAbandonedResultReaperRunNoServicesIsNoop verifies the runner degrades to
// a no-op (no panic, no error) when no DB service is wired.
func TestAbandonedResultReaperRunNoServicesIsNoop(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	jctx := &jobdef.JobContext{Logger: slog.Default()}
	run := &jobtypes.AbandonedResultReaperJobRun{}
	r.NoError(run.Run(t.Context(), jctx))
}

// TestAbandonedResultReaperRunWithinThresholdNoop verifies a fresh
// lifecycle-marker row is not reaped and the reaped counter does not move.
func TestAbandonedResultReaperRunWithinThresholdNoop(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	jctx, dbSvc := newAbandonedReaperTestContext(t)

	ctx := t.Context()
	org := models.NewOrganization("abandoned-fresh-org", "Abandoned Fresh Org")
	r.NoError(dbSvc.CreateOrganization(ctx, org))
	check := models.NewCheck(org.UID, "abandoned-fresh-check", "http")
	r.NoError(dbSvc.CreateCheck(ctx, check))

	seeded, err := dbSvc.ListResults(ctx, &models.ListResultsFilter{
		OrganizationUID: org.UID, CheckUIDs: []string{check.UID}, Limit: 10,
	})
	r.NoError(err)

	seededUIDs := make([]string, 0, len(seeded.Results))
	for _, row := range seeded.Results {
		seededUIDs = append(seededUIDs, row.UID)
	}

	if len(seededUIDs) > 0 {
		_, delErr := dbSvc.DeleteResults(ctx, org.UID, seededUIDs)
		r.NoError(delErr)
	}

	fresh := models.NewResult(org.UID, check.UID, models.ResultStatusCreated, 0)
	fresh.PeriodStart = time.Now().Add(-time.Minute)
	r.NoError(dbSvc.CreateResult(ctx, fresh))

	run := &jobtypes.AbandonedResultReaperJobRun{}
	r.NoError(run.Run(t.Context(), jctx))

	var row models.Result
	r.NoError(dbSvc.DB().NewSelect().Model(&row).Where("uid = ?", fresh.UID).Scan(t.Context()))
	r.NotNil(row.Status)
	r.Equal(int(models.ResultStatusCreated), *row.Status, "a fresh created row must survive the sweep")
	r.False(row.Abandoned)
}

func countPendingAbandonedReaperJobs(ctx context.Context, dbSvc db.Service) (int, error) {
	return dbSvc.DB().NewSelect().
		Model((*models.Job)(nil)).
		Where("type = ?", string(jobdef.JobTypeAbandonedResultReaper)).
		Where("status = ?", models.JobStatusPending).
		Where("deleted_at IS NULL").
		Count(ctx)
}
