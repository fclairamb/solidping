// Package jobworker implements the job execution engine.
package jobworker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/uptrace/bun"

	"github.com/fclairamb/solidping/server/internal/app/services"
	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/jobs/jobdef"
	"github.com/fclairamb/solidping/server/internal/jobs/jobsvc"
	"github.com/fclairamb/solidping/server/internal/jobs/jobtypes"
	"github.com/fclairamb/solidping/server/internal/prommetrics"
	"github.com/fclairamb/solidping/server/internal/stats"
)

// JobWorker executes jobs from the queue.
type JobWorker struct {
	db        *bun.DB
	dbService db.Service
	config    *config.Config
	services  *services.Registry
	jobSvc    jobsvc.Service
	logger    *slog.Logger
	wg        sync.WaitGroup
	stats     stats.ProcessingStats

	// Pool tracking for self-stats
	poolSize         int
	availableRunners atomic.Int32

	// Self-stats reporting fields
	workerSlug       string // Slug identifier for this worker
	internalCheckUID string // UID of the internal check for this worker
	defaultOrgUID    string // UID of the default organization

	// cancelWatchInterval is how often a running job's row is re-checked for
	// soft-deletion; cancellation latency is bounded by one interval. Zero
	// falls back to defaultCancelWatchInterval (tests shrink it).
	cancelWatchInterval time.Duration

	// backoffMin/backoffMax bound the retry delay a runner waits after a failed
	// processNext. Zero falls back to errBackoffMinDelay/errBackoffMaxDelay (tests pin
	// them to make the wait observable without a wall-clock race).
	backoffMin time.Duration
	backoffMax time.Duration

	// backoffAfter produces the channel a backing-off runner waits on. Zero
	// falls back to time.After; tests substitute a recorder so the sequence of
	// requested delays can be asserted without sleeping.
	backoffAfter func(time.Duration) <-chan time.Time
}

// defaultCancelWatchInterval bounds how long a canceled (soft-deleted) job
// keeps running before its context is canceled.
const defaultCancelWatchInterval = 2 * time.Second

// Retry backoff bounds for a runner whose processNext keeps failing. A failing
// GetJobWait returns instantly (only "no work" blocks), so without a delay the
// loop retries at CPU speed and turns any persistent infrastructure error into
// a log bomb (one incident filled a 460 GB disk with the same two lines).
// (Bare "Min"/"Max" suffixes on a time.Duration read as minutes to the
// linter, hence the explicit "Delay".)
const (
	errBackoffMinDelay = 100 * time.Millisecond
	errBackoffMaxDelay = 30 * time.Second
)

// NewJobWorker creates a new job worker.
func NewJobWorker(
	database *bun.DB,
	dbService db.Service,
	cfg *config.Config,
	svc *services.Registry,
	jobSvc jobsvc.Service,
) *JobWorker {
	logger := slog.Default().With("component", "job_worker")

	poolSize := cfg.Server.JobWorker.Nb
	if poolSize <= 0 {
		poolSize = 2
	}

	return &JobWorker{
		db:        database,
		dbService: dbService,
		config:    cfg,
		services:  svc,
		jobSvc:    jobSvc,
		logger:    logger,
		stats:     stats.NewProcessingStats(time.Minute, time.Minute, logger),
		poolSize:  poolSize,
	}
}

// Run starts the worker with multiple internal runners (blocking).
func (w *JobWorker) Run(ctx context.Context) error {
	w.logger.InfoContext(ctx, "Starting job worker")

	w.logger.InfoContext(ctx, "Starting worker pool", "pool_size", w.poolSize)

	// Setup self-stats reporting
	if err := w.setupSelfStats(ctx); err != nil {
		w.logger.WarnContext(ctx, "Failed to setup self-stats, continuing without it", "error", err)
	}

	// Start worker goroutines
	for i := 0; i < w.poolSize; i++ {
		w.wg.Add(1)
		go w.workerLoop(ctx, i)
	}

	// Wait for shutdown
	<-ctx.Done()
	w.logger.InfoContext(ctx, "Job worker stopping, waiting for runners")
	w.wg.Wait()
	w.logger.InfoContext(ctx, "Job worker stopped")

	return ctx.Err()
}

