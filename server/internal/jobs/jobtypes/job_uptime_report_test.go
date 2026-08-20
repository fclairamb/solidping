package jobtypes

import (
	"context"
	"encoding/json"
	"log/slog"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/app/services"
	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/jobs/jobdef"
	"github.com/fclairamb/solidping/server/internal/jobs/jobsvc"
	"github.com/fclairamb/solidping/server/internal/uptimereport"
	"github.com/fclairamb/solidping/server/internal/utils/clock"
)

// capturingJobService records every job CreateJob is asked for, so a test can
// assert exactly which recipients were mailed. Everything else is inherited
// from the embedded nil interface and panics if called.
type capturingJobService struct {
	jobsvc.Service
	created []capturedJob
}

type capturedJob struct {
	orgUID  string
	jobType string
	config  json.RawMessage
}

var _ jobsvc.Service = (*capturingJobService)(nil)

func (s *capturingJobService) CreateJob(
	_ context.Context, orgUID, jobType string, config json.RawMessage, _ *jobsvc.JobOptions,
) (*models.Job, error) {
	s.created = append(s.created, capturedJob{orgUID: orgUID, jobType: jobType, config: config})

	return &models.Job{UID: uuid.Must(uuid.NewV7()).String()}, nil
}

// recipients returns the To: of every enqueued email job, in order.
func (s *capturingJobService) recipients(t *testing.T) []string {
	t.Helper()

	out := make([]string, 0, len(s.created))

	for _, job := range s.created {
		if job.jobType != string(jobdef.JobTypeEmail) {
			continue
		}

		var cfg EmailJobConfig
		require.NoError(t, json.Unmarshal(job.config, &cfg))
		out = append(out, cfg.To...)
	}

	return out
}

func newReportEnv(
	t *testing.T, now time.Time,
) (*sqlite.Service, *models.Organization, *jobdef.JobContext, *capturingJobService) {
	t.Helper()

	r := require.New(t)
	ctx := t.Context()

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	org := &models.Organization{
		UID:       uuid.New().String(),
		Slug:      "acme",
		Name:      "acme",
		CreatedAt: now.Add(-365 * 24 * time.Hour),
		UpdatedAt: now,
	}
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	jobs := &capturingJobService{}
	registry := services.NewRegistry()
	registry.Jobs = jobs
	registry.Clock = clock.NewFake(now)

	appConfig := &config.Config{}
	appConfig.Auth.JWTSecret = "test-secret"
	appConfig.Server.BaseURL = "https://acme.example"

	jctx := &jobdef.JobContext{
		DBService: dbSvc,
		Services:  registry,
		AppConfig: appConfig,
		Logger:    slog.Default(),
		Job:       &models.Job{UID: uuid.New().String()},
	}

	return dbSvc, org, jctx, jobs
}

// TestUptimeReportPeriodCloseDetectionInTimezone pins that a schedule reports on
// the period that CLOSED in its OWN timezone: at 03:00 UTC on 1 August, a
// UTC schedule's July has closed, but a Pacific/Auckland schedule is already in
// August (UTC+12) so July closed for it too, while a Pacific/Honolulu schedule
// (UTC-10) is still in July and must report June.
func TestUptimeReportPeriodCloseDetectionInTimezone(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	now := time.Date(2026, 8, 1, 3, 0, 0, 0, time.UTC)

	tests := []struct {
		tz        string
		frequency string
		wantStart string
	}{
		{tz: "UTC", frequency: models.ReportFrequencyMonthly, wantStart: "2026-07-01T00:00:00Z"},
		// UTC+12: local time is 15:00 on 1 August, so July is the closed month.
		{tz: "Pacific/Auckland", frequency: models.ReportFrequencyMonthly, wantStart: "2026-06-30T12:00:00Z"},
		// UTC-10: local time is still 17:00 on 31 July, so the closed month is June.
		{tz: "Pacific/Honolulu", frequency: models.ReportFrequencyMonthly, wantStart: "2026-06-01T10:00:00Z"},
		// 1 August 2026 is a Saturday; the closed ISO week is 20-26 July.
		{tz: "UTC", frequency: models.ReportFrequencyWeekly, wantStart: "2026-07-20T00:00:00Z"},
	}

	for _, tt := range tests {
		schedule := models.NewReportSchedule("org", "digest", tt.frequency)
		schedule.Timezone = tt.tz

		window, loc := uptimereport.Window(schedule, now)
		r.Equal(tt.tz, loc.String())
		r.Equal(tt.wantStart, window.Start.UTC().Format(time.RFC3339),
			"tz=%s frequency=%s", tt.tz, tt.frequency)
		// The reported period must always be fully in the past.
		r.False(window.End.After(now), "tz=%s: the reported period must have closed", tt.tz)
	}
}

// TestUptimeReportRunsOncePerPeriod proves the sweep is idempotent: running it
// twice inside the same closed period mails the recipients exactly once.
func TestUptimeReportRunsOncePerPeriod(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := t.Context()
	now := time.Date(2026, 8, 1, 3, 0, 0, 0, time.UTC)

	dbSvc, org, jctx, jobs := newReportEnv(t, now)

	schedule := models.NewReportSchedule(org.UID, "Monthly digest", models.ReportFrequencyMonthly)
	schedule.Recipients = []string{"alice@acme.com", "bob@acme.com"}
	r.NoError(dbSvc.CreateReportSchedule(ctx, schedule))

	run := &UptimeReportJobRun{}
	r.NoError(run.Run(ctx, jctx))

	// Positive control: the first sweep really did mail both recipients, so the
	// "no new emails" assertion below is about suppression and not about a
	// pipeline that never worked.
	r.Equal([]string{"alice@acme.com", "bob@acme.com"}, jobs.recipients(t))

	before := len(jobs.recipients(t))
	r.NoError(run.Run(ctx, jctx))
	r.Len(jobs.recipients(t), before, "a second sweep inside the same closed period must mail nobody")
}

