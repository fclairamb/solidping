package jobworker

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"sync/atomic"
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
	leaseLost  bool
	jobDeleted atomic.Bool // flips the cancellation watcher's row check

	// waitFn, when set, replaces the canned waitJob answer so a test can script
	// a sequence of failures and successes (see worker_backoff_test.go).
	waitFn func(context.Context) (*models.Job, error)
}

func (f *fakeJobSvc) GetJobWait(ctx context.Context) (*models.Job, error) {
	if f.waitFn != nil {
		return f.waitFn(ctx)
	}

	return f.waitJob, nil
}

func (f *fakeJobSvc) UpdateJobStatus(
	_ context.Context, job *models.Job, status models.JobStatus, _ json.RawMessage,
) error {
	f.lastStatus = status
	job.Status = status

	return nil
}

func (f *fakeJobSvc) CompleteRunningJob(
	_ context.Context, job *models.Job, status models.JobStatus, _ json.RawMessage,
) error {
	if f.leaseLost {
		return jobsvc.ErrJobLeaseLost
	}

	f.lastStatus = status
	job.Status = status

	return nil
}

func (f *fakeJobSvc) ReapStuckJobs(_ context.Context, _ time.Duration) (jobsvc.ReapResult, error) {
	return jobsvc.ReapResult{}, errNotImplemented
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

func (f *fakeJobSvc) IsJobDeleted(_ context.Context, _ string) (bool, error) {
	return f.jobDeleted.Load(), nil
}

func (f *fakeJobSvc) CancelPendingForIncident(
	_ context.Context, _ string, _ *time.Time,
) (int64, error) {
	return 0, errNotImplemented
}

func (f *fakeJobSvc) ListCanceledPendingForIncident(
	_ context.Context, _, _ string,
) ([]*models.Job, error) {
	return nil, errNotImplemented
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

// TestHandleResultLeaseLost verifies that when the reaper has already moved a
// job out of 'running', the worker's terminal write is discarded: handleResult
// returns no error, increments the lease-lost metric, and does not clobber the
// status (the fake never records lastStatus on a lease-lost completion).
func TestHandleResultLeaseLost(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name        string
		jobType     string
		jobErr      error
		retryCount  int
		wantOutcome string
	}{
		// Distinct job types so the per-type lease-lost counter is isolated
		// across these parallel subtests (no shared-label race).
		{name: "success lost to reaper", jobType: "lease-lost-success", jobErr: nil, wantOutcome: outcomeSuccess},
		{
			name: "retried lost to reaper", jobType: "lease-lost-retried",
			jobErr: retryableErr(), retryCount: 0, wantOutcome: outcomeRetried,
		},
		{name: "failed lost to reaper", jobType: "lease-lost-failed", jobErr: errTerminal, wantOutcome: outcomeFailed},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			r := require.New(t)

			job := models.NewJob(nil, tc.jobType)
			job.RetryCount = tc.retryCount

			before := testutil.ToFloat64(prommetrics.JobsLeaseLost.WithLabelValues(job.Type))

			svc := &fakeJobSvc{leaseLost: true}
			w := newTestWorker(svc)

			outcome, err := w.handleResult(context.Background(), w.logger, job, tc.jobErr)
			r.NoError(err, "a lost lease must not error the worker")
			r.Equal(tc.wantOutcome, outcome)
			r.Empty(string(svc.lastStatus), "no terminal status should be recorded when the lease is lost")

			after := testutil.ToFloat64(prommetrics.JobsLeaseLost.WithLabelValues(job.Type))
			r.InDelta(before+1, after, 0.0001, "lease-lost metric should increment once")
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
	beforeDur := histogramSampleCount(r, prommetrics.JobDuration.WithLabelValues(jobType, outcomeFailed))
	beforeDelay := histogramSampleCount(r, prommetrics.JobSchedulingDelay.WithLabelValues(jobType))

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

	// Duration and scheduling-delay histograms got exactly one new observation
	// for this type (relative delta, not an absolute count: these vectors are
	// process-global and never reset between test iterations, so repeated
	// -count=N runs keep observing into the same child histogram).
	r.Equal(beforeDur+1, histogramSampleCount(r,
		prommetrics.JobDuration.WithLabelValues(jobType, outcomeFailed)))
	r.Equal(beforeDelay+1, histogramSampleCount(r,
		prommetrics.JobSchedulingDelay.WithLabelValues(jobType)))
}

// TestProcessNextCanceledMidRun is the regression test for mid-run job
// cancellation: when a job's row is soft-deleted while the job is running, the
// cancellation watcher must cancel the job's context (aborting a ctx-aware
// runner long before it finishes), write NO terminal status (the row is
// deleted), create NO retry clone (cancellation is terminal, even though
// context.Canceled comes back as the run error), and record the "canceled"
// outcome. Before the watcher existed, a canceled discovery chunk kept its
// runner slot for its full duration and the already-running guard blocked new
// scans the whole time.
func TestProcessNextCanceledMidRun(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	const jobType = "sleep" // registered, ctx-aware runner

	before := testutil.ToFloat64(prommetrics.JobsProcessed.WithLabelValues(jobType, outcomeCanceled))

	// A 30s sleep: if cancellation does not propagate, the test times out
	// loudly instead of passing by luck.
	job := models.NewJob(nil, jobType)
	job.Config = models.JSONMap{"seconds": 30}

	svc := &fakeJobSvc{waitJob: job}
	w := newTestWorker(svc)
	w.cancelWatchInterval = 10 * time.Millisecond

	// Soft-delete the row shortly after the job starts.
	go func() {
		time.Sleep(50 * time.Millisecond)
		svc.jobDeleted.Store(true)
	}()

	start := time.Now()
	err := w.processNext(context.Background(), w.logger)
	elapsed := time.Since(start)

	r.NoError(err, "a canceled job is not a processing error")
	r.Less(elapsed, 5*time.Second, "cancellation must abort the 30s sleep promptly")
	r.Empty(svc.lastStatus, "no terminal status may be written for a canceled job")

	after := testutil.ToFloat64(prommetrics.JobsProcessed.WithLabelValues(jobType, outcomeCanceled))
	r.InDelta(before+1, after, 0.001)
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

	beforePast := histogramSampleCount(r, prommetrics.JobSchedulingDelay.WithLabelValues(jobTypePast))
	beforeZero := histogramSampleCount(r, prommetrics.JobSchedulingDelay.WithLabelValues(jobTypeZero))

	w.recordJobMetrics(jobTypePast, outcomeSuccess, 100*time.Millisecond, 5*time.Second)
	w.recordJobMetrics(jobTypeZero, outcomeSuccess, 100*time.Millisecond, 0)

	// Relative delta: the vector is process-global and never reset between
	// test iterations, so repeated -count=N runs keep observing into the
	// same child histogram.
	r.Equal(beforePast+1, histogramSampleCount(r,
		prommetrics.JobSchedulingDelay.WithLabelValues(jobTypePast)))
	r.Equal(beforeZero+1, histogramSampleCount(r,
		prommetrics.JobSchedulingDelay.WithLabelValues(jobTypeZero)))
}
