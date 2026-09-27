package backend_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/checkworker/backend"
	"github.com/fclairamb/solidping/server/internal/checkworker/checkjobsvc"
	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/postgres"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/handlers/checkscreenshots"
	"github.com/fclairamb/solidping/server/internal/handlers/incidents"
	"github.com/fclairamb/solidping/server/internal/notifier"
	"github.com/fclairamb/solidping/server/internal/testsupport"
	"github.com/fclairamb/solidping/server/internal/utils/clock"
)

// portCaptureFailure is this file's embedded-Postgres port, distinct from
// every other *_postgres test in the repo.
const portCaptureFailure = 15618

// exerciseCaptureNowFailure is spec 2026-09-27-01 §3 end to end on the
// in-process submission path, run on both engines: a real "Capture now"
// request is recorded, claimed, and answered by a submitted result, and the
// screenshot listing reports what became of it.
//
//  1. A run whose result has no screenshot records a failed outcome for
//     exactly that request's requestedAt, with the checker's reason.
//  2. A later run that DID capture records nothing: the failure shown is still
//     the first request's.
//  3. A result from a lease that carried no request records nothing, even
//     without a screenshot (the same guard as HonorOnDemand).
func exerciseCaptureNowFailure(ctx context.Context, t *testing.T, dbSvc db.Service) {
	t.Helper()

	r := require.New(t)

	events := notifier.NewLocalEventNotifier()
	t.Cleanup(func() { _ = events.Close() })

	checkJobSvc := checkjobsvc.NewService(dbSvc.DB())
	be := backend.NewDirectBackend(dbSvc, checkJobSvc, incidents.NewService(dbSvc, nil, clock.Real{}, nil), events, nil)
	listing := checkscreenshots.NewService(dbSvc, nil, nil, nil)

	org := models.NewOrganization("cf"+uuid.New().String()[:8], "Capture Failure Org")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	workerUID := registerWorker(ctx, t, dbSvc, "cfw-"+uuid.New().String()[:8])

	newCheck := func(slug string) *models.CheckJob {
		check := models.NewCheck(org.UID, slug, "browser")
		check.Config = models.JSONMap{"url": "https://acme.com"}
		r.NoError(dbSvc.CreateCheck(ctx, check))

		jobs, err := dbSvc.ListCheckJobsByCheckUID(ctx, check.UID)
		r.NoError(err)
		r.NotEmpty(jobs)

		return jobs[0]
	}

	// claim runs the real claim, optionally after a real "Capture now".
	claim := func(job *models.CheckJob, requestedAt *time.Time) *models.CheckJob {
		if requestedAt != nil {
			r.NoError(dbSvc.RequestCheckCapture(ctx, job.UID, *requestedAt))
		} else {
			due := time.Now().Add(-time.Second).UTC()
			_, err := dbSvc.DB().NewUpdate().Model((*models.CheckJob)(nil)).
				Set("scheduled_at = ?", due).Set("effective_scheduled_at = ?", due).
				Where("uid = ?", job.UID).Exec(ctx)
			r.NoError(err)
		}

		claimed, err := checkJobSvc.ClaimJobsForCheck(ctx, workerUID, job.Region, job.CheckUID)
		r.NoError(err)
		r.Len(claimed, 1)

		return claimed[0]
	}

	submit := func(job *models.CheckJob, diagnostics *checkerdef.Diagnostics) {
		req := submitReq()
		req.Diagnostics = diagnostics
		r.NoError(be.SubmitResult(ctx, job, workerUID, req))
	}

	outcome := func(checkUID string) *checkscreenshots.CaptureOutcome {
		got, err := listing.ListScreenshots(ctx, org.Slug, checkUID, 5)
		r.NoError(err)

		return got.CaptureOutcome
	}

	// 1. The request's run comes back without a screenshot.
	job := newCheck("failing")
	r.Nil(outcome(job.CheckUID), "control: nothing reported before any request")

	firstRequest := time.Now().Add(-2 * time.Minute).UTC().Truncate(time.Microsecond)
	claimed := claim(job, &firstRequest)
	r.NotNil(claimed.CaptureClaimedAt, "the lease carries the request")

	submit(claimed, &checkerdef.Diagnostics{ScreenshotError: "the capture timed out after 5s"})

	got := outcome(job.CheckUID)
	r.NotNil(got, "a Capture now that produced no screenshot is reported")
	r.True(got.RequestedAt.Equal(firstRequest),
		"matched to the request the API returned: want %s, got %s", firstRequest, got.RequestedAt)
	r.True(got.Failed)
	r.Equal("the capture timed out after 5s", got.Error)

	// 2. The next request's run captures: nothing new is recorded.
	secondRequest := time.Now().Add(-time.Minute).UTC().Truncate(time.Microsecond)
	claimed = claim(job, &secondRequest)
	submit(claimed, &checkerdef.Diagnostics{Screenshot: &checkerdef.Screenshot{
		Format: checkerdef.ImageFormatWebP, Available: true, CaptureID: "cap-ok", OnDemand: true,
	}})

	got = outcome(job.CheckUID)
	r.NotNil(got)
	r.True(got.RequestedAt.Equal(firstRequest), "a successful capture records no failure")

	// 3. No request on the lease: a screenshot-less result records nothing.
	plain := newCheck("plain")
	claimed = claim(plain, nil)
	r.Nil(claimed.CaptureClaimedAt, "control: this lease carries no request")

	submit(claimed, nil)
	r.Nil(outcome(plain.CheckUID), "a run no one asked a capture of reports no capture failure")
}

// TestCaptureNowFailureIsRecorded runs it on SQLite.
func TestCaptureNowFailureIsRecorded(t *testing.T) {
	t.Parallel()

	ctx := t.Context()

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	require.NoError(t, err)
	require.NoError(t, dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	exerciseCaptureNowFailure(ctx, t, dbSvc)
}

// TestCaptureNowFailureIsRecorded_Postgres runs it on Postgres, where the
// requested-at round-trips through a timestamptz column.
func TestCaptureNowFailureIsRecorded_Postgres(t *testing.T) {
	t.Parallel()

	if testing.Short() {
		t.Skip("skipping embedded-postgres test in -short mode")
	}

	ctx := t.Context()

	dbSvc, err := postgres.New(ctx, &postgres.Config{Embedded: true, Port: portCaptureFailure, RunMode: "test"})
	if err != nil {
		testsupport.PostgresUnavailable(t, err)
	}

	t.Cleanup(func() { _ = dbSvc.Close() })

	if initErr := dbSvc.Initialize(ctx); initErr != nil {
		testsupport.PostgresInitFailed(t, initErr)
	}

	exerciseCaptureNowFailure(ctx, t, dbSvc)
}
