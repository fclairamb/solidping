// Package jobworker implements the job execution engine.
package jobworker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
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
	"github.com/fclairamb/solidping/server/internal/stats"
)

const (
	// maxRetryCount is the maximum number of retries allowed for a job.
	maxRetryCount = 2
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
}

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
func (w *JobWorker) workerLoop(ctx context.Context, runnerID int) {
	defer w.wg.Done()

	logger := w.logger.With("runner_id", runnerID)
	logger.InfoContext(ctx, "Job runner started")
	defer logger.InfoContext(ctx, "Job runner stopped")

	for {
		select {
		case <-ctx.Done():
			return
		default:
			// Signal: "I'm available for work"
			w.availableRunners.Add(1)

			err := w.processNext(ctx, logger)

			// Signal: "I'm now busy" (or done)
			w.availableRunners.Add(-1)

			if err != nil {
				// Don't log context.Canceled as an error during shutdown
				if !errors.Is(err, context.Canceled) {
					logger.ErrorContext(ctx, "Error processing job", "error", err)
				}
			}
		}
	}
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

		return fmt.Errorf("%w: %s", ErrUnknownJobType, job.Type)
	}

	// 3. Convert config to json.RawMessage
	configBytes, err := json.Marshal(job.Config)
	if err != nil {
		_ = w.jobSvc.UpdateJobStatus(ctx, job, models.JobStatusFailed,
			json.RawMessage(fmt.Sprintf(`{"error": "invalid config: %q}`, err.Error())))
		w.stats.AddMetric(false, time.Since(startTime), delay)

		return fmt.Errorf("failed to marshal job config: %w", err)
	}

	// 4. Create job instance
	jobRun, err := def.CreateJobRun(configBytes)
	if err != nil {
		_ = w.jobSvc.UpdateJobStatus(ctx, job, models.JobStatusFailed,
			json.RawMessage(fmt.Sprintf(`{"error": %q}`, err.Error())))
		w.stats.AddMetric(false, time.Since(startTime), delay)

		return fmt.Errorf("failed to create job run: %w", err)
	}

	// 5. Setup job context
	jctx := w.createJobContext(job, configBytes, logger)

	// 6. Execute with panic recovery
	jobErr := w.executeWithRecovery(ctx, jobRun, jctx)

	// 7. Record stats
	w.stats.AddMetric(jobErr == nil, time.Since(startTime), delay)

	// 8. Handle result (success, retry, or fail)
	return w.handleResult(ctx, logger, job, jobErr)
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

func (w *JobWorker) handleResult(ctx context.Context, logger *slog.Logger, job *models.Job, err error) error {
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

		return w.jobSvc.UpdateJobStatus(ctx, job, models.JobStatusSuccess, output)
	}

	logger.ErrorContext(ctx, "Job failed", "job_uid", job.UID, "error", err)

	if jobdef.IsRetryable(err) && job.RetryCount < maxRetryCount {
		// Retryable error and retries remaining: create retry job
		retryJob, retryErr := w.jobSvc.RetryJob(ctx, job)
		if retryErr != nil {
			logger.ErrorContext(ctx, "Failed to create retry job", "job_uid", job.UID, "error", retryErr)
		} else {
			logger.InfoContext(ctx, "Created retry job",
				"original_job_uid", job.UID,
				"retry_job_uid", retryJob.UID,
				"scheduled_at", retryJob.ScheduledAt)
		}
		// Mark original as retried
		return w.jobSvc.UpdateJobStatus(ctx, job, models.JobStatusRetried,
			json.RawMessage(fmt.Sprintf(`{"error": %q, "retried": true}`, err.Error())))
	}

	// Non-retryable error or max retries reached: mark as failed
	return w.jobSvc.UpdateJobStatus(ctx, job, models.JobStatusFailed,
		json.RawMessage(fmt.Sprintf(`{"error": %q}`, err.Error())))
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
	// Generate worker slug from hostname
	hostname, err := os.Hostname()
	if err != nil {
		hostname = "unknown"
	}
	if len(hostname) > 15 {
		hostname = hostname[:15]
	}
	w.workerSlug = strings.ToLower(hostname)

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
