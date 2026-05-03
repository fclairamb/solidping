// Package checkworker implements distributed check job execution.
package checkworker

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
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/fclairamb/solidping/server/internal/app/services"
	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/checkers/registry"
	"github.com/fclairamb/solidping/server/internal/checkworker/checkjobsvc"
	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/handlers/incidents"
	"github.com/fclairamb/solidping/server/internal/stats"
)

// Errors returned by the check runner.
var (
	ErrUnknownCheckType   = errors.New("unknown check type")
	ErrCheckerNotFound    = errors.New("checker not found for type")
	ErrFailedToParseConf  = errors.New("failed to parse config")
	ErrFailedToFetchCheck = errors.New("failed to fetch check from database")
	ErrNoCheckType        = errors.New("check job has no type set")
)

const (
	// heartbeatInterval is how often the worker updates its last_active_at.
	heartbeatInterval = 50 * time.Second

	periodTypeRaw = "raw"

	outputKeyMessage = "message"
)

// CheckWorker executes check jobs from the queue.
type CheckWorker struct {
	worker      *models.Worker
	dbService   db.Service
	checkJobSvc checkjobsvc.Service
	incidentSvc *incidents.Service
	config      *config.Config
	services    *services.Registry
	logger      *slog.Logger
	wg          sync.WaitGroup
	stats       stats.ProcessingStats

	// Channel-based architecture fields
	poolSize         int                   // Number of runner goroutines
	availableRunners atomic.Int32          // Runners waiting for jobs
	jobsChan         chan *models.CheckJob // Fetcher → Runners
	completionChan   chan struct{}         // Runners → Fetcher (wake-up signal)

	// Self-stats reporting fields
	internalCheckUID string // UID of the internal check for this worker
	defaultOrgUID    string // UID of the default organization
}

// NewCheckWorker creates a new check runner.
func NewCheckWorker(
	dbService db.Service,
	cfg *config.Config,
	svc *services.Registry,
	checkJobSvc checkjobsvc.Service,
) *CheckWorker {
	logger := slog.Default().With("component", "check_worker")

	poolSize := cfg.Server.CheckWorker.Nb
	if poolSize <= 0 {
		poolSize = 5
	}

	return &CheckWorker{
		dbService:   dbService,
		config:      cfg,
		services:    svc,
		checkJobSvc: checkJobSvc,
		incidentSvc: incidents.NewService(dbService, svc.Jobs),
		logger:      logger,
		stats:       stats.NewProcessingStats(time.Minute, time.Minute, logger),
		// Channel-based architecture
		poolSize:       poolSize,
		jobsChan:       make(chan *models.CheckJob),
		completionChan: make(chan struct{}, 1),
	}
}

// Run starts the runner loop (blocking).
func (r *CheckWorker) Run(ctx context.Context) error {
	r.logger.InfoContext(ctx, "Starting check worker")

	// 1. Register worker in database
	if err := r.registerWorker(ctx); err != nil {
		return fmt.Errorf("failed to register worker: %w", err)
	}

	r.logger.InfoContext(ctx, "Worker registered",
		"worker_uid", r.worker.UID,
		"worker_slug", r.worker.Slug,
		"pool_size", r.poolSize)

	// 2. Setup self-stats reporting
	if err := r.setupSelfStats(ctx); err != nil {
		r.logger.WarnContext(ctx, "Failed to setup self-stats, continuing without it", "error", err)
	}

	// 3. Start heartbeat goroutine
	r.wg.Add(1)
	go r.heartbeatLoop(ctx)

	// 4. Start runner pool
	for i := 0; i < r.poolSize; i++ {
		r.wg.Add(1)
		go r.runnerLoop(ctx, i)
	}

	// 5. Start fetcher (owns jobsChan, closes it on exit)
	r.wg.Add(1)
	go r.fetcherLoop(ctx)

	// 6. Start express runner (handles check.created events directly)
	r.wg.Add(1)
	go r.expressLoop(ctx)

	// 7. Wait for shutdown signal
	<-ctx.Done()
	r.logger.InfoContext(ctx, "Check worker stopping, waiting for goroutines")

	// 7. Wait for all goroutines to finish
	// The fetcherLoop closes jobsChan on exit, which signals runners to stop
	r.wg.Wait()
	r.logger.InfoContext(ctx, "Check worker stopped")

	return ctx.Err()
}