// workerLoop is the main loop for a worker goroutine.
//
// processNext blocks while there is no work, but an error path returns
// instantly, so consecutive failures are spaced by an exponential backoff
// (errBackoffMinDelay → errBackoffMaxDelay, full jitter so runners diverge) and their
// logs are collapsed to the 1st, 2nd, 4th, 8th … occurrence. The backoff wait
// sits outside the availableRunners window: a runner that is sleeping off a
// failure is not available for work, and queue-depth metrics must not say
// otherwise.
func (w *JobWorker) workerLoop(ctx context.Context, runnerID int) {
	defer w.wg.Done()

	logger := w.logger.With("runner_id", runnerID)
	logger.InfoContext(ctx, "Job runner started")
	defer logger.InfoContext(ctx, "Job runner stopped")

	var (
		backoff      time.Duration
		consecutive  int
		firstFailure time.Time
	)

	for {
		if backoff > 0 {
			select {
			case <-ctx.Done():
				return
			case <-w.after(jitter(backoff)):
			}
		}

		select {
		case <-ctx.Done():
			return
		default:
		}

		// Signal: "I'm available for work"
		w.availableRunners.Add(1)

		err := w.processNext(ctx, logger)

		// Signal: "I'm now busy" (or done)
		w.availableRunners.Add(-1)

		switch {
		case err == nil:
			if consecutive > 0 {
				// One line per outage, not per failure: the count and the
				// duration are the information the flood was hiding.
				logger.InfoContext(ctx, "Job processing recovered",
					"consecutive_failures", consecutive,
					"outage", time.Since(firstFailure).String())
			}

			backoff, consecutive = 0, 0
		case errors.Is(err, context.Canceled):
			// Shutdown: not an error, and there is nothing left to do.
			return
		default:
			if consecutive == 0 {
				firstFailure = time.Now()
			}

			consecutive++

			if shouldLog(consecutive) {
				logger.ErrorContext(ctx, "Error processing job",
					"error", err, "consecutive", consecutive)
			}

			backoff = w.nextBackoff(backoff)
		}
	}
}

// after is the backoff timer, indirected so tests can observe the requested
// delays instead of waiting them out.
func (w *JobWorker) after(delay time.Duration) <-chan time.Time {
	if w.backoffAfter != nil {
		return w.backoffAfter(delay)
	}

	return time.After(delay)
}

// nextBackoff doubles the retry delay, starting at the minimum and saturating
// at the maximum.
func (w *JobWorker) nextBackoff(current time.Duration) time.Duration {
	minDelay, maxDelay := w.backoffMin, w.backoffMax
	if minDelay <= 0 {
		minDelay = errBackoffMinDelay
	}

	if maxDelay <= 0 {
		maxDelay = errBackoffMaxDelay
	}

	if current <= 0 {
		if minDelay > maxDelay {
			return maxDelay
		}

		return minDelay
	}

	if current >= maxDelay/2 {
		return maxDelay
	}

	return current * 2
}

// jitter returns a full-jitter delay in [delay/2, delay], so two runners that
// failed at the same instant do not keep retrying in lockstep.
func jitter(delay time.Duration) time.Duration {
	if delay <= 0 {
		return 0
	}

	half := delay / 2

	// Non-cryptographic on purpose: this only spreads runners apart.
	return half + time.Duration(rand.Int64N(int64(delay-half)+1))
}

// shouldLog reports whether a failure at this consecutive count deserves a log
// line: the 1st, 2nd, 4th, 8th … Ten thousand identical lines carry no more
// information than the first, and at the 30 s cap this keeps a permanently
// broken runner at a handful of lines per hour.
func shouldLog(consecutive int) bool {
	return consecutive > 0 && consecutive&(consecutive-1) == 0
}

