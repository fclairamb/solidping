package backend

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/fclairamb/solidping/server/internal/checkworker/checkjobsvc"
	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/handlers/incidents"
	"github.com/fclairamb/solidping/server/internal/notifier"
	"github.com/fclairamb/solidping/server/internal/prommetrics"
)

// DirectBackend implements WorkerBackend by calling the database and services
// directly. This is the production path when the worker runs in the same
// process as the API server; its SubmitResult mirrors the exact
// save-result → process-incidents → release-lease sequence CheckWorker
// performed inline before the WorkerBackend refactor.
type DirectBackend struct {
	dbService   db.Service
	checkJobSvc checkjobsvc.Service
	incidentSvc *incidents.Service
	events      notifier.EventNotifier
}

// NewDirectBackend creates a DirectBackend.
func NewDirectBackend(
	dbService db.Service,
	checkJobSvc checkjobsvc.Service,
	incidentSvc *incidents.Service,
	events notifier.EventNotifier,
) *DirectBackend {
	return &DirectBackend{
		dbService:   dbService,
		checkJobSvc: checkJobSvc,
		incidentSvc: incidentSvc,
		events:      events,
	}
}

// Register registers or updates a worker in the database.
func (b *DirectBackend) Register(
	ctx context.Context, worker *models.Worker,
) (*models.Worker, error) {
	return b.dbService.RegisterOrUpdateWorker(ctx, worker)
}

// Heartbeat updates the worker's last_active_at timestamp.
func (b *DirectBackend) Heartbeat(
	ctx context.Context, workerUID string,
) error {
	return b.dbService.UpdateWorkerHeartbeat(ctx, workerUID)
}

// ClaimJobs claims up to fastLimit jobs for the given worker with the slow
// lane bounded by slowLimit (spec 2026-07-01-03 D3).
func (b *DirectBackend) ClaimJobs(
	ctx context.Context,
	workerUID string,
	region *string,
	fastLimit int,
	slowLimit int,
	maxAhead time.Duration,
) ([]*models.CheckJob, error) {
	return b.checkJobSvc.ClaimJobs(
		ctx, workerUID, region, fastLimit, slowLimit, maxAhead,
	)
}

// ClaimJobsForCheck claims any due job rows for one check (express path).
func (b *DirectBackend) ClaimJobsForCheck(
	ctx context.Context,
	workerUID string,
	region *string,
	checkUID string,
) ([]*models.CheckJob, error) {
	return b.checkJobSvc.ClaimJobsForCheck(ctx, workerUID, region, checkUID)
}

// SubmitResult saves the result row (with status tracking), processes
// incidents, and releases the lease — with scheduling state when provided,
// plain otherwise. Incident processing only runs after a successful save (a
// result that never landed must not drive the incident state machine); the
// lease is released regardless so the job never wedges behind a failed write.
func (b *DirectBackend) SubmitResult(
	ctx context.Context,
	job *models.CheckJob,
	workerUID string,
	req *SubmitResultRequest,
) error {
	resultUID, err := uuid.NewV7()
	if err != nil {
		return fmt.Errorf("failed to generate result UID: %w", err)
	}

	status := req.Status
	duration := req.Duration
	lastForStatus := true
	result := &models.Result{
		UID:             resultUID.String(),
		OrganizationUID: job.OrganizationUID,
		CheckUID:        job.CheckUID,
		PeriodType:      models.PeriodTypeRaw,
		PeriodStart:     time.Now(),
		WorkerUID:       &workerUID,
		Region:          req.Region,
		Status:          &status,
		Duration:        &duration,
		Metrics:         models.JSONMap(req.Metrics),
		Output:          models.JSONMap(req.Output),
		CreatedAt:       time.Now(),
		LastForStatus:   &lastForStatus,
	}

	saveStart := time.Now()
	saveErr := b.dbService.SaveResultWithStatusTracking(ctx, result)
	prommetrics.RecordCheckStage("save_result", time.Since(saveStart).Seconds())

	if saveErr == nil {
		incStart := time.Now()
		b.processIncidents(ctx, job, result)
		prommetrics.RecordCheckStage("process_incident", time.Since(incStart).Seconds())
	} else {
		slog.ErrorContext(ctx, "Failed to save result", "error", saveErr, "check_uid", job.CheckUID)
	}

	releaseStart := time.Now()

	var releaseErr error
	if req.Sched != nil {
		releaseErr = b.checkJobSvc.ReleaseLeaseWithSchedulingState(
			ctx, job.UID, workerUID, req.NextScheduledAt,
			req.Sched.CostEWMAMs, req.Sched.DelayEWMAMs,
			req.Sched.EffectiveScheduledAt, req.Sched.Lane,
		)
	} else {
		releaseErr = b.checkJobSvc.ReleaseLease(ctx, job.UID, workerUID, req.NextScheduledAt)
	}

	prommetrics.RecordCheckStage("release_lease", time.Since(releaseStart).Seconds())

	if saveErr != nil {
		return fmt.Errorf("failed to save result: %w", saveErr)
	}

	if releaseErr != nil {
		return fmt.Errorf("failed to release lease: %w", releaseErr)
	}

	return nil
}

// processIncidents mirrors CheckWorker's incident hot path: use the check
// attached at claim time, falling back to a fetch when missing.
func (b *DirectBackend) processIncidents(ctx context.Context, job *models.CheckJob, result *models.Result) {
	check := job.Check
	if check == nil {
		var err error

		check, err = b.dbService.GetCheck(ctx, job.OrganizationUID, job.CheckUID)
		if err != nil {
			slog.WarnContext(ctx, "Failed to fetch check for incident processing", "error", err)

			return
		}
	}

	if err := b.incidentSvc.ProcessCheckResult(ctx, check, result); err != nil {
		slog.WarnContext(ctx, "Failed to process check result for incidents", "error", err)
	}
}

// ReleaseLease releases the lease and reschedules without writing a result.
func (b *DirectBackend) ReleaseLease(
	ctx context.Context,
	job *models.CheckJob,
	workerUID string,
	nextScheduledAt time.Time,
) error {
	return b.checkJobSvc.ReleaseLease(ctx, job.UID, workerUID, nextScheduledAt)
}

// LastResults returns the latest result per check (passive checks).
func (b *DirectBackend) LastResults(
	ctx context.Context, orgUID string, checkUIDs []string,
) (map[string]*models.Result, error) {
	return b.dbService.GetLastResultForChecks(ctx, orgUID, checkUIDs)
}

// Hints subscribes to check.created events (the in-process express hint).
func (b *DirectBackend) Hints() <-chan string {
	return b.events.Listen("check.created")
}
