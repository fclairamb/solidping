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
	"github.com/fclairamb/solidping/server/internal/checkworker/scheduling"
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
	// schedParams drives the post-exec accounting this path performs on behalf
	// of remote executors (see SubmitResult). The zero value is "every
	// scheduling feature off", which reproduces flat FIFO behavior.
	schedParams scheduling.Params
}

// NewService creates a new workers service. schedParams should come from
// scheduling.ParamsFromConfig(cfg.Server.Scheduling) so remote results are
// scored with exactly the knobs the in-process worker uses.
func NewService(
	dbService db.Service,
	checkJobSvc checkjobsvc.Service,
	incidentSvc *incidents.Service,
	schedParams scheduling.Params,
) *Service {
	return &Service{
		db:          dbService,
		checkJobSvc: checkJobSvc,
		incidentSvc: incidentSvc,
		schedParams: schedParams,
	}
}

// Heartbeat updates the worker's last_active_at and, when the executor
// reported them, its egress families (spec 2026-08-15-11). A zero WorkerEgress
// leaves the capability columns untouched.
func (s *Service) Heartbeat(
	ctx context.Context, workerUID string, egress models.WorkerEgress,
) error {
	return s.db.UpdateWorkerHeartbeat(ctx, workerUID, egress)
}

// SubmitResultRequest is the input for SubmitResult.
type SubmitResultRequest struct {
	JobUID    string         `json:"jobUid"`
	WorkerUID string         `json:"workerUid"`
	Status    int            `json:"status"`
	Duration  float32        `json:"duration"`
	Metrics   map[string]any `json:"metrics,omitempty"`
	Output    map[string]any `json:"output,omitempty"`
	// ExecStart is the executor's wall-clock probe start, used for the delay
	// sample. nil contributes no delay sample (an agent predating the field).
	ExecStart *time.Time `json:"execStart,omitempty"`
	// FromProbe marks a result produced by an actual check execution, whose
	// duration is therefore a real cost sample. Dispatch-time error results
	// (undecryptable credentials, a tunnel that could not be assembled) leave it
	// false: they never ran the probe, so folding their zero duration into the
	// cost EWMA would make an expensive check look cheap and flip its lane.
	FromProbe bool `json:"fromProbe,omitempty"`
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

	// 5. Release lease, folding in the post-exec scheduling state.
	//
	// This is the accounting parity point (spec 2026-07-27-01 item 4): results
	// arriving over the agent transport used to release the lease and nothing
	// else, so cost/delay EWMAs and lane classification only ever advanced for
	// in-process workers. A shared cloud region served by platform agents would
	// therefore have degraded to unweighted FIFO with every job pinned in the
	// lane it was created in. The computation is the same scheduling.PostExec
	// the in-process worker runs.
	nextScheduledAt := calculateNextScheduledAt(&job)

	if err := s.releaseLease(ctx, &job, req, nextScheduledAt); err != nil {
		return nil, fmt.Errorf(
			"failed to release lease: %w", err,
		)
	}

	return &SubmitResultResponse{
		NextScheduledAt: nextScheduledAt,
	}, nil
}

// releaseLease releases the job's lease: with the recomputed scheduling state
// for a real probe result, plain otherwise (a dispatch-time error never ran the
// check, so it carries no cost or delay sample to fold in — the same rule
// CheckWorker applies when it submits an error result without a Sched block).
func (s *Service) releaseLease(
	ctx context.Context, job *models.CheckJob, req *SubmitResultRequest, nextScheduledAt time.Time,
) error {
	if !req.FromProbe {
		return s.checkJobSvc.ReleaseLease(ctx, job.UID, req.WorkerUID, nextScheduledAt)
	}

	state := s.schedParams.PostExec(&scheduling.PostExecInput{
		PrevCostEWMAMs:       job.CostEWMAMs,
		PrevDelayEWMAMs:      job.DelayEWMAMs,
		PrevLane:             job.Lane,
		PlanWeight:           job.PlanWeight,
		ScheduledAt:          job.ScheduledAt,
		EffectiveScheduledAt: job.EffectiveScheduledAt,
		DurationMs:           float64(req.Duration),
		TimedOut:             req.Status == int(models.ResultStatusTimeout),
		ExecStart:            req.ExecStart,
		NextScheduledAt:      nextScheduledAt,
	})

	return s.checkJobSvc.ReleaseLeaseWithSchedulingState(
		ctx, job.UID, req.WorkerUID, nextScheduledAt,
		state.CostEWMAMs, state.DelayEWMAMs, state.EffectiveScheduledAt, state.Lane,
	)
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
