package jobsvc_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/postgres"
	"github.com/fclairamb/solidping/server/internal/jobs/jobsvc"
	"github.com/fclairamb/solidping/server/internal/notifier"
	"github.com/fclairamb/solidping/server/internal/testsupport"
)

// TestGetJobWaitClaimsFutureJobNearDueTime_Postgres is the Postgres half of
// spec 2026-09-25-07 Tests item 1 (TestGetJobWaitClaimsFutureJobNearDueTime in
// getjobwait_wake_test.go is the SQLite half): proves the nextPendingWait
// fix — and, on this dialect specifically, that idx_jobs_queue is actually
// usable by the ORDER BY scheduled_at ASC LIMIT 1 lookup — against a real
// embedded PostgreSQL.
//
// Self-skips under `-short` (the default `make test` mode) and on any
// embedded-startup error, mirroring
// internal/checkworker/checkjobsvc/service_postgres_test.go. Uses port 15446,
// distinct from the other embedded-postgres ports already in use across the
// suite (15432, 15434, 15437, 15438, 15439, 15445, 15517).
func TestGetJobWaitClaimsFutureJobNearDueTime_Postgres(t *testing.T) {
	t.Parallel()

	if testing.Short() {
		t.Skip("skipping embedded-postgres test in -short mode")
	}

	ctx := t.Context()
	r := require.New(t)

	dbSvc, err := postgres.New(ctx, &postgres.Config{
		Embedded: true,
		Port:     15446,
		RunMode:  "test",
	})
	if err != nil {
		testsupport.PostgresUnavailable(t, err)
	}

	t.Cleanup(func() { _ = dbSvc.Close() })

	if initErr := dbSvc.Initialize(ctx); initErr != nil {
		testsupport.PostgresInitFailed(t, initErr)
	}

	bus := notifier.NewLocalEventNotifier()
	t.Cleanup(func() { _ = bus.Close() })

	svc := jobsvc.NewService(dbSvc.DB(), dbSvc, bus, nil)

	dueAt := time.Now().Add(2 * time.Second)
	job := models.NewJob(nil, "email")
	job.Status = models.JobStatusPending
	job.ScheduledAt = dueAt
	_, err = dbSvc.DB().NewInsert().Model(job).Exec(ctx)
	r.NoError(err)

	waitCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	claimed, err := svc.GetJobWait(waitCtx)
	r.NoError(err)
	r.NotNil(claimed)
	r.Equal(job.UID, claimed.UID)

	claimLatency := time.Since(dueAt)
	r.Lessf(claimLatency, 500*time.Millisecond,
		"job claimed %s after its scheduled_at, want < 500ms", claimLatency)
}