func (w *JobWorker) processNext(ctx context.Context, logger *slog.Logger) error {
	// 1. Wait for and claim next job (sets status to 'running')
	job, err := w.jobSvc.GetJobWait(ctx)
	if err != nil {
		return err
	}

	startTime := time.Now()
	delay := startTime.Sub(job.ScheduledAt)
	if delay < 0 {
		delay = 0
	}

	logger.InfoContext(ctx, "Processing job",
		"job_uid", job.UID, "job_type", job.Type, "retry_count", job.RetryCount)

	// 2. Get job definition from registry
	def, ok := jobtypes.GetJobDefinition(jobdef.JobType(job.Type))
	if !ok {
		_ = w.jobSvc.UpdateJobStatus(ctx, job, models.JobStatusFailed,
			json.RawMessage(`{"error": "unknown job type"}`))
		w.stats.AddMetric(false, time.Since(startTime), delay)
		w.recordJobMetrics(job.Type, outcomeFailed, time.Since(startTime), delay)

		return fmt.Errorf("%w: %s", ErrUnknownJobType, job.Type)
	}

	// 3. Convert config to json.RawMessage
	configBytes, err := json.Marshal(job.Config)
	if err != nil {
		_ = w.jobSvc.UpdateJobStatus(ctx, job, models.JobStatusFailed,
			json.RawMessage(fmt.Sprintf(`{"error": "invalid config: %q}`, err.Error())))
		w.stats.AddMetric(false, time.Since(startTime), delay)
		w.recordJobMetrics(job.Type, outcomeFailed, time.Since(startTime), delay)

		return fmt.Errorf("failed to marshal job config: %w", err)
	}

	// 4. Create job instance
	jobRun, err := def.CreateJobRun(configBytes)
	if err != nil {
		_ = w.jobSvc.UpdateJobStatus(ctx, job, models.JobStatusFailed,
			json.RawMessage(fmt.Sprintf(`{"error": %q}`, err.Error())))
		w.stats.AddMetric(false, time.Since(startTime), delay)
		w.recordJobMetrics(job.Type, outcomeFailed, time.Since(startTime), delay)

		return fmt.Errorf("failed to create job run: %w", err)
	}

	// 5. Setup job context
	jctx := w.createJobContext(job, configBytes, logger)

	// 6. Execute with panic recovery.
	// Use a detached context so that canceling the polling context (worker
	// shutdown) does not abort a job that is already mid-execution. The job
	// was claimed; it should run to completion so status is recorded cleanly.
	// One escape hatch: a cancellation watcher polls the job row and cancels
	// the exec context when the row is soft-deleted mid-run (e.g. the user
	// stopped a discovery scan), so ctx-aware runners abort promptly instead
	// of holding their runner slot to completion.
	jobExecCtx, cancelExec := context.WithCancel(context.WithoutCancel(ctx))
	defer cancelExec()

	canceled := w.watchJobCancellation(jobExecCtx, job.UID, cancelExec)
	jobErr := w.executeWithRecovery(jobExecCtx, jobRun, jctx)

	// 7. Record stats
	duration := time.Since(startTime)

	if canceled.Load() {
		w.finishCanceled(ctx, logger, job, jobErr, duration, delay)

		return nil
	}

	w.stats.AddMetric(jobErr == nil, duration, delay)

	// 8. Handle result (success, retry, or fail). The outcome returned matches
	// the terminal status written by UpdateJobStatus, so the metric and the DB
	// row never disagree.
	outcome, resultErr := w.handleResult(jobExecCtx, logger, job, jobErr)
	w.recordJobMetrics(job.Type, outcome, duration, delay)

	return resultErr
}

