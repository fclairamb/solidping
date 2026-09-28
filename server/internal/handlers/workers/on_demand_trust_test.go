package workers_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/checkworker/checkjobsvc"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/handlers/attachments"
	"github.com/fclairamb/solidping/server/internal/handlers/incidents"
	"github.com/fclairamb/solidping/server/internal/handlers/workers"
	"github.com/fclairamb/solidping/server/internal/jobs/jobsvc"
	"github.com/fclairamb/solidping/server/internal/notifier"
	"github.com/fclairamb/solidping/server/internal/utils/clock"
)

// recordingUploads records the upload requests the incident pipeline emits.
type recordingUploads struct {
	mu     sync.Mutex
	topics []string
}

func (r *recordingUploads) RequestScreenshotUpload(_ context.Context, _, _, topic string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.topics = append(r.topics, topic)
}

func (r *recordingUploads) snapshot() []string {
	r.mu.Lock()
	defer r.mu.Unlock()

	return append([]string(nil), r.topics...)
}

// TestAgentOnDemandMarkerNeedsARealRequest pins spec 2026-09-25-34's trust
// rule on the agent submission path: an agent's `onDemand` marker on a HEALTHY
// run is honored only when the job's lease carried a "Capture now" request
// (capture_claimed_at). Without one the capture is not asked for — a healthy
// run never becomes a stored "capture-now". The second submission, on a job
// whose lease did carry a request, is the positive control.
func TestAgentOnDemandMarkerNeedsARealRequest(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := t.Context()
	e := newSubmitEnv(t)

	events := notifier.NewLocalEventNotifier()
	t.Cleanup(func() { _ = events.Close() })

	uploads := &recordingUploads{}
	incidentSvc := incidents.NewService(e.dbSvc, jobsvc.NewService(e.dbSvc.DB(), e.dbSvc, events, nil), clock.Real{}, nil)
	incidentSvc.SetAgentUploadRequester(uploads)
	svc := workers.NewService(e.dbSvc, checkjobsvc.NewService(e.dbSvc.DB()), incidentSvc, submitParams())

	// A worker with a real uid: the upload request is addressed to it, and the
	// shared fixture's worker has none.
	worker := models.NewWorker("ag-trust", "agent:trust")
	_, err := e.dbSvc.DB().NewInsert().Model(worker).Exec(ctx)
	r.NoError(err)

	lease := func(jobUID string, claimed bool) {
		update := e.dbSvc.DB().NewUpdate().Model((*models.CheckJob)(nil)).
			Set("lease_worker_uid = ?", worker.UID).
			Set("lease_expires_at = ?", time.Now().Add(time.Minute)).
			Where("uid = ?", jobUID)
		if claimed {
			update = update.Set("capture_claimed_at = ?", time.Now())
		}

		_, leaseErr := update.Exec(ctx)
		r.NoError(leaseErr)
	}

	submitHealthyOnDemand := func(jobUID string) {
		_, err := svc.SubmitResult(ctx, &workers.SubmitResultRequest{
			JobUID:    jobUID,
			WorkerUID: worker.UID,
			Status:    int(models.ResultStatusUp),
			Duration:  800,
			Diagnostics: &checkerdef.Diagnostics{Screenshot: &checkerdef.Screenshot{
				Available: true, CaptureID: "cap-1", OnDemand: true,
			}},
		})
		r.NoError(err)
	}

	unrequested := e.leasedJob(100, 0, 0)
	lease(unrequested.UID, false)
	submitHealthyOnDemand(unrequested.UID)
	r.Empty(uploads.snapshot(), "an onDemand claim on a job with no request is not honored")

	// The same job, leased again — this time by a claim that carried a
	// "Capture now" request.
	requested := unrequested
	lease(requested.UID, true)

	submitHealthyOnDemand(requested.UID)
	r.Equal([]string{attachments.CheckScreenshotTopic(requested.CheckUID)}, uploads.snapshot(),
		"control: the lease carried a request, so the capture is asked for under the check")

	r.Nil(e.reload(requested.UID).CaptureClaimedAt, "the release spends the request")
}

// TestAgentCaptureNowFailureIsRecorded is spec 2026-09-27-01 §3 on the agent
// submission path: a result frame without a screenshot, from a lease that
// carried a "Capture now" request, records the failure with the agent's own
// reason. A frame WITH a capture marker records nothing, and neither does a
// screenshot-less frame from a lease that carried no request.
func TestAgentCaptureNowFailureIsRecorded(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := t.Context()
	e := newSubmitEnv(t)

	lease := func(jobUID string, claimedAt *time.Time) {
		update := e.dbSvc.DB().NewUpdate().Model((*models.CheckJob)(nil)).
			Set("lease_worker_uid = ?", e.workerUID).
			Set("lease_expires_at = ?", time.Now().Add(time.Minute)).
			Set("capture_claimed_at = ?", claimedAt).
			Where("uid = ?", jobUID)

		_, leaseErr := update.Exec(ctx)
		r.NoError(leaseErr)
	}

	submit := func(jobUID string, diagnostics *checkerdef.Diagnostics) {
		_, err := e.svc.SubmitResult(ctx, &workers.SubmitResultRequest{
			JobUID: jobUID, WorkerUID: e.workerUID, Status: int(models.ResultStatusUp), Duration: 800,
			Diagnostics: diagnostics,
		})
		r.NoError(err)
	}

	job := e.leasedJob(100, 0, 0)

	// No request on the lease: nothing recorded, screenshot or not.
	lease(job.UID, nil)
	submit(job.UID, &checkerdef.Diagnostics{ScreenshotError: "renderer gone"})
	r.Nil(e.reload(job.UID).CaptureFailedRequestAt, "a run no one asked a capture of records nothing")

	// The request's run comes back with a capture marker: nothing recorded.
	requested := time.Now().Add(-time.Minute).UTC().Truncate(time.Millisecond)
	lease(job.UID, &requested)
	submit(job.UID, &checkerdef.Diagnostics{Screenshot: &checkerdef.Screenshot{
		Available: true, CaptureID: "cap-1", OnDemand: true,
	}})
	r.Nil(e.reload(job.UID).CaptureFailedRequestAt, "a capture that arrived is not a failure")

	// The request's run comes back without one: the failure and the agent's
	// reason are recorded against that request.
	lease(job.UID, &requested)
	submit(job.UID, &checkerdef.Diagnostics{ScreenshotError: "the agent could not keep the image for upload"})

	stored := e.reload(job.UID)
	r.NotNil(stored.CaptureFailedRequestAt)
	r.True(stored.CaptureFailedRequestAt.Equal(requested), "got %s", stored.CaptureFailedRequestAt)
	r.NotNil(stored.CaptureFailureReason)
	r.Equal("the agent could not keep the image for upload", *stored.CaptureFailureReason)
	r.Nil(stored.CaptureClaimedAt, "the release still spends the request")
}
