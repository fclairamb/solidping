package jobtypes

import (
	"database/sql"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/jobs/jobdef"
)

func runReportTestSend(
	t *testing.T, cfg ReportTestSendJobConfig, jctx *jobdef.JobContext,
) error {
	t.Helper()

	raw, err := json.Marshal(cfg)
	require.NoError(t, err)

	run, err := (&ReportTestSendJobDefinition{}).CreateJobRun(raw)
	require.NoError(t, err)

	return run.Run(t.Context(), jctx)
}

func TestReportTestSendBuildsAndMailsOneAddress(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	now := time.Date(2026, 8, 1, 3, 0, 0, 0, time.UTC)
	dbSvc, org, jctx, jobs := newReportEnv(t, now)

	// Zero checks on purpose: unlike the sweep, a test send mails an empty report.
	schedule := models.NewReportSchedule(org.UID, "Digest", models.ReportFrequencyMonthly)
	schedule.Recipients = []string{"alice@acme.com", "bob@acme.com"}
	r.NoError(dbSvc.CreateReportSchedule(t.Context(), schedule))

	err := runReportTestSend(t, ReportTestSendJobConfig{
		OrgUID: org.UID, ScheduleUID: schedule.UID, Address: "me@acme.com", Now: now,
	}, jctx)

	r.NoError(err)
	r.Equal([]string{"me@acme.com"}, jobs.recipients(t), "only the caller, never the schedule's recipients")
}

func TestReportTestSendHonoursSuppressionAtRunTime(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	now := time.Date(2026, 8, 1, 3, 0, 0, 0, time.UTC)
	dbSvc, org, jctx, jobs := newReportEnv(t, now)

	schedule := models.NewReportSchedule(org.UID, "Digest", models.ReportFrequencyMonthly)
	r.NoError(dbSvc.CreateReportSchedule(t.Context(), schedule))

	sup := models.NewEmailSuppression(org.UID, "me@acme.com", nil, models.EmailSuppressionSourceLink)
	r.NoError(dbSvc.CreateEmailSuppression(t.Context(), sup))

	cfg := ReportTestSendJobConfig{OrgUID: org.UID, ScheduleUID: schedule.UID, Address: "me@acme.com", Now: now}
	r.NoError(runReportTestSend(t, cfg, jctx))
	r.Empty(jobs.recipients(t))

	// Positive control: another address on the same schedule is mailed.
	cfg.Address = "other@acme.com"
	r.NoError(runReportTestSend(t, cfg, jctx))
	r.Equal([]string{"other@acme.com"}, jobs.recipients(t))
}

func TestReportTestSendFailsCleanlyOnDeletedSchedule(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	now := time.Date(2026, 8, 1, 3, 0, 0, 0, time.UTC)
	dbSvc, org, jctx, jobs := newReportEnv(t, now)

	schedule := models.NewReportSchedule(org.UID, "Digest", models.ReportFrequencyMonthly)
	r.NoError(dbSvc.CreateReportSchedule(t.Context(), schedule))
	r.NoError(dbSvc.DeleteReportSchedule(t.Context(), org.UID, schedule.UID))

	err := runReportTestSend(t, ReportTestSendJobConfig{
		OrgUID: org.UID, ScheduleUID: schedule.UID, Address: "me@acme.com", Now: now,
	}, jctx)

	r.ErrorIs(err, sql.ErrNoRows)
	r.False(jobdef.IsRetryable(err), "a deleted schedule must not be retried")
	r.Empty(jobs.recipients(t))
}