// finishCanceled records a mid-run cancellation. Cancellation is terminal:
// no status is written (the row is soft-deleted, and a retry clone would
// resurrect canceled work); the slot time is recorded under its own outcome so
// canceled runs stay visible in metrics.
func (w *JobWorker) finishCanceled(
	ctx context.Context, logger *slog.Logger, job *models.Job, jobErr error, duration, delay time.Duration,
) {
	logger.InfoContext(ctx, "Job canceled mid-run (row soft-deleted)",
		"job_uid", job.UID, "job_type", job.Type, "run_error", jobErr)
	w.stats.AddMetric(false, duration, delay)
	w.recordJobMetrics(job.Type, outcomeCanceled, duration, delay)
}

// watchJobCancellation polls the job row while the job runs and cancels the
// exec context when the row is soft-deleted. The returned flag reports whether
// cancellation was triggered by row deletion (as opposed to the normal
// end-of-run cancel the caller performs). The watcher exits when execCtx ends,
// whichever side canceled it.
func (w *JobWorker) watchJobCancellation(
	execCtx context.Context, jobUID string, cancelExec context.CancelFunc,
) *atomic.Bool {
	interval := w.cancelWatchInterval
	if interval <= 0 {
		interval = defaultCancelWatchInterval
	}

	var canceled atomic.Bool

	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		for {
			select {
			case <-execCtx.Done():
				return
			case <-ticker.C:
				// Bounded by one interval, inherited from execCtx: while this
				// branch runs execCtx is live, and if the job finishes
				// mid-check the aborted lookup is irrelevant anyway.
				checkCtx, cancelCheck := context.WithTimeout(execCtx, interval)
				deleted, err := w.jobSvc.IsJobDeleted(checkCtx, jobUID)
				cancelCheck()

				if err != nil {
					// Transient lookup failure: keep the job running rather
					// than killing work on a DB hiccup.
					continue
				}

				if deleted {
					canceled.Store(true)
					cancelExec()

					return
				}
			}
		}
	}()

	return &canceled
}

// Job outcome label values for Prometheus, matching the terminal job status.
// outcomeCanceled is the exception: a canceled job's row is soft-deleted, so
// no terminal status is written at all.
const (
	outcomeSuccess  = "success"
	outcomeRetried  = "retried"
	outcomeFailed   = "failed"
	outcomeCanceled = "canceled"
)

// recordJobMetrics records the processed counter, duration histogram, and
// scheduling-delay histogram for one processed job. Recording is unconditional
// — metric collection stays on even when the /metrics endpoint is gated by
// SP_PROMETHEUS_ENABLED.
func (w *JobWorker) recordJobMetrics(jobType, outcome string, duration, delay time.Duration) {
	prommetrics.RecordJobProcessed(jobType, outcome)
	prommetrics.RecordJobDuration(jobType, outcome, duration.Seconds())
	prommetrics.RecordJobSchedulingDelay(jobType, delay.Seconds())
}

func (w *JobWorker) executeWithRecovery(
	ctx context.Context,
	job jobdef.JobRunner,
	jctx *jobdef.JobContext,
) (err error) {
	defer func() {
		if rec := recover(); rec != nil {
			err = fmt.Errorf("%w: %v", ErrJobPanic, rec)
		}
	}()

	return job.Run(ctx, jctx)
}

// handleResult writes the terminal job status and returns the outcome label
// ("success" | "retried" | "failed") matching that status, plus any error from
// the status update. The returned outcome drives the Prometheus metrics so they
// can never disagree with the DB row.
func (w *JobWorker) handleResult(
	ctx context.Context, logger *slog.Logger, job *models.Job, err error,
) (string, error) {
	if err == nil {
		// Success: mark job as successful with output from job execution
		logger.InfoContext(ctx, "Job completed successfully", "job_uid", job.UID)

		var output json.RawMessage
		if len(job.Output) > 0 {
			var marshalErr error
			output, marshalErr = json.Marshal(job.Output)
			if marshalErr != nil {
				logger.WarnContext(ctx, "Failed to marshal job output", "job_uid", job.UID, "error", marshalErr)
			}
		}

		return outcomeSuccess, w.completeTerminal(ctx, logger, job, models.JobStatusSuccess, output)
	}

	logger.ErrorContext(ctx, "Job failed", "job_uid", job.UID, "error", err)

	if jobdef.IsRetryable(err) && job.RetryCount < jobsvc.MaxRetryCount {
		return outcomeRetried, w.handleRetry(ctx, logger, job, err)
	}

	// Non-retryable error or max retries reached: mark as failed
	return outcomeFailed, w.completeTerminal(ctx, logger, job, models.JobStatusFailed,
		json.RawMessage(fmt.Sprintf(`{"error": %q}`, err.Error())))
}

