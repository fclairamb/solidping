package backend

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/fclairamb/solidping/server/internal/checkworker/checkjobsvc"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/handlers/attachments"
)

// Errors of the multi-step backend surface.
var (
	// ErrNoStepStore means the backend was built without a step store.
	ErrNoStepStore = errors.New("multi-step state store not configured")
	// ErrNoStepState means the job has no state file to resume from.
	ErrNoStepState = errors.New("no step state to resume")
	// ErrStepStateMismatch means the state file belongs to another run.
	ErrStepStateMismatch = errors.New("step state belongs to another run")
	// ErrStepStateTooLarge means a state envelope is over the cap.
	ErrStepStateTooLarge = errors.New("step state too large")
)

var _ StepBackend = (*DirectBackend)(nil)

// LoadStepState implements StepBackend.
func (b *DirectBackend) LoadStepState(ctx context.Context, job *models.CheckJob) ([]byte, error) {
	if b.steps == nil {
		return nil, ErrNoStepStore
	}

	if job.StepStateFileUID == nil || job.StepRunUID == nil {
		return nil, ErrNoStepState
	}

	body, _, err := b.steps.ReadCheckFile(
		ctx, job.OrganizationUID, job.CheckUID, attachments.KindStepState, *job.StepStateFileUID,
	)
	if err != nil {
		return nil, err
	}

	env, err := attachments.DecodeStepState(body)
	if err != nil {
		return nil, err
	}

	if env.RunUID != *job.StepRunUID || env.CheckType != job.Type {
		return nil, ErrStepStateMismatch
	}

	return env.Payload, nil
}

// SaveStepState implements StepBackend.
func (b *DirectBackend) SaveStepState(
	ctx context.Context, job *models.CheckJob, req *SaveStepStateRequest,
) (string, error) {
	if b.steps == nil {
		return "", ErrNoStepStore
	}

	envelope, err := attachments.EncodeStepState(req.RunUID, job.Type, req.Payload)
	if err != nil {
		return "", fmt.Errorf("encode step state: %w", err)
	}

	if len(envelope) > attachments.MaxStepStateBytes {
		return "", ErrStepStateTooLarge
	}

	return b.steps.PutStepState(ctx, job.OrganizationUID, job.CheckUID, envelope,
		attachments.StepStateDetails(req.RunUID, req.Step, req.Progress))
}

// SubmitStep implements StepBackend.
func (b *DirectBackend) SubmitStep(
	ctx context.Context, job *models.CheckJob, workerUID string, req *SubmitStepRequest,
) error {
	return b.checkJobSvc.SubmitStep(ctx, job.UID, workerUID, &checkjobsvc.StepUpdate{
		ExpectedRunUID: job.StepRunUID,
		RunUID:         req.RunUID,
		RunStartedAt:   req.RunStartedAt,
		StateFileUID:   req.StateFileUID,
		Failed:         req.Failed,
		NextAt:         req.NextAt,
	})
}

// SaveStepReport implements StepBackend.
func (b *DirectBackend) SaveStepReport(
	ctx context.Context, job *models.CheckJob, runUID string, report []byte,
) (string, error) {
	if b.steps == nil {
		return "", ErrNoStepStore
	}

	return b.steps.PutCrawlReport(ctx, job.OrganizationUID, job.CheckUID, report, models.JSONMap{
		attachments.DetailKeyRunUID:     runUID,
		attachments.DetailKeyCheckUID:   job.CheckUID,
		attachments.DetailKeyCapturedAt: time.Now().UTC().Format(time.RFC3339),
	})
}

// PreviousStepReport implements StepBackend.
func (b *DirectBackend) PreviousStepReport(ctx context.Context, job *models.CheckJob) ([]byte, error) {
	if b.steps == nil {
		return nil, ErrNoStepStore
	}

	return b.steps.LatestCrawlReport(ctx, job.OrganizationUID, job.CheckUID)
}