// TestUptimeReportRespectsSuppressionList proves a recipient who unsubscribed is
// skipped while the others still receive the report.
func TestUptimeReportRespectsSuppressionList(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := t.Context()
	now := time.Date(2026, 8, 1, 3, 0, 0, 0, time.UTC)

	dbSvc, org, jctx, jobs := newReportEnv(t, now)

	schedule := models.NewReportSchedule(org.UID, "Monthly digest", models.ReportFrequencyMonthly)
	schedule.Recipients = []string{"alice@acme.com", "bob@acme.com"}
	r.NoError(dbSvc.CreateReportSchedule(ctx, schedule))

	r.NoError(dbSvc.CreateEmailSuppression(ctx, models.NewEmailSuppression(
		org.UID, "alice@acme.com", nil, models.EmailSuppressionSourceLink,
	)))

	run := &UptimeReportJobRun{}
	r.NoError(run.Run(ctx, jctx))

	got := jobs.recipients(t)
	// Positive control first: the payload is non-empty, so "alice is absent" is
	// a real statement rather than a vacuous one over an empty list.
	r.NotEmpty(got)
	r.Equal([]string{"bob@acme.com"}, got)
}

// A schedule with no recipients must not even claim its period — otherwise
// adding a recipient later would silently skip the month it was configured in.
func TestUptimeReportSkipsEmptyRecipientList(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := t.Context()
	now := time.Date(2026, 8, 1, 3, 0, 0, 0, time.UTC)

	dbSvc, org, jctx, jobs := newReportEnv(t, now)

	schedule := models.NewReportSchedule(org.UID, "Empty", models.ReportFrequencyMonthly)
	r.NoError(dbSvc.CreateReportSchedule(ctx, schedule))

	run := &UptimeReportJobRun{}
	r.NoError(run.Run(ctx, jctx))

	r.Empty(jobs.recipients(t))

	stored, err := dbSvc.GetReportSchedule(ctx, org.UID, schedule.UID)
	r.NoError(err)
	r.Nil(stored.LastPeriodStart, "an unsent period must stay unclaimed")
}

// A disabled schedule is never in the sweep's work list at all.
func TestUptimeReportSkipsDisabledSchedule(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := t.Context()
	now := time.Date(2026, 8, 1, 3, 0, 0, 0, time.UTC)

	dbSvc, org, jctx, jobs := newReportEnv(t, now)

	enabled := models.NewReportSchedule(org.UID, "On", models.ReportFrequencyMonthly)
	enabled.Recipients = []string{"alice@acme.com"}
	r.NoError(dbSvc.CreateReportSchedule(ctx, enabled))

	disabled := models.NewReportSchedule(org.UID, "Off", models.ReportFrequencyMonthly)
	disabled.Recipients = []string{"bob@acme.com"}
	disabled.Enabled = false
	r.NoError(dbSvc.CreateReportSchedule(ctx, disabled))

	run := &UptimeReportJobRun{}
	r.NoError(run.Run(ctx, jctx))

	// Positive control attached: alice proves the sweep ran at all.
	r.Equal([]string{"alice@acme.com"}, jobs.recipients(t))
}

// The sweep must keep itself alive.
func TestUptimeReportReschedulesItself(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := t.Context()
	now := time.Date(2026, 8, 1, 3, 0, 0, 0, time.UTC)

	_, _, jctx, jobs := newReportEnv(t, now)

	run := &UptimeReportJobRun{}
	r.NoError(run.Run(ctx, jctx))

	found := false

	for _, job := range jobs.created {
		if job.jobType == string(jobdef.JobTypeUptimeReport) {
			found = true
		}
	}

	r.True(found, "the sweep must reschedule itself")
}

// A digest is bulk mail: RFC 2369 / RFC 8058 unsubscribe headers are mandatory.
// The in-body link alone leaves a recipient whose client only exposes the
// header-level control with no way to stop the mail.
func TestUptimeReportCarriesUnsubscribeHeaders(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := t.Context()
	now := time.Date(2026, 8, 1, 3, 0, 0, 0, time.UTC)

	dbSvc, org, jctx, jobs := newReportEnv(t, now)

	schedule := models.NewReportSchedule(org.UID, "Monthly digest", models.ReportFrequencyMonthly)
	schedule.Recipients = []string{"alice@acme.com"}
	r.NoError(dbSvc.CreateReportSchedule(ctx, schedule))

	run := &UptimeReportJobRun{}
	r.NoError(run.Run(ctx, jctx))

	var cfg EmailJobConfig

	found := false

	for _, job := range jobs.created {
		if job.jobType != string(jobdef.JobTypeEmail) {
			continue
		}

		r.NoError(json.Unmarshal(job.config, &cfg))

		found = true
	}

	// Positive control: an email job really was enqueued, so the assertions
	// below are about its contents rather than about an empty loop.
	r.True(found)
	r.NotEmpty(cfg.ListUnsubscribeURL)
	r.Contains(cfg.ListUnsubscribeURL, "https://acme.example/unsubscribe?token=")

	// The same URL must reach the template footer, so the in-body link and the
	// header can never point at different scopes.
	data, ok := cfg.TemplateData.(map[string]any)
	r.True(ok)
	r.Equal(cfg.ListUnsubscribeURL, data["UnsubscribeURL"])
}