// handleRetry marks the original 'retried' (guarded) then creates the backoff
// retry clone. The guarded flip means a worker that lost its job to the reaper
// neither clobbers the reaper's decision nor creates a duplicate retry clone on
// top of the reaper's own recovery — when the lease is lost we stop after the
// guarded flip.
func (w *JobWorker) handleRetry(
	ctx context.Context, logger *slog.Logger, job *models.Job, jobErr error,
) error {
	// Guarded flip first. CompleteRunningJob asserts status='running', so if the
	// reaper already moved the row we get ErrJobLeaseLost and bail out before
	// scheduling a second clone.
	flipErr := w.jobSvc.CompleteRunningJob(ctx, job, models.JobStatusRetried,
		json.RawMessage(fmt.Sprintf(`{"error": %q, "retried": true}`, jobErr.Error())))
	if errors.Is(flipErr, jobsvc.ErrJobLeaseLost) {
		w.noteLeaseLost(ctx, logger, job, models.JobStatusRetried)

		return nil
	}
	if flipErr != nil {
		return flipErr
	}

	// Reuse the clone half of the retry machinery. The job is already 'retried'
	// in memory, so RetryJob's own flip is an idempotent no-op WHERE uid match.
	retryJob, retryErr := w.jobSvc.RetryJob(ctx, job)
	if retryErr != nil {
		logger.ErrorContext(ctx, "Failed to create retry job", "job_uid", job.UID, "error", retryErr)

		return nil
	}

	logger.InfoContext(ctx, "Created retry job",
		"original_job_uid", job.UID,
		"retry_job_uid", retryJob.UID,
		"scheduled_at", retryJob.ScheduledAt)

	return nil
}

// completeTerminal writes a terminal status via the guarded CompleteRunningJob.
// If the job is no longer 'running' the reaper already transitioned it: the
// worker's result is discarded (logged + metric), and we return nil so the
// runner does not treat a lost lease as a processing error.
func (w *JobWorker) completeTerminal(
	ctx context.Context, logger *slog.Logger, job *models.Job, status models.JobStatus, output json.RawMessage,
) error {
	err := w.jobSvc.CompleteRunningJob(ctx, job, status, output)
	if errors.Is(err, jobsvc.ErrJobLeaseLost) {
		w.noteLeaseLost(ctx, logger, job, status)

		return nil
	}

	return err
}

// noteLeaseLost logs and meters a discarded terminal write (the reaper won the
// race for this job).
func (w *JobWorker) noteLeaseLost(
	ctx context.Context, logger *slog.Logger, job *models.Job, attempted models.JobStatus,
) {
	logger.WarnContext(ctx, "job lease lost, result discarded",
		"job_uid", job.UID, "job_type", job.Type, "attempted_status", attempted)
	prommetrics.RecordJobLeaseLost(job.Type)
}

// createJobContext creates a job context with a logger that includes the jobUid attribute.
func (w *JobWorker) createJobContext(
	job *models.Job,
	configBytes json.RawMessage,
	logger *slog.Logger,
) *jobdef.JobContext {
	// Create logger with jobUid attribute for log correlation
	jobLogger := logger.With("jobUid", job.UID)

	return &jobdef.JobContext{
		OrganizationUID: job.OrganizationUID,
		Job:             job,
		Config:          configBytes,
		Services:        w.services,
		DB:              w.db,
		DBService:       w.dbService,
		AppConfig:       w.config,
		Logger:          jobLogger,
	}
}

