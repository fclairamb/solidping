package checkjobsvc_test

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"github.com/uptrace/bun"

	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/postgres"
	"github.com/fclairamb/solidping/server/internal/testsupport"
)

// portRunRequest is this file's embedded-Postgres port.
const portRunRequest = 15619

// exerciseRequestCheckRun pins RequestCheckRun (spec 2026-10-04-01) on both
// engines: both schedule columns move to requestedAt, the capture flag stays
// NULL, a leased job or one carrying a multi-step run is refused with
// db.ErrCheckJobBusy and left untouched, and a missing job is sql.ErrNoRows.
func exerciseRequestCheckRun(ctx context.Context, t *testing.T, dbSvc db.Service, bunDB *bun.DB) {
	t.Helper()

	r := require.New(t)

	org := models.NewOrganization("rr"+uuid.New().String()[:8], "Run Request Org")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	check := models.NewCheck(org.UID, "rr-"+uuid.New().String()[:8], "http")
	r.NoError(dbSvc.CreateCheck(ctx, check))

	jobs, err := dbSvc.ListCheckJobsByCheckUID(ctx, check.UID)
	r.NoError(err)
	r.NotEmpty(jobs)

	job := jobs[0]

	far := time.Now().Add(time.Hour).UTC()
	_, err = bunDB.NewUpdate().Model((*models.CheckJob)(nil)).
		Set("scheduled_at = ?", far).Set("effective_scheduled_at = ?", far).
		Where("uid = ?", job.UID).Exec(ctx)
	r.NoError(err)

	requestedAt := time.Now().UTC().Truncate(time.Millisecond)
	r.NoError(dbSvc.RequestCheckRun(ctx, job.UID, requestedAt))

	stored := loadJob(ctx, t, bunDB, job.UID)
	r.WithinDuration(requestedAt, *stored.ScheduledAt, time.Millisecond)
	r.WithinDuration(requestedAt, *stored.EffectiveScheduledAt, time.Millisecond)
	r.Nil(stored.CaptureRequestedAt, "run-now never sets the capture flag")

	// Leased: refused, untouched.
	_, err = bunDB.NewUpdate().Model((*models.CheckJob)(nil)).
		Set("scheduled_at = ?", far).
		Set("lease_expires_at = ?", time.Now().Add(time.Hour).UTC()).
		Where("uid = ?", job.UID).Exec(ctx)
	r.NoError(err)
	r.ErrorIs(dbSvc.RequestCheckRun(ctx, job.UID, time.Now().UTC()), db.ErrCheckJobBusy)
	r.WithinDuration(far, *loadJob(ctx, t, bunDB, job.UID).ScheduledAt, time.Millisecond)

	// Multi-step run between slices (lease expired): refused too.
	_, err = bunDB.NewUpdate().Model((*models.CheckJob)(nil)).
		Set("lease_expires_at = ?", time.Now().Add(-time.Minute).UTC()).
		Set("step_run_uid = ?", uuid.New().String()).
		Where("uid = ?", job.UID).Exec(ctx)
	r.NoError(err)
	r.ErrorIs(dbSvc.RequestCheckRun(ctx, job.UID, time.Now().UTC()), db.ErrCheckJobBusy)
	r.WithinDuration(far, *loadJob(ctx, t, bunDB, job.UID).ScheduledAt, time.Millisecond)

	r.ErrorIs(dbSvc.RequestCheckRun(ctx, uuid.New().String(), time.Now().UTC()), sql.ErrNoRows)
}

// TestRequestCheckRun runs the contract on SQLite.
func TestRequestCheckRun(t *testing.T) {
	t.Parallel()

	dbSvc, ctx := setupTestDB(t)
	t.Cleanup(func() { _ = dbSvc.Close() })

	exerciseRequestCheckRun(ctx, t, dbSvc, dbSvc.DB())
}

// TestRequestCheckRun_Postgres runs the same contract on Postgres.
func TestRequestCheckRun_Postgres(t *testing.T) {
	t.Parallel()

	if testing.Short() {
		t.Skip("skipping embedded-postgres test in -short mode")
	}

	ctx := t.Context()

	dbSvc, err := postgres.New(ctx, &postgres.Config{Embedded: true, Port: portRunRequest, RunMode: "test"})
	if err != nil {
		testsupport.PostgresUnavailable(t, err)
	}

	t.Cleanup(func() { _ = dbSvc.Close() })

	if initErr := dbSvc.Initialize(ctx); initErr != nil {
		testsupport.PostgresInitFailed(t, initErr)
	}

	exerciseRequestCheckRun(ctx, t, dbSvc, dbSvc.DB())
}
