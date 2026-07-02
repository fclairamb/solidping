package jobsvc_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/jobs/jobsvc"
	"github.com/fclairamb/solidping/server/internal/notifier"
	"github.com/fclairamb/solidping/server/internal/realtime"
)

// jobsHintCounter counts `jobs` hints per org seen on the bus.
type jobsHintCounter struct {
	mu     sync.Mutex
	counts map[string]int
}

func countJobsHints(t *testing.T, bus notifier.EventNotifier) *jobsHintCounter {
	t.Helper()

	counter := &jobsHintCounter{counts: make(map[string]int)}
	ch := bus.Listen(realtime.ChannelOrgEvents)

	go func() {
		for payload := range ch {
			hint, err := realtime.DecodeHint(payload)
			if err != nil {
				continue
			}
			for _, k := range hint.Kinds {
				if k == string(realtime.KindJobs) {
					counter.mu.Lock()
					counter.counts[hint.Org]++
					counter.mu.Unlock()
				}
			}
		}
	}()

	return counter
}

func (c *jobsHintCounter) count(orgUID string) int {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.counts[orgUID]
}

// TestJobLifecycle_PublishesJobsHints walks a job through create -> running ->
// success and asserts each transition emits a coalesced org-scoped `jobs`
// hint, while org-less system jobs stay silent.
func TestJobLifecycle_PublishesJobsHints(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := t.Context()

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	org := models.NewOrganization("jobs-hints", "Jobs Hints")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	bus := notifier.NewLocalEventNotifier()
	counter := countJobsHints(t, bus)
	pub := realtime.NewPublisher(t.Context(), bus, 20*time.Millisecond, nil)
	t.Cleanup(func() {
		pub.Close()
		_ = bus.Close()
	})

	svc := jobsvc.NewService(dbSvc.DB(), dbSvc, notifier.NewLocalEventNotifier(), pub)

	// Create: one hint for the org.
	job, err := svc.CreateJob(ctx, org.UID, "test_job", nil, nil)
	r.NoError(err)
	r.Eventually(func() bool { return counter.count(org.UID) >= 1 },
		2*time.Second, 5*time.Millisecond, "job creation must hint `jobs`")

	// Status write: another hint (allowing for coalescing, count only grows).
	before := counter.count(org.UID)
	r.NoError(svc.UpdateJobStatus(context.Background(), job, models.JobStatusRunning, nil))
	r.NoError(svc.UpdateJobStatus(context.Background(), job, models.JobStatusSuccess, nil))
	r.Eventually(func() bool { return counter.count(org.UID) > before },
		2*time.Second, 5*time.Millisecond, "job status transitions must hint `jobs`")

	// System job (no org): silent.
	sysBefore := counter.count("")
	_, err = svc.CreateJob(ctx, "", "system_job", nil, nil)
	r.NoError(err)
	time.Sleep(100 * time.Millisecond)
	r.Equal(sysBefore, counter.count(""), "org-less system jobs must not hint")
}

// TestCancelJob_PublishesJobsHint pins the cancel path: the org is looked up
// from the canceled row so watching dashboards drop it from the pending list.
func TestCancelJob_PublishesJobsHint(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := t.Context()

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	org := models.NewOrganization("jobs-cancel", "Jobs Cancel")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	bus := notifier.NewLocalEventNotifier()
	counter := countJobsHints(t, bus)
	pub := realtime.NewPublisher(t.Context(), bus, 20*time.Millisecond, nil)
	t.Cleanup(func() {
		pub.Close()
		_ = bus.Close()
	})

	svc := jobsvc.NewService(dbSvc.DB(), dbSvc, notifier.NewLocalEventNotifier(), pub)

	scheduledAt := time.Now().Add(time.Hour) // keep it pending
	job, err := svc.CreateJob(ctx, org.UID, "cancel_me", nil, &jobsvc.JobOptions{ScheduledAt: &scheduledAt})
	r.NoError(err)

	r.Eventually(func() bool { return counter.count(org.UID) >= 1 },
		2*time.Second, 5*time.Millisecond)
	before := counter.count(org.UID)

	r.NoError(svc.CancelJob(context.Background(), job.UID))
	r.Eventually(func() bool { return counter.count(org.UID) > before },
		2*time.Second, 5*time.Millisecond, "cancel must hint `jobs` for the job's org")
}