// setupSelfStats configures self-stats reporting for the worker.
func (w *JobWorker) setupSelfStats(ctx context.Context) error {
	w.resolveWorkerSlug(ctx)

	// Get the default organization
	org, err := w.dbService.GetOrganizationBySlug(ctx, "default")
	if err != nil {
		return fmt.Errorf("failed to get default organization: %w", err)
	}
	w.defaultOrgUID = org.UID

	// Create or get the internal check
	if err := w.createInternalCheck(ctx); err != nil {
		return fmt.Errorf("failed to create internal check: %w", err)
	}

	// Wire up the stats reporter
	w.stats.SetReporter(w.reportStats)
	w.stats.SetFreeRunnersFunc(func() float64 {
		return float64(w.availableRunners.Load())
	})

	w.logger.InfoContext(ctx, "Self-stats reporting configured",
		"internal_check_uid", w.internalCheckUID)

	return nil
}

// resolveWorkerSlug sets this worker's identity: SP_NODE_NAME when configured,
// otherwise the lowercased OS hostname truncated to
// config.WorkerHostnameMaxLen. It is the exact same resolution the check worker
// uses (config.Config.WorkerIdentity), so both agree on who this process is,
// and it emits the truncation WARN for the same reason.
func (w *JobWorker) resolveWorkerSlug(ctx context.Context) {
	identity := w.config.WorkerIdentity()
	identity.WarnIfTruncated(ctx, w.logger)
	w.workerSlug = identity.Slug
}

// createInternalCheck creates or retrieves the internal check for this worker.
func (w *JobWorker) createInternalCheck(ctx context.Context) error {
	slug := "int-jobs-" + w.workerSlug

	// Check if already exists
	existing, err := w.dbService.GetCheckByUidOrSlug(ctx, w.defaultOrgUID, slug)
	if err == nil && existing != nil {
		w.internalCheckUID = existing.UID

		// Fix legacy checks that had type "internal:jobworker" and internal=false
		if !existing.Internal || existing.Type != "jobworker" {
			internalTrue := true
			newType := "jobworker"
			_ = w.dbService.UpdateCheck(ctx, existing.UID, &models.CheckUpdate{
				Internal: &internalTrue,
				Type:     &newType,
			})
		}

		return nil
	}

	// Create new internal check
	check := models.NewCheck(w.defaultOrgUID, slug, "jobworker")
	name := "Job Worker: " + w.workerSlug
	check.Name = &name
	check.Enabled = false // Don't schedule it as a regular check
	check.Internal = true

	if err := w.dbService.CreateCheck(ctx, check); err != nil {
		return err
	}

	w.internalCheckUID = check.UID
	return nil
}

// reportStats saves worker stats as a result to the database.
func (w *JobWorker) reportStats(reported stats.ReportedStats) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Determine status: UP if at least one job succeeded
	status := int(models.ResultStatusUp)
	if reported.TotalChecks == 0 || reported.TotalChecks == reported.FailedChecks {
		status = int(models.ResultStatusDown)
	}

	resultUID, err := uuid.NewV7()
	if err != nil {
		w.logger.Error("Failed to generate result UID for self-stats", "error", err)
		return
	}

	result := &models.Result{
		UID:             resultUID.String(),
		OrganizationUID: w.defaultOrgUID,
		CheckUID:        w.internalCheckUID,
		PeriodType:      models.PeriodTypeRaw,
		PeriodStart:     time.Now(),
		Status:          &status,
		Metrics: models.JSONMap{
			"job_runs":         reported.TotalChecks,
			"free_runners":     reported.FreeRunners,
			"average_duration": reported.AverageDuration,
			"average_delay":    reported.AverageDelay,
		},
		CreatedAt: time.Now(),
	}

	if err := w.dbService.CreateResult(ctx, result); err != nil {
		w.logger.Error("Failed to save self-stats result", "error", err)
	}
}
