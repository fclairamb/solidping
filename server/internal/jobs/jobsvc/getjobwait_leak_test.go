package jobsvc_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/jobs/jobsvc"
	"github.com/fclairamb/solidping/server/internal/notifier"
)

// TestGetJobWaitDoesNotLeakListeners is the end-to-end regression test for the
// 2026-07-11 realtime-silence incident: GetJobWait subscribes to the
// "job.created" event on every call, and job runners loop over it, so without a
// matching Unlisten one listener channel leaked per processed job — eventually
// wedging the notifier's forward loop. Here we seed claimable jobs, drain them
// via repeated GetJobWait calls, and assert the notifier's ListenerCount returns
// to zero after every claim instead of growing with the number of jobs.
func TestGetJobWaitDoesNotLeakListeners(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := t.Context()

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	// Keep our own handle on the notifier so we can read its ListenerCount.
	bus := notifier.NewLocalEventNotifier()
	t.Cleanup(func() { _ = bus.Close() })

	svc := jobsvc.NewService(dbSvc.DB(), dbSvc, bus, nil)

	r.Equal(0, bus.ListenerCount(), "baseline should be zero listeners")

	// Seed a batch of immediately-claimable pending jobs.
	const jobCount = 50
	for i := 0; i < jobCount; i++ {
		job := models.NewJob(nil, "email")
		job.Status = models.JobStatusPending
		job.ScheduledAt = time.Now().Add(-time.Minute) // in the past -> claimable now
		_, insErr := dbSvc.DB().NewInsert().Model(job).Exec(ctx)
		r.NoError(insErr)
	}

	// Drain the batch one GetJobWait call at a time. Each call subscribes and
	// must deregister on return, so ListenerCount is back to zero between calls.
	for i := 0; i < jobCount; i++ {
		job, waitErr := svc.GetJobWait(ctx)
		r.NoError(waitErr)
		r.NotNil(job)
		r.Equal(0, bus.ListenerCount(),
			"GetJobWait must Unlisten on return; leaked %d listeners after %d claims",
			bus.ListenerCount(), i+1)
	}

	r.Equal(0, bus.ListenerCount(), "no listeners must leak across GetJobWait calls")
}
