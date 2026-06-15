package jobworker

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	dto "github.com/prometheus/client_model/go"
	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/jobs/jobdef"
	"github.com/fclairamb/solidping/server/internal/jobs/jobsvc"
	"github.com/fclairamb/solidping/server/internal/prommetrics"
	"github.com/fclairamb/solidping/server/internal/stats"
)

// fakeJobSvc is a minimal jobsvc.Service stand-in for white-box worker tests.
// Only the methods the worker calls during processNext/handleResult do
// anything; the rest satisfy the interface.
type fakeJobSvc struct {
	waitJob    *models.Job
	lastStatus models.JobStatus
	retryErr   error
}

func (f *fakeJobSvc) GetJobWait(_ context.Context) (*models.Job, error) {
	return f.waitJob, nil
}

func (f *fakeJobSvc) UpdateJobStatus(
	_ context.Context, job *models.Job, status models.JobStatus, _ json.RawMessage,
) error {
	f.lastStatus = status
	job.Status = status

	return nil
}

func (f *fakeJobSvc) RetryJob(_ context.Context, job *models.Job) (*models.Job, error) {
	if f.retryErr != nil {
		return nil, f.retryErr
	}

	return models.NewJob(job.OrganizationUID, job.Type), nil
}

func (f *fakeJobSvc) CreateJob(
	_ context.Context, _, _ string, _ json.RawMessage, _ *jobsvc.JobOptions,
) (*models.Job, error) {
	return nil, errNotImplemented
}

func (f *fakeJobSvc) GetJob(_ context.Context, _ string) (*models.Job, error) {
	return nil, errNotImplemented
}

func (f *fakeJobSvc) ListJobs(
	_ context.Context, _ string, _ jobsvc.ListJobsOptions,
) ([]*models.Job, error) {
	return nil, errNotImplemented
}

func (f *fakeJobSvc) CancelJob(_ context.Context, _ string) error {
	return errNotImplemented
}

func (f *fakeJobSvc) CancelPendingForIncident(
	_ context.Context, _ string, _ *time.Time,
) (int64, error) {
	return 0, errNotImplemented
}

func (f *fakeJobSvc) CountQueueDepth(_ context.Context) (map[models.JobStatus]int, error) {
	return nil, errNotImplemented
}

// Static sentinel errors for the fake service and test cases (err113 linter
// disallows dynamic errors.New in non-trivial code).
var (
	errNotImplemented = errors.New("not implemented")
	errTransient      = errors.New("transient")
	errTerminal       = errors.New("terminal failure")
)

// retryableErr is a job error that the worker should classify as "retried".
func retryableErr() error {
	return jobdef.NewRetryableError(errTransient)
}

func newTestWorker(svc *fakeJobSvc) *JobWorker {
	logger := slog.Default()

	return &JobWorker{
		jobSvc: svc,
		logger: logger,
		stats:  stats.NewProcessingStats(time.Minute, time.Minute, logger),
	}
}

// TestHandleResultOutcome verifies that the outcome label handleResult returns
// matches the terminal status it writes, so the metric and DB row agree.
func TestHandleResultOutcome(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		jobErr      error
		retryCount  int
		wantOutcome string
		wantStatus  models.JobStatus
	}{
		{
			name:        "success",
			jobErr:      nil,
			wantOutcome: outcomeSuccess,
			wantStatus:  models.JobStatusSuccess,
		},
		{
			name:        "retryable error within retry budget -> retried",
			jobErr:      retryableErr(),
			retryCount:  0,
			wantOutcome: outcomeRetried,
			wantStatus:  models.JobStatusRetried,
		},
		{
			name:        "non-retryable error -> failed",
			jobErr:      errTerminal,
			wantOutcome: outcomeFailed,
			wantStatus:  models.JobStatusFailed,
		},
		{
			name:        "retryable but retries exhausted -> failed",
			jobErr:      retryableErr(),
			retryCount:  jobsvc.MaxRetryCount,
			wantOutcome: outcomeFailed,
			wantStatus:  models.JobStatusFailed,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			r := require.New(t)

			svc := &fakeJobSvc{}
			w := newTestWorker(svc)

			job := models.NewJob(nil, "email")
			job.RetryCount = tt.retryCount

			outcome, err := w.handleResult(context.Background(), w.logger, job, tt.jobErr)
			r.NoError(err)
			r.Equal(tt.wantOutcome, outcome)
			r.Equal(tt.wantStatus, svc.lastStatus)
		})
	}
}

// TestProcessNextRecordsUnknownTypeFailure verifies the early-exit unknown-type
// path records a "failed" outcome (and increments duration/scheduling-delay).
func TestProcessNextRecordsUnknownTypeFailure(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	const jobType = "totally-unknown-type-for-metrics-test"

	before := testutil.ToFloat64(prommetrics.JobsProcessed.WithLabelValues(jobType, outcomeFailed))

	svc := &fakeJobSvc{
		waitJob: func() *models.Job {
			j := models.NewJob(nil, jobType)
			j.ScheduledAt = time.Now().Add(-2 * time.Second) // positive scheduling delay
			return j
		}(),
	}
	w := newTestWorker(svc)

	err := w.processNext(context.Background(), w.logger)
	r.Error(err)
	r.ErrorIs(err, ErrUnknownJobType)
	r.Equal(models.JobStatusFailed, svc.lastStatus)

	after := testutil.ToFloat64(prommetrics.JobsProcessed.WithLabelValues(jobType, outcomeFailed))
	r.InDelta(before+1, after, 0.001)

	// Duration and scheduling-delay histograms got an observation for this type.
	r.Equal(uint64(1), histogramSampleCount(r,
		prommetrics.JobDuration.WithLabelValues(jobType, outcomeFailed)))
	r.Equal(uint64(1), histogramSampleCount(r,
		prommetrics.JobSchedulingDelay.WithLabelValues(jobType)))
}

// histogramSampleCount returns the sample count of a single histogram observer
// (the value returned by HistogramVec.WithLabelValues), which under the hood is
// a prometheus.Histogram (a Collector + Metric).
func histogramSampleCount(r *require.Assertions, obs prometheus.Observer) uint64 {
	h, ok := obs.(prometheus.Metric)
	r.True(ok, "observer is not a prometheus.Metric")

	var m dto.Metric
	r.NoError(h.Write(&m))
	r.NotNil(m.GetHistogram())

	return m.GetHistogram().GetSampleCount()
}

// TestRecordJobMetricsSchedulingDelay verifies the scheduling-delay observation
// reflects the supplied delay (positive vs clamped-zero handled by the caller).
func TestRecordJobMetricsSchedulingDelay(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	const jobTypePast = "metrics-delay-past"
	const jobTypeZero = "metrics-delay-zero"

	w := newTestWorker(&fakeJobSvc{})

	w.recordJobMetrics(jobTypePast, outcomeSuccess, 100*time.Millisecond, 5*time.Second)
	w.recordJobMetrics(jobTypeZero, outcomeSuccess, 100*time.Millisecond, 0)

	r.Equal(uint64(1), histogramSampleCount(r,
		prommetrics.JobSchedulingDelay.WithLabelValues(jobTypePast)))
	r.Equal(uint64(1), histogramSampleCount(r,
		prommetrics.JobSchedulingDelay.WithLabelValues(jobTypeZero)))
}