// registerWorker registers or updates the worker in the database.
func (r *CheckWorker) registerWorker(ctx context.Context) error {
	hostname, err := os.Hostname()
	if err != nil {
		hostname = "unknown"
	}

	// Limit hostname to ensure slug doesn't exceed 20 characters (slug = hostname-cr-X)
	// Reserve 5 chars for "-cr-X", leaving 15 for hostname
	if len(hostname) > 15 {
		hostname = hostname[:15]
	}

	slug := strings.ToLower(hostname)
	name := hostname

	region := "default"
	if r.config.Server.CheckWorker.Region != "" {
		region = r.config.Server.CheckWorker.Region
	}

	worker := &models.Worker{
		UID:    uuid.New().String(),
		Slug:   slug,
		Name:   name,
		Region: &region,
	}

	registeredWorker, err := r.dbService.RegisterOrUpdateWorker(ctx, worker)
	if err != nil {
		return err
	}

	r.worker = registeredWorker
	return nil
}

// heartbeatLoop periodically updates the worker's last_active_at.
func (r *CheckWorker) heartbeatLoop(ctx context.Context) {
	defer r.wg.Done()
	ticker := time.NewTicker(heartbeatInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			r.updateHeartbeat(ctx)
		}
	}
}

// updateHeartbeat updates the worker's last_active_at timestamp.
func (r *CheckWorker) updateHeartbeat(ctx context.Context) {
	if err := r.dbService.UpdateWorkerHeartbeat(ctx, r.worker.UID); err != nil {
		r.logger.ErrorContext(ctx, "Failed to update heartbeat", "error", err)
	}
}

// fetcherLoop fetches jobs from the database and distributes them to runners.
func (r *CheckWorker) fetcherLoop(ctx context.Context) {
	defer r.wg.Done()
	defer close(r.jobsChan) // Signal runners to exit when fetcher stops

	logger := r.logger.With("role", "fetcher")
	logger.InfoContext(ctx, "Fetcher started")
	defer logger.InfoContext(ctx, "Fetcher stopped")

	checkCreatedChan := r.services.EventNotifier.Listen("check.created")

	for {
		// Check for shutdown
		select {
		case <-ctx.Done():
			return
		default:
		}

		// Fetch and distribute jobs if runners are available
		if err := r.fetchAndDistributeJobs(ctx, logger); err != nil {
			if errors.Is(err, context.Canceled) {
				return
			}
			// Wait briefly before retry on error
			select {
			case <-ctx.Done():
				return
			case <-time.After(time.Second * 5):
			}
			continue
		}

		// Wait for next trigger
		select {
		case <-ctx.Done():
			return
		case <-r.completionChan:
			// A runner completed a job, capacity available
		case <-checkCreatedChan:
			// New check created, might be ready to execute
		case <-time.After(time.Minute):
			// Periodic check for newly-scheduled jobs
		}
	}
}

// fetchAndDistributeJobs claims jobs from the database and sends them to runners.
// Returns nil if no runners available or no jobs to distribute.
func (r *CheckWorker) fetchAndDistributeJobs(ctx context.Context, logger *slog.Logger) error {
	available := int(r.availableRunners.Load())

	if available == 0 {
		logger.DebugContext(ctx, "All runners busy, waiting for completion")
		return nil
	}

	cfg := r.config.Server.CheckWorker
	jobs, err := r.checkJobSvc.ClaimJobs(
		ctx,
		r.worker.UID,
		r.worker.Region,
		available,
		cfg.FetchMaxAhead,
	)
	if err != nil {
		if !errors.Is(err, context.Canceled) {
			logger.ErrorContext(ctx, "Failed to claim jobs", "error", err)
		}
		return err
	}

	// Distribute jobs to runners
	for _, job := range jobs {
		select {
		case r.jobsChan <- job:
			// Job delivered to a runner
		case <-ctx.Done():
			return ctx.Err()
		}
	}

	if len(jobs) > 0 {
		logger.DebugContext(ctx, "Distributed jobs",
			"count", len(jobs),
			"available_runners", available)
	}

	return nil
}

