package jobworker

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/getsentry/sentry-go"
	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/errorreport/errorreporttest"
	"github.com/fclairamb/solidping/server/internal/jobs/jobdef"
)

type panickingJob struct{}

func (panickingJob) Run(context.Context, *jobdef.JobContext) error {
	panic("simulated job panic")
}

type failingJob struct{}

var errJobFailed = errors.New("the job did its work and the work failed")

func (failingJob) Run(context.Context, *jobdef.JobContext) error {
	return errJobFailed
}

func jobPanicContext(orgUID *string) *jobdef.JobContext {
	return &jobdef.JobContext{
		OrganizationUID: orgUID,
		Job: &models.Job{
			UID:             "job-uid-42",
			OrganizationUID: orgUID,
			Type:            "aggregation",
		},
	}
}

func TestExecuteWithRecovery_PanicIsReportedWithJobTags(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	hub, transport, err := errorreporttest.NewHub()
	r.NoError(err)

	ctx := sentry.SetHubOnContext(t.Context(), hub)
	orgUID := "org-uid-7"
	worker := &JobWorker{}

	execErr := worker.executeWithRecovery(ctx, panickingJob{}, jobPanicContext(&orgUID))
	r.ErrorIs(execErr, ErrJobPanic)
	r.NotContains(execErr.Error(), "goroutine", "the stack belongs in Sentry, not in the persisted job message")

	events := transport.Events()
	r.Len(events, 1)
	r.Equal("aggregation", events[0].Tags["job.type"])
	r.Equal("job-uid-42", events[0].Tags["job.uid"])
	r.Equal(orgUID, events[0].Tags["organization"])
	r.Contains(jobEventText(events[0]), "simulated job panic")
}

// Positive control: a job that fails normally is not a crash, and reporting
// every failed job would drown the crashes this exists to surface.
func TestExecuteWithRecovery_OrdinaryFailureIsNotReported(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	hub, transport, err := errorreporttest.NewHub()
	r.NoError(err)

	ctx := sentry.SetHubOnContext(t.Context(), hub)
	worker := &JobWorker{}

	execErr := worker.executeWithRecovery(ctx, failingJob{}, jobPanicContext(nil))
	r.ErrorIs(execErr, errJobFailed)
	r.Equal(0, transport.Len())
}

// A system job has no organization; the tag must simply be absent rather than
// present and empty.
func TestExecuteWithRecovery_SystemJobPanicHasNoOrganizationTag(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	hub, transport, err := errorreporttest.NewHub()
	r.NoError(err)

	ctx := sentry.SetHubOnContext(t.Context(), hub)
	worker := &JobWorker{}

	r.Error(worker.executeWithRecovery(ctx, panickingJob{}, jobPanicContext(nil)))

	events := transport.Events()
	r.Len(events, 1)
	r.NotContains(events[0].Tags, "organization")
	r.Equal("aggregation", events[0].Tags["job.type"])
}

// jobEventText joins the event's exception chain: sentry-go unwraps the %w
// chain into one entry per level, so the panic value is not at index 0.
func jobEventText(event *sentry.Event) string {
	parts := make([]string, 0, len(event.Exception)+1)
	parts = append(parts, event.Message)

	for _, exception := range event.Exception {
		parts = append(parts, exception.Value)
	}

	return strings.Join(parts, " | ")
}
