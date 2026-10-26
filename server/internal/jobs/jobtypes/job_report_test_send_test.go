package jobtypes

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/app/services"
	"github.com/fclairamb/solidping/server/internal/db"
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

// failingReportDB makes one chosen call of the test send fail, the way a
// database outage would, leaving every other call on the real SQLite.
type failingReportDB struct {
	db.Service

	failGetSchedule bool
	failSuppressed  bool
	failListChecks  bool
}

var errReportDBDown = errors.New("db down")

func (s *failingReportDB) GetReportSchedule(
	ctx context.Context, orgUID, uid string,
) (*models.ReportSchedule, error) {
	if s.failGetSchedule {
		return nil, errReportDBDown
	}

	return s.Service.GetReportSchedule(ctx, orgUID, uid)
}

func (s *failingReportDB) IsEmailSuppressed(ctx context.Context, orgUID, email, scope string) (bool, error) {
	if s.failSuppressed {
		return false, errReportDBDown
	}

	return s.Service.IsEmailSuppressed(ctx, orgUID, email, scope)
}

func (s *failingReportDB) ListChecks(
	ctx context.Context, orgUID string, filter *models.ListChecksFilter,
) ([]*models.Check, int64, error) {
	if s.failListChecks {
		return nil, 0, errReportDBDown
	}

	return s.Service.ListChecks(ctx, orgUID, filter)
}

func TestReportTestSendDefinitionType(t *testing.T) {
	t.Parallel()

	require.Equal(t, jobdef.JobTypeReportTestSend, (&ReportTestSendJobDefinition{}).Type())
}

func TestReportTestSendRejectsABadConfig(t *testing.T) {
	t.Parallel()

	run, err := (&ReportTestSendJobDefinition{}).CreateJobRun(json.RawMessage(`{"orgUid": 12`))

	require.Error(t, err)
	require.Nil(t, run)
}

func TestReportTestSendErrorPaths(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 8, 1, 3, 0, 0, 0, time.UTC)

	tests := []struct {
		name string
		// setup tweaks the job context and the config before the run.
		setup         func(jctx *jobdef.JobContext, cfg *ReportTestSendJobConfig, dbSvc db.Service)
		wantErr       error
		wantRetryable bool
	}{
		{
			name: "unknown organization is a permanent failure",
			setup: func(_ *jobdef.JobContext, cfg *ReportTestSendJobConfig, _ db.Service) {
				cfg.OrgUID = uuid.New().String()
			},
			wantErr: sql.ErrNoRows,
		},
		{
			name: "schedule lookup outage is retried",
			setup: func(jctx *jobdef.JobContext, _ *ReportTestSendJobConfig, dbSvc db.Service) {
				jctx.DBService = &failingReportDB{Service: dbSvc, failGetSchedule: true}
			},
			wantErr:       errReportDBDown,
			wantRetryable: true,
		},
		{
			name: "suppression lookup outage is retried",
			setup: func(jctx *jobdef.JobContext, _ *ReportTestSendJobConfig, dbSvc db.Service) {
				jctx.DBService = &failingReportDB{Service: dbSvc, failSuppressed: true}
			},
			wantErr:       errReportDBDown,
			wantRetryable: true,
		},
		{
			name: "report build failure is retried",
			setup: func(jctx *jobdef.JobContext, _ *ReportTestSendJobConfig, dbSvc db.Service) {
				jctx.DBService = &failingReportDB{Service: dbSvc, failListChecks: true}
			},
			wantErr:       errReportDBDown,
			wantRetryable: true,
		},
		{
			name: "missing jobs service fails the enqueue",
			setup: func(jctx *jobdef.JobContext, _ *ReportTestSendJobConfig, _ db.Service) {
				jctx.Services = services.NewRegistry()
			},
			wantErr: errJobsUnavailable,
		},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			r := require.New(t)
			dbSvc, org, jctx, jobs := newReportEnv(t, now)

			schedule := models.NewReportSchedule(org.UID, "Digest", models.ReportFrequencyMonthly)
			r.NoError(dbSvc.CreateReportSchedule(t.Context(), schedule))

			cfg := ReportTestSendJobConfig{
				OrgUID: org.UID, ScheduleUID: schedule.UID, Address: "me@acme.com", Now: now,
			}
			testCase.setup(jctx, &cfg, dbSvc)

			err := runReportTestSend(t, cfg, jctx)

			r.ErrorIs(err, testCase.wantErr)
			r.Equal(testCase.wantRetryable, jobdef.IsRetryable(err))
			r.Empty(jobs.recipients(t))
		})
	}
}