// expressLoop subscribes to check.created and runs the freshly-created
// check on its own goroutine, bypassing the regular runner pool. This
// keeps first-run latency bounded by execution time rather than by
// however long the busiest pool runner is mid-check. Jobs claimed here
// are atomic via the shared lease mechanism, so a parallel
// fetcherLoop pickup of the same row cannot double-execute it.
func (r *CheckWorker) expressLoop(ctx context.Context) {
	defer r.wg.Done()

	logger := r.logger.With("role", "express")
	logger.InfoContext(ctx, "Express runner started")
	defer logger.InfoContext(ctx, "Express runner stopped")

	events := r.services.EventNotifier.Listen("check.created")

	for {
		select {
		case <-ctx.Done():
			return
		case payload, ok := <-events:
			if !ok {
				return
			}
			r.handleExpressEvent(ctx, logger, payload)
		}
	}
}

// handleExpressEvent decodes one check.created payload and runs the
// matching job, if it can claim it. Old senders publish "{}" (no
// check_uid), in which case the express path silently no-ops and the
// regular fetcher still picks the new check up on its next poll.
func (r *CheckWorker) handleExpressEvent(ctx context.Context, logger *slog.Logger, payload string) {
	// The wire format uses snake_case to stay aligned with the check.* event
	// payloads stored in the events table; switching only the notifier
	// payload to camelCase would split the convention across two surfaces
	// for the same check_uid concept.
	var msg struct {
		CheckUID string `json:"check_uid"` //nolint:tagliatelle // intentional: matches event payload convention
	}

	if err := json.Unmarshal([]byte(payload), &msg); err != nil || msg.CheckUID == "" {
		return
	}

	jobs, err := r.checkJobSvc.ClaimJobsForCheck(ctx, r.worker.UID, r.worker.Region, msg.CheckUID)
	if err != nil {
		if !errors.Is(err, context.Canceled) {
			logger.WarnContext(ctx, "express claim failed",
				"error", err,
				"check_uid", msg.CheckUID)
		}

		return
	}

	for _, job := range jobs {
		if err := r.executeJob(ctx, logger, job); err != nil && !errors.Is(err, context.Canceled) {
			logger.ErrorContext(ctx, "express execution failed",
				"error", err,
				"check_uid", job.CheckUID)
		}
	}
}

// runnerLoop is the main loop for a runner goroutine.
func (r *CheckWorker) runnerLoop(ctx context.Context, id int) {
	defer r.wg.Done()

	logger := r.logger.With("runner_id", id)
	logger.InfoContext(ctx, "Runner started")
	defer logger.InfoContext(ctx, "Runner stopped")

	for {
		// Signal: "I'm available for work"
		r.availableRunners.Add(1)

		// Wait for a job
		var job *models.CheckJob
		var ok bool

		select {
		case job, ok = <-r.jobsChan:
			// Got a job or channel was closed
		case <-ctx.Done():
			r.availableRunners.Add(-1)
			return
		}

		// Signal: "I'm now busy"
		r.availableRunners.Add(-1)

		// Channel closed = shutdown
		if !ok {
			return
		}

		// Execute the job
		if err := r.executeJob(ctx, logger, job); err != nil {
			logger.ErrorContext(ctx, "Error executing job",
				"error", err,
				"check_uid", job.CheckUID)
		}

		// Signal completion to wake fetcher (non-blocking)
		select {
		case r.completionChan <- struct{}{}:
		default:
			// Channel already has a signal, that's fine
		}
	}
}

