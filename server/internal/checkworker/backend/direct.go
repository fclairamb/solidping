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
)

// DirectBackend implements WorkerBackend by calling the database and
// services directly.  This is used when the worker runs in the same
// process as the API server.
type DirectBackend struct {
	dbService   db.Service
	checkJobSvc checkjobsvc.Service
	incidentSvc *incidents.Service
}

// NewDirectBackend creates a DirectBackend.
func NewDirectBackend(
	dbService db.Service,
	checkJobSvc checkjobsvc.Service,
	incidentSvc *incidents.Service,
) *DirectBackend {
	return &DirectBackend{
		dbService:   dbService,
		checkJobSvc: checkJobSvc,
		incidentSvc: incidentSvc,
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

// ClaimJobs claims up to limit jobs for the given worker.
func (b *DirectBackend) ClaimJobs(
	ctx context.Context,
	workerUID string,
	region *string,
	limit int,
	maxAhead time.Duration,
) ([]*models.CheckJob, error) {
	return b.checkJobSvc.ClaimJobs(
		ctx, workerUID, region, limit, maxAhead,
	)
}

// SubmitResult saves a result, processes incidents, and releases the
// job lease.
func (b *DirectBackend) SubmitResult(
	ctx context.Context,
	jobUID, workerUID string,
	req *SubmitResultRequest,
) (*SubmitResultResponse, error) {
	// 1. Look up the check job to get org/check UIDs and period.
	job, err := b.findJobByUID(ctx, jobUID)
	if err != nil {
		return nil, fmt.Errorf("job not found: %w", err)
	}

	// 2. Build the result model.
	resultUID, err := uuid.NewV7()
	if err != nil {
		return nil, fmt.Errorf("failed to generate result UID: %w", err)
	}

	lastForStatus := true
	result := &models.Result{
		UID:             resultUID.String(),
		OrganizationUID: job.OrganizationUID,
		CheckUID:        job.CheckUID,
		PeriodType:      models.PeriodTypeRaw,
		PeriodStart:     time.Now(),
		WorkerUID:       &workerUID,
		Region:          job.Region,
		Status:          &req.Status,
		Duration:        &req.Duration,
		Metrics:         models.JSONMap(req.Metrics),
		Output:          models.JSONMap(req.Output),
		CreatedAt:       time.Now(),
		LastForStatus:   &lastForStatus,
	}

	// 3. Save result with status tracking.
	if saveErr := b.dbService.SaveResultWithStatusTracking(ctx, result); saveErr != nil {
		return nil, fmt.Errorf("failed to save result: %w", saveErr)
	}

	// 4. Process incidents.
	check, checkErr := b.dbService.GetCheck(
		ctx, job.OrganizationUID, job.CheckUID,
	)
	if checkErr != nil {
		slog.WarnContext(ctx, "Failed to fetch check for incident processing",
			"error", checkErr)
	} else {
		if incErr := b.incidentSvc.ProcessCheckResult(
			ctx, check, result,
		); incErr != nil {
			slog.WarnContext(ctx,
				"Failed to process incidents", "error", incErr)
		}
	}

	// 5. Release lease and reschedule.
	nextScheduledAt := calculateNextScheduledAt(job)

	if err := b.checkJobSvc.ReleaseLease(
		ctx, job.UID, workerUID, nextScheduledAt,
	); err != nil {
		return nil, fmt.Errorf("failed to release lease: %w", err)
	}

	return &SubmitResultResponse{
		NextScheduledAt: nextScheduledAt,
	}, nil
}

// findJobByUID retrieves a single check job by UID using the DB.
func (b *DirectBackend) findJobByUID(
	ctx context.Context, jobUID string,
) (*models.CheckJob, error) {
	var job models.CheckJob

	err := b.dbService.DB().NewSelect().
		Model(&job).
		Where("uid = ?", jobUID).
		Scan(ctx)
	if err != nil {
		return nil, err
	}

	return &job, nil
}

// calculateNextScheduledAt mirrors the logic from CheckWorker.
func calculateNextScheduledAt(job *models.CheckJob) time.Time {
	intervalDuration := time.Duration(job.Period)
	now := time.Now()

	if job.ScheduledAt == nil {
		return now.Add(intervalDuration)
	}

	nextScheduled := job.ScheduledAt.Add(intervalDuration)
	if nextScheduled.After(now) {
		return nextScheduled
	}

	return now.Add(intervalDuration)
}
