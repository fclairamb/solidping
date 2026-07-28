// Package workers provides the claim/submit business logic shared by every
// remote check executor.
//
// The HTTP edge-worker API this package once served was removed with spec
// 2026-07-16-02 (no production client; auth was a plaintext spw_ bearer token
// matched verbatim). What remains is the transport-agnostic service logic —
// SubmitResult (lease-ownership guard, result write, server-side incident
// processing) — which the WebSocket deported-agent handler reuses.
// Authentication is the transport's concern: the agent WS handshake verifies
// an Ed25519 signature before the upgrade, so no bearer credential exists in
// the database at all.
package workers

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/fclairamb/solidping/server/internal/activation"
	"github.com/fclairamb/solidping/server/internal/checkworker/checkjobsvc"
	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/handlers/incidents"
)

// Service errors.
var (
	ErrWorkerNotFound = errors.New("worker not found")
	ErrJobNotFound    = errors.New("check job not found")
)

// Service provides the transport-agnostic claim/submit business logic.
type Service struct {
	db          db.Service
	checkJobSvc checkjobsvc.Service
	incidentSvc *incidents.Service
}

// NewService creates a new workers service.
func NewService(
	dbService db.Service,
	checkJobSvc checkjobsvc.Service,
	incidentSvc *incidents.Service,
) *Service {
	return &Service{
		db:          dbService,
		checkJobSvc: checkJobSvc,
		incidentSvc: incidentSvc,
	}
}

// Heartbeat updates the worker's last_active_at.
func (s *Service) Heartbeat(
	ctx context.Context, workerUID string,
) error {
	return s.db.UpdateWorkerHeartbeat(ctx, workerUID)
}

// SubmitResultRequest is the input for SubmitResult.
type SubmitResultRequest struct {
	JobUID    string         `json:"jobUid"`
	WorkerUID string         `json:"workerUid"`
	Status    int            `json:"status"`
	Duration  float32        `json:"duration"`
	Metrics   map[string]any `json:"metrics,omitempty"`
	Output    map[string]any `json:"output,omitempty"`
}

// SubmitResultResponse is the output for SubmitResult.
type SubmitResultResponse struct {
	NextScheduledAt time.Time `json:"nextScheduledAt"`
}

// SubmitResult saves a check result, processes incidents, and releases
// the job lease.
func (s *Service) SubmitResult(
	ctx context.Context, req *SubmitResultRequest,
) (*SubmitResultResponse, error) {
	// 1. Look up the check job.
	var job models.CheckJob

	err := s.db.DB().NewSelect().
		Model(&job).
		Where("uid = ?", req.JobUID).
		Scan(ctx)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrJobNotFound, err)
	}

	// 2. Build the result.
	resultUID, err := uuid.NewV7()
	if err != nil {
		return nil, fmt.Errorf(
			"failed to generate result UID: %w", err,
		)
	}

	result := &models.Result{
		UID:             resultUID.String(),
		OrganizationUID: job.OrganizationUID,
		CheckUID:        job.CheckUID,
		PeriodType:      "raw",
		PeriodStart:     time.Now(),
		WorkerUID:       &req.WorkerUID,
		Region:          job.Region,
		Status:          &req.Status,
		Duration:        &req.Duration,
		Metrics:         models.JSONMap(req.Metrics),
		Output:          models.JSONMap(req.Output),
		CreatedAt:       time.Now(),
	}

	// 3. Save with status tracking.
	if saveErr := s.db.SaveResultWithStatusTracking(ctx, result); saveErr != nil {
		return nil, fmt.Errorf("failed to save result: %w", saveErr)
	}

	activation.Emit(ctx, s.db, job.OrganizationUID,
		models.EventTypeOrgActivationFirstResultReceived,
		activation.SourceSystem, "")

	// 4. Process incidents (best-effort).
	check, checkErr := s.db.GetCheck(
		ctx, job.OrganizationUID, job.CheckUID,
	)
	if checkErr != nil {
		slog.WarnContext(ctx,
			"Failed to fetch check for incidents", "error", checkErr)
	} else if incErr := s.incidentSvc.ProcessCheckResult(
		ctx, check, result,
	); incErr != nil {
		slog.WarnContext(ctx,
			"Failed to process incidents", "error", incErr)
	}

	// 5. Release lease.
	nextScheduledAt := calculateNextScheduledAt(&job)

	if err := s.checkJobSvc.ReleaseLease(
		ctx, job.UID, req.WorkerUID, nextScheduledAt,
	); err != nil {
		return nil, fmt.Errorf(
			"failed to release lease: %w", err,
		)
	}

	return &SubmitResultResponse{
		NextScheduledAt: nextScheduledAt,
	}, nil
}

// calculateNextScheduledAt mirrors the logic from CheckWorker.
func calculateNextScheduledAt(job *models.CheckJob) time.Time {
	interval := time.Duration(job.Period)
	now := time.Now()

	if job.ScheduledAt == nil {
		return now.Add(interval)
	}

	next := job.ScheduledAt.Add(interval)
	if next.After(now) {
		return next
	}

	return now.Add(interval)
}