// executeJob executes a single check job.
//
//nolint:funlen,cyclop // Slightly over limits due to OTel tracing
func (r *CheckWorker) executeJob(
	ctx context.Context,
	logger *slog.Logger,
	checkJob *models.CheckJob,
) error {
	ctx, span := otel.Tracer("solidping.check").Start(
		ctx, "check.execute",
		trace.WithAttributes(
			attribute.String("check.uid", checkJob.CheckUID),
			attribute.String("check.type", checkJob.Type),
			attribute.String(
				"organization.uid",
				checkJob.OrganizationUID,
			),
		),
	)
	defer span.End()

	logger.InfoContext(
		ctx,
		"Executing check job",
		"check_type", checkJob.Type,
		"check_config", checkJob.Config,
	)

	startTime := time.Now()

	// 1. Get check type from check_jobs.type
	if checkJob.Type == "" {
		return r.saveErrorResult(ctx, checkJob, ErrNoCheckType)
	}
	checkType := checkJob.Type

	// Passive checks (heartbeat, email) don't make outbound requests — the
	// worker just inspects whether a recent inbound signal arrived in time.
	if isPassiveCheckType(checkerdef.CheckType(checkType)) {
		return r.executePassiveJob(ctx, logger, checkJob)
	}

	// 2. Parse check configuration
	var checkConfig checkerdef.Config

	// Parse config from check_jobs.config
	config, ok := registry.ParseConfig(checkerdef.CheckType(checkType))
	if !ok {
		return r.saveErrorResult(ctx, checkJob, fmt.Errorf("%w: %s", ErrUnknownCheckType, checkType))
	}

	if err := config.FromMap(checkJob.Config); err != nil {
		return r.saveErrorResult(ctx, checkJob, fmt.Errorf("%w: %w", ErrFailedToParseConf, err))
	}

	checkConfig = config

	// 3. Get checker from registry
	checker, ok := registry.GetChecker(checkerdef.CheckType(checkType))
	if !ok {
		return r.saveErrorResult(ctx, checkJob, fmt.Errorf("%w: %s", ErrCheckerNotFound, checkType))
	}

	sleepTime := checkJob.ScheduledAt.Sub(startTime)

	delay := time.Duration(0)
	if sleepTime < 0 {
		delay = -sleepTime
	}

	// Wait for the check time to come
	if sleepTime > 0 {
		timer := time.NewTimer(sleepTime)
		select {
		case <-ctx.Done():
			timer.Stop()
			// Server is shutting down — don't save a result, let the lease expire
			// so the job is picked up again on restart
			logger.InfoContext(ctx, "Server shutting down during sleep, leaving job for next startup")

			return nil
		case <-timer.C:
			// Scheduled time reached, continue with check execution
		}
	}

	// 4. Execute check with timeout
	// Use background context so the check can complete even during shutdown
	// Only the 30s timeout should cancel the check, not the runner shutdown
	execCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	result, err := checker.Execute(execCtx, checkConfig)
	if err != nil {
		duration := time.Since(startTime)

		// Distinguish timeout (check took too long) from other errors
		if errors.Is(err, context.DeadlineExceeded) {
			logger.WarnContext(ctx, "Check execution timed out", "duration_ms", duration.Milliseconds())
			result = &checkerdef.Result{
				Status:   checkerdef.StatusTimeout,
				Duration: duration,
				Output: map[string]any{
					checkerdef.OutputKeyError: "check execution timed out",
				},
			}
		} else {
			logger.ErrorContext(ctx, "Check execution failed", "error", err)
			result = &checkerdef.Result{
				Status:   checkerdef.StatusError,
				Duration: duration,
				Output: map[string]any{
					checkerdef.OutputKeyError: err.Error(),
				},
			}
		}
	}

	// 5. Save result
	// Use a fallback context for cleanup operations if the main context is canceled
	saveCtx := ctx //nolint:contextcheck // Conditional context assignment is intentional
	if ctx.Err() != nil {
		// Context is canceled, use background context with timeout for cleanup
		var cancel context.CancelFunc
		saveCtx, cancel = context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
	}

	r.stats.AddMetric(result.Status == checkerdef.StatusUp, result.Duration, delay)

	if err := r.saveResult(saveCtx, checkJob, *result); err != nil {
		logger.ErrorContext(ctx, "Failed to save result", "error", err)
		// Continue to release lease even if save failed
	}
	r.logger.InfoContext(ctx, "Check result saved")

	// 6. Release lease and reschedule
	// Use a fallback context for cleanup operations if the main context is canceled
	releaseCtx := ctx //nolint:contextcheck // Conditional context assignment is intentional
	if ctx.Err() != nil {
		// Context is canceled, use background context with timeout for cleanup
		var cancel context.CancelFunc
		releaseCtx, cancel = context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
	}

	if err := r.releaseLease(releaseCtx, checkJob); err != nil {
		return fmt.Errorf("failed to release lease: %w", err)
	}

	duration := time.Since(startTime)
	logger.InfoContext(ctx, "Check job completed",
		"status", result.Status,
		"duration_ms", duration.Milliseconds())

	return nil
}

