package jobtypes

import (
	"context"
	"encoding/json"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/app/services"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/jobs/jobdef"
	"github.com/fclairamb/solidping/server/internal/jobs/jobsvc"
)

// recordingJobs is a jobsvc.Service that only records CreateJob calls.
type recordingJobs struct {
	jobsvc.Service

	calls []string
	delay time.Duration
	err   error
}

func (r *recordingJobs) CreateJob(
	_ context.Context, _, jobType string, _ json.RawMessage, opts *jobsvc.JobOptions,
) (*models.Job, error) {
	r.calls = append(r.calls, jobType)
	if opts != nil && opts.ScheduledAt != nil {
		r.delay = time.Until(*opts.ScheduledAt)
	}

	return nil, r.err
}

func sweepJctx(jobs jobsvc.Service) *jobdef.JobContext {
	jctx := &jobdef.JobContext{Logger: slog.New(slog.DiscardHandler)}
	if jobs != nil {
		jctx.Services = &services.Registry{Jobs: jobs}
	}

	return jctx
}

func TestPeriodicSweepExecute(t *testing.T) {
	t.Parallel()

	boom := errTestBoom

	tests := []struct {
		name      string
		run       func(context.Context, *jobdef.JobContext) (int, bool, error)
		jobsErr   error
		wantErr   bool
		retryable bool
		reschedul bool
	}{
		{"unwired reschedules and succeeds", func(context.Context, *jobdef.JobContext) (int, bool, error) {
			return 0, false, nil
		}, nil, false, false, true},
		{"failure reschedules and is retryable", func(context.Context, *jobdef.JobContext) (int, bool, error) {
			return 0, true, boom
		}, nil, true, true, true},
		{"work done reschedules", func(context.Context, *jobdef.JobContext) (int, bool, error) {
			return 3, true, nil
		}, nil, false, false, true},
		{"reschedule failure is not fatal", func(context.Context, *jobdef.JobContext) (int, bool, error) {
			return 1, true, nil
		}, boom, false, false, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			jobs := &recordingJobs{err: tt.jobsErr}
			sweep := periodicSweep{
				jobType:         jobdef.JobTypeSleep,
				defaultInterval: time.Hour,
				config:          sweepIntervalConfig{IntervalSeconds: 120},
				what:            "things",
				logMessage:      "did things",
				run:             tt.run,
			}

			err := sweep.execute(t.Context(), sweepJctx(jobs))
			if tt.wantErr {
				require.Error(t, err)
				require.Equal(t, tt.retryable, jobdef.IsRetryable(err))
				require.ErrorIs(t, err, boom)
			} else {
				require.NoError(t, err)
			}

			require.Equal(t, []string{string(jobdef.JobTypeSleep)}, jobs.calls)
			require.InDelta(t, 120, jobs.delay.Seconds(), 5, "interval override applies")
		})
	}
}

func TestPeriodicSweepRescheduleWithoutJobs(t *testing.T) {
	t.Parallel()

	sweep := periodicSweep{run: func(context.Context, *jobdef.JobContext) (int, bool, error) { return 0, true, nil }}

	require.NoError(t, sweep.execute(t.Context(), sweepJctx(nil)))
}

func TestDecodeSweepConfig(t *testing.T) {
	t.Parallel()

	cfg, err := decodeSweepConfig(nil, "x")
	require.NoError(t, err)
	require.Equal(t, time.Minute, cfg.interval(time.Minute))

	cfg, err = decodeSweepConfig(json.RawMessage(`{"intervalSeconds":7}`), "x")
	require.NoError(t, err)
	require.Equal(t, 7*time.Second, cfg.interval(time.Minute))

	_, err = decodeSweepConfig(json.RawMessage(`{"intervalSeconds":"bad"}`), "x")
	require.ErrorContains(t, err, "invalid x config")
}

// Every sweep job definition rejects a malformed config and runs unwired
// without error.
func TestSweepJobDefinitions(t *testing.T) {
	t.Parallel()

	defs := []jobdef.JobDefinition{
		&DegradedEvalJobDefinition{},
		&RegionHealthSweepJobDefinition{},
		&SLOBurnEvalJobDefinition{},
		&CheckFreshnessSweepJobDefinition{},
	}

	for _, def := range defs {
		t.Run(string(def.Type()), func(t *testing.T) {
			t.Parallel()

			_, err := def.CreateJobRun(json.RawMessage(`{"intervalSeconds":"bad"}`))
			require.Error(t, err)

			run, err := def.CreateJobRun(nil)
			require.NoError(t, err)

			jobs := &recordingJobs{}
			require.NoError(t, run.Run(t.Context(), sweepJctx(jobs)))
			require.Len(t, jobs.calls, 1, "the sweep re-arms itself")
		})
	}
}
