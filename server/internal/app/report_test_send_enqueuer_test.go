package app

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/app/services"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/i18n/recipient"
	"github.com/fclairamb/solidping/server/internal/jobs/jobdef"
	"github.com/fclairamb/solidping/server/internal/jobs/jobsvc"
	"github.com/fclairamb/solidping/server/internal/jobs/jobtypes"
)

var errQueueDown = errors.New("queue down")

// recordingJobs records the one CreateJob the enqueuer makes, or fails it.
type recordingJobs struct {
	jobsvc.Service

	err     error
	orgUID  string
	jobType string
	config  json.RawMessage
}

func (r *recordingJobs) CreateJob(
	_ context.Context, orgUID, jobType string, config json.RawMessage, _ *jobsvc.JobOptions,
) (*models.Job, error) {
	if r.err != nil {
		return nil, r.err
	}

	r.orgUID, r.jobType, r.config = orgUID, jobType, config

	return &models.Job{UID: "job-42"}, nil
}

func TestReportTestSendEnqueuerQueuesTheJob(t *testing.T) {
	t.Parallel()

	jobs := &recordingJobs{}
	enqueuer := &reportTestSendEnqueuer{services: &services.Registry{Jobs: jobs}}
	now := time.Date(2026, 8, 1, 3, 0, 0, 0, time.UTC)

	jobUID, err := enqueuer.EnqueueTestSend(
		t.Context(), "org-1", "sched-1", "me@acme.com",
		recipient.Hint{Locale: "fr", Timezone: "Europe/Paris"}, now,
	)

	require.NoError(t, err)
	require.Equal(t, "job-42", jobUID)
	require.Equal(t, "org-1", jobs.orgUID)
	require.Equal(t, string(jobdef.JobTypeReportTestSend), jobs.jobType)

	var cfg jobtypes.ReportTestSendJobConfig
	require.NoError(t, json.Unmarshal(jobs.config, &cfg))
	require.Equal(t, jobtypes.ReportTestSendJobConfig{
		OrgUID: "org-1", ScheduleUID: "sched-1", Address: "me@acme.com",
		Lang: "fr", Timezone: "Europe/Paris", Now: now,
	}, cfg)
}

func TestReportTestSendEnqueuerFailures(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		enqueuer *reportTestSendEnqueuer
		wantErr  error
	}{
		{
			name:     "no registry",
			enqueuer: &reportTestSendEnqueuer{},
			wantErr:  errReportJobsUnavailable,
		},
		{
			name:     "no jobs service",
			enqueuer: &reportTestSendEnqueuer{services: &services.Registry{}},
			wantErr:  errReportJobsUnavailable,
		},
		{
			name: "queue refuses the job",
			enqueuer: &reportTestSendEnqueuer{
				services: &services.Registry{Jobs: &recordingJobs{err: errQueueDown}},
			},
			wantErr: errQueueDown,
		},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			jobUID, err := testCase.enqueuer.EnqueueTestSend(
				t.Context(), "org-1", "sched-1", "me@acme.com", recipient.Hint{}, time.Now(),
			)

			require.ErrorIs(t, err, testCase.wantErr)
			require.Empty(t, jobUID)
		})
	}
}