// saveResult saves a check result to the database and processes incidents.
func (r *CheckWorker) saveResult(ctx context.Context, checkJob *models.CheckJob, checkResult checkerdef.Result) error {
	resultUID, err := uuid.NewV7()
	if err != nil {
		return fmt.Errorf("failed to generate result UID: %w", err)
	}

	status := int(checkResult.Status)
	durationMs := float32(checkResult.Duration.Seconds() * 1000)
	lastForStatus := true

	result := &models.Result{
		UID:             resultUID.String(),
		OrganizationUID: checkJob.OrganizationUID,
		CheckUID:        checkJob.CheckUID,
		PeriodType:      periodTypeRaw,
		PeriodStart:     time.Now(),
		WorkerUID:       &r.worker.UID,
		Region:          checkJob.Region,
		Status:          &status,
		Duration:        &durationMs,
		Metrics:         models.JSONMap(checkResult.Metrics),
		Output:          models.JSONMap(checkResult.Output),
		CreatedAt:       time.Now(),
		LastForStatus:   &lastForStatus,
	}

	if err := r.dbService.SaveResultWithStatusTracking(ctx, result); err != nil {
		return err
	}

	// Process incidents
	r.processIncidents(ctx, checkJob, result)

	return nil
}

// processIncidents handles incident creation/resolution based on check results.
func (r *CheckWorker) processIncidents(ctx context.Context, checkJob *models.CheckJob, result *models.Result) {
	// Fetch the check to get threshold configuration
	check, err := r.dbService.GetCheck(ctx, checkJob.OrganizationUID, checkJob.CheckUID)
	if err != nil {
		r.logger.WarnContext(ctx, "Failed to fetch check for incident processing", "error", err)
		return
	}

	if err := r.incidentSvc.ProcessCheckResult(ctx, check, result); err != nil {
		r.logger.WarnContext(ctx, "Failed to process check result for incidents", "error", err)
	}
}

// saveErrorResult saves an error result when check execution fails.
func (r *CheckWorker) saveErrorResult(ctx context.Context, checkJob *models.CheckJob, err error) error {
	resultUID, uidErr := uuid.NewV7()
	if uidErr != nil {
		return fmt.Errorf("failed to generate result UID: %w", uidErr)
	}

	status := int(checkerdef.StatusError)
	durationMs := float32(0)
	lastForStatus := true

	result := &models.Result{
		UID:             resultUID.String(),
		OrganizationUID: checkJob.OrganizationUID,
		CheckUID:        checkJob.CheckUID,
		PeriodType:      periodTypeRaw,
		PeriodStart:     time.Now(),
		WorkerUID:       &r.worker.UID,
		Region:          checkJob.Region,
		Status:          &status,
		Duration:        &durationMs,
		Metrics:         make(models.JSONMap),
		Output:          models.JSONMap{checkerdef.OutputKeyError: err.Error()},
		CreatedAt:       time.Now(),
		LastForStatus:   &lastForStatus,
	}

	// Use a fallback context for cleanup operations if the main context is canceled
	saveCtx := ctx //nolint:contextcheck // Conditional context assignment is intentional
	if ctx.Err() != nil {
		// Context is canceled, use background context with timeout for cleanup
		var cancel context.CancelFunc
		saveCtx, cancel = context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
	}

	insertErr := r.dbService.SaveResultWithStatusTracking(saveCtx, result)

	// Process incidents for error results
	r.processIncidents(saveCtx, checkJob, result)

	// Also release the lease using fallback context
	releaseCtx := ctx //nolint:contextcheck // Conditional context assignment is intentional
	if ctx.Err() != nil {
		// Context is canceled, use background context with timeout for cleanup
		var cancel context.CancelFunc
		releaseCtx, cancel = context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
	}
	_ = r.releaseLease(releaseCtx, checkJob)

	return insertErr
}

