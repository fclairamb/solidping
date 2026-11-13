package jobtypes

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/app/services"
	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/jobs/jobdef"
	"github.com/fclairamb/solidping/server/internal/jobs/jobsvc"
	"github.com/fclairamb/solidping/server/internal/utils/clock"
)

// twoDigests is two organizations with one monthly digest each: acme mails
// alice, the neighbor mails bob. A failure scoped to acme must never cost
// the neighbor its report.
type twoDigests struct {
	jctx      *jobdef.JobContext
	jobs      *capturingJobService
	acme      *models.Organization
	acmeSched *models.ReportSchedule
}

func newTwoDigests(t *testing.T) (*twoDigests, func(fragments ...string) *queryFault) {
	t.Helper()

	r := require.New(t)
	ctx := t.Context()
	now := time.Date(2026, 8, 1, 3, 0, 0, 0, time.UTC)

	dbSvc, acme, jctx, jobs := newReportEnv(t, now)
	newCheck(t, dbSvc, acme)

	neighbor := &models.Organization{
		UID: uuid.New().String(), Slug: "acmetech", Name: "acmetech",
		CreatedAt: now.Add(-365 * 24 * time.Hour), UpdatedAt: now,
	}
	r.NoError(dbSvc.CreateOrganization(ctx, neighbor))
	newCheck(t, dbSvc, neighbor)

	acmeSched := models.NewReportSchedule(acme.UID, "Monthly digest", models.ReportFrequencyMonthly)
	acmeSched.Recipients = []string{"alice@acme.com"}
	r.NoError(dbSvc.CreateReportSchedule(ctx, acmeSched))

	other := models.NewReportSchedule(neighbor.UID, "Monthly digest", models.ReportFrequencyMonthly)
	other.Recipients = []string{"bob@acme.com"}
	r.NoError(dbSvc.CreateReportSchedule(ctx, other))

	world := &twoDigests{jctx: jctx, jobs: jobs, acme: acme, acmeSched: acmeSched}

	return world, func(fragments ...string) *queryFault { return failQueries(dbSvc.DB(), fragments...) }
}

func (w *twoDigests) rescheduled() bool {
	for _, job := range w.jobs.created {
		if job.jobType == string(jobdef.JobTypeUptimeReport) {
			return true
		}
	}

	return false
}

// One store call of acme's schedule fails per case. The sweep still succeeds,
// the neighbor is mailed, and the sweep re-arms.
func TestUptimeReport_OneBrokenScheduleDoesNotStopTheOthers(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		fail func(world *twoDigests) []string
	}{
		{
			"organization lookup",
			func(world *twoDigests) []string { return []string{`FROM "organizations"`, world.acme.UID} },
		},
		{
			"period claim",
			func(world *twoDigests) []string { return []string{`UPDATE "report_schedules"`, world.acmeSched.UID} },
		},
		{
			"report build",
			func(world *twoDigests) []string { return []string{`FROM "checks"`, world.acme.UID} },
		},
		{
			"suppression lookup",
			func(world *twoDigests) []string { return []string{`FROM "email_suppressions"`, world.acme.UID} },
		},
	}

	t.Run("control: nothing fails", func(t *testing.T) {
		t.Parallel()

		world, _ := newTwoDigests(t)
		require.NoError(t, (&UptimeReportJobRun{}).Run(t.Context(), world.jctx))
		require.ElementsMatch(t, []string{"alice@acme.com", "bob@acme.com"}, world.jobs.recipients(t))
	})

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			world, fail := newTwoDigests(t)
			fault := fail(tt.fail(world)...)

			require.NoError(t, (&UptimeReportJobRun{}).Run(t.Context(), world.jctx))
			fault.requireFired(t)

			require.Equal(t, []string{"bob@acme.com"}, world.jobs.recipients(t))
			require.True(t, world.rescheduled())
		})
	}
}

// refusingJobs refuses the email jobs of one organization.
type refusingJobs struct {
	*capturingJobService

	refuseOrg string
}

func (j *refusingJobs) CreateJob(
	ctx context.Context, orgUID, jobType string, config json.RawMessage, opts *jobsvc.JobOptions,
) (*models.Job, error) {
	if orgUID == j.refuseOrg && jobType == string(jobdef.JobTypeEmail) {
		return nil, errTestBoom
	}

	return j.capturingJobService.CreateJob(ctx, orgUID, jobType, config, opts)
}

// The queue refuses acme's email: the neighbor still gets its report.
func TestUptimeReport_EnqueueFailureIsScopedToItsSchedule(t *testing.T) {
	t.Parallel()

	world, _ := newTwoDigests(t)
	world.jctx.Services.Jobs = &refusingJobs{capturingJobService: world.jobs, refuseOrg: world.acme.UID}

	require.NoError(t, (&UptimeReportJobRun{}).Run(t.Context(), world.jctx))
	require.Equal(t, []string{"bob@acme.com"}, world.jobs.recipients(t))
	require.True(t, world.rescheduled())
}

// The sweep cannot list the schedules: it asks for a retry and mails nobody.
func TestUptimeReport_ListingFailureIsRetryable(t *testing.T) {
	t.Parallel()

	world, fail := newTwoDigests(t)
	fault := fail(`FROM "report_schedules"`)

	err := (&UptimeReportJobRun{}).Run(t.Context(), world.jctx)
	fault.requireFired(t)

	require.ErrorContains(t, err, "list report schedules")
	require.ErrorIs(t, err, context.Canceled)
	require.True(t, jobdef.IsRetryable(err))
	require.Empty(t, world.jobs.recipients(t))
}

// The interval override moves the next sweep; a process without a database
// or a job service has nothing to do and does not fail.
func TestUptimeReport_IntervalOverrideAndMissingServices(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	_, jctx := startupWorld(t)
	jobs := &recordingJobs{}
	fakeClock := clock.NewFake(time.Date(2026, 8, 1, 3, 0, 0, 0, time.UTC))
	jctx.AppConfig = &config.Config{}
	jctx.Services = &services.Registry{Jobs: jobs, Clock: fakeClock}

	run, err := (&UptimeReportJobDefinition{}).CreateJobRun(json.RawMessage(`{"intervalSeconds":120}`))
	r.NoError(err)
	r.NoError(run.Run(t.Context(), jctx))
	r.Equal([]string{string(jobdef.JobTypeUptimeReport)}, jobs.calls)
	r.InDelta(120, jobs.delay.Seconds(), 30)

	// No job service: the sweep runs, there is nowhere to queue the next one.
	jctx.Services = &services.Registry{Clock: fakeClock}
	r.NoError(run.Run(t.Context(), jctx))
	r.Len(jobs.calls, 1)

	// No database: nothing to sweep.
	jctx.Services = &services.Registry{Jobs: jobs}
	jctx.DBService = nil
	r.NoError(run.Run(t.Context(), jctx))
	r.Len(jobs.calls, 1, "nothing is queued without a database")
}