// releaseLease releases the job lease and reschedules for next execution.
func (r *CheckWorker) releaseLease(ctx context.Context, checkJob *models.CheckJob) error {
	// Parse period and calculate next scheduled time
	nextScheduledAt := r.calculateNextScheduledAt(checkJob)

	return r.checkJobSvc.ReleaseLease(ctx, checkJob.UID, r.worker.UID, nextScheduledAt)
}

// isPassiveCheckType reports whether a check type is passive — driven by
// inbound signals (HTTP heartbeats, incoming emails) rather than outbound
// probes. Passive checks share the same overdue/grace-period logic.
func isPassiveCheckType(t checkerdef.CheckType) bool {
	return t == checkerdef.CheckTypeHeartbeat || t == checkerdef.CheckTypeEmail
}

// passiveSignalNoun returns the human-readable noun used in result messages
// for the given passive check type ("heartbeat" / "email").
func passiveSignalNoun(t checkerdef.CheckType) string {
	if t == checkerdef.CheckTypeEmail {
		return "Email"
	}

	return "Heartbeat"
}

// executePassiveJob handles passive check jobs (heartbeat, email).
// Instead of making a network request, it inspects whether a recent inbound
// signal landed within the check's period.
func (r *CheckWorker) executePassiveJob(ctx context.Context, logger *slog.Logger, checkJob *models.CheckJob) error {
	period := time.Duration(checkJob.Period)
	noun := passiveSignalNoun(checkerdef.CheckType(checkJob.Type))

	// Get the latest result for this check
	lastResults, err := r.dbService.GetLastResultForChecks(ctx, []string{checkJob.CheckUID})
	if err != nil {
		return r.saveErrorResult(ctx, checkJob, fmt.Errorf("failed to get last result: %w", err))
	}

	// Determine status based on recency of last passive signal
	status := checkerdef.StatusDown
	output := map[string]any{outputKeyMessage: "No " + strings.ToLower(noun) + " received"}

	if lastResult, ok := lastResults[checkJob.CheckUID]; ok && lastResult.Status != nil {
		elapsed := time.Since(lastResult.PeriodStart)

		switch {
		// Last result was UP and recent enough
		case *lastResult.Status == int(checkerdef.StatusUp) && elapsed <= period:
			status = checkerdef.StatusUp
			output = map[string]any{
				outputKeyMessage: noun + " received",
				"lastSignalAt":   lastResult.PeriodStart.Format(time.RFC3339),
			}

		// Last result was UP but overdue
		case *lastResult.Status == int(checkerdef.StatusUp):
			output = map[string]any{
				outputKeyMessage: noun + " overdue",
				"lastSignalAt":   lastResult.PeriodStart.Format(time.RFC3339),
				"overdueBy":      (elapsed - period).String(),
			}

		// Last result was RUNNING and still within grace period (2x period)
		case *lastResult.Status == int(checkerdef.StatusRunning) && elapsed <= period*2:
			status = checkerdef.StatusRunning
			output = map[string]any{
				outputKeyMessage: "Run in progress",
				"runStarted":     lastResult.PeriodStart.Format(time.RFC3339),
			}

		// Last result was RUNNING but exceeded grace period — stale run
		case *lastResult.Status == int(checkerdef.StatusRunning):
			status = checkerdef.StatusTimeout
			output = map[string]any{
				outputKeyMessage: "Run started but never completed",
				"runStarted":     lastResult.PeriodStart.Format(time.RFC3339),
				"overdueBy":      (elapsed - period*2).String(),
			}
		}
	}

	result := checkerdef.Result{
		Status:   status,
		Duration: 0,
		Metrics:  make(map[string]any),
		Output:   output,
	}

	r.stats.AddMetric(result.Status == checkerdef.StatusUp, result.Duration, 0)

	if err := r.saveResult(ctx, checkJob, result); err != nil {
		logger.ErrorContext(ctx, "Failed to save passive check result", "error", err)
	}

	if err := r.releaseLease(ctx, checkJob); err != nil {
		return fmt.Errorf("failed to release lease: %w", err)
	}

	logger.InfoContext(ctx, "Passive check completed",
		"type", checkJob.Type,
		"status", result.Status,
		"check_uid", checkJob.CheckUID)

	return nil
}

// calculateNextScheduledAt calculates the next scheduled time for a check job.
// If scheduled_at + period > now, use scheduled_at + period (we're on schedule).
// Otherwise, use now + period (we're behind schedule).
func (r *CheckWorker) calculateNextScheduledAt(checkJob *models.CheckJob) time.Time {
	// Convert Period to time.Duration
	intervalDuration := time.Duration(checkJob.Period)

	now := time.Now()

	if checkJob.ScheduledAt == nil {
		// No scheduled_at, schedule for now + period
		return now.Add(intervalDuration)
	}

	nextScheduled := checkJob.ScheduledAt.Add(intervalDuration)

	if nextScheduled.After(now) {
		// We're on schedule
		return nextScheduled
	}

	// We're behind schedule, catch up
	return now.Add(intervalDuration)
}

// setupSelfStats configures self-stats reporting for the worker.
func (r *CheckWorker) setupSelfStats(ctx context.Context) error {
	// Get the default organization
	org, err := r.dbService.GetOrganizationBySlug(ctx, "default")
	if err != nil {
		return fmt.Errorf("failed to get default organization: %w", err)
	}
	r.defaultOrgUID = org.UID

	// Create or get the internal check
	if err := r.createInternalCheck(ctx); err != nil {
		return fmt.Errorf("failed to create internal check: %w", err)
	}

	// Wire up the stats reporter
	r.stats.SetReporter(r.reportStats)
	r.stats.SetFreeRunnersFunc(func() float64 {
		return float64(r.availableRunners.Load())
	})

	r.logger.InfoContext(ctx, "Self-stats reporting configured",
		"internal_check_uid", r.internalCheckUID)

	return nil
}

// createInternalCheck creates or retrieves the internal check for this worker.
func (r *CheckWorker) createInternalCheck(ctx context.Context) error {
	slug := "int-checks-" + r.worker.Slug

	// Check if already exists
	existing, err := r.dbService.GetCheckByUidOrSlug(ctx, r.defaultOrgUID, slug)
	if err == nil && existing != nil {
		r.internalCheckUID = existing.UID

		// Fix legacy checks that had type "internal:checkworker" and internal=false
		if !existing.Internal || existing.Type != "checkworker" {
			internalTrue := true
			newType := "checkworker"
			_ = r.dbService.UpdateCheck(ctx, existing.UID, &models.CheckUpdate{
				Internal: &internalTrue,
				Type:     &newType,
			})
		}

		return nil
	}

	// Create new internal check
	check := models.NewCheck(r.defaultOrgUID, slug, "checkworker")
	name := "Check Worker: " + r.worker.Name
	check.Name = &name
	check.Enabled = false // Don't schedule it as a regular check
	check.Internal = true

	if err := r.dbService.CreateCheck(ctx, check); err != nil {
		return err
	}

	r.internalCheckUID = check.UID
	return nil
}

// reportStats saves worker stats as a result to the database.
func (r *CheckWorker) reportStats(reported stats.ReportedStats) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Determine status: UP if at least one check succeeded
	status := int(models.ResultStatusUp)
	if reported.TotalChecks == 0 || reported.TotalChecks == reported.FailedChecks {
		status = int(models.ResultStatusDown)
	}

	resultUID, err := uuid.NewV7()
	if err != nil {
		r.logger.Error("Failed to generate result UID for self-stats", "error", err)
		return
	}

	result := &models.Result{
		UID:             resultUID.String(),
		OrganizationUID: r.defaultOrgUID,
		CheckUID:        r.internalCheckUID,
		PeriodType:      periodTypeRaw,
		PeriodStart:     time.Now(),
		WorkerUID:       &r.worker.UID,
		Status:          &status,
		Metrics: models.JSONMap{
			"job_runs":         reported.TotalChecks,
			"free_runners":     reported.FreeRunners,
			"average_duration": reported.AverageDuration,
			"average_delay":    reported.AverageDelay,
		},
		CreatedAt: time.Now(),
	}

	if err := r.dbService.CreateResult(ctx, result); err != nil {
		r.logger.Error("Failed to save self-stats result", "error", err)
	}
}
