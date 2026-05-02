// Package escalationpolicies implements the escalation policy domain:
// CRUD on policies and the steps/targets nested inside them. The runtime
// fan-out (scheduling jobs at delays, calling the on-call resolver, etc.)
// is intentionally a follow-up — this package owns the data model and API.
package escalationpolicies

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
)

// Service errors.
var (
	ErrPolicyNotFound      = errors.New("escalation policy not found")
	ErrInvalidTargetType   = errors.New("target type must be one of user|schedule|connection|all_admins")
	ErrTargetUIDRequired   = errors.New("target UID is required for user/schedule/connection targets")
	ErrTargetUIDForbidden  = errors.New("target UID must be empty for all_admins targets")
	ErrRepeatRequiresAfter = errors.New("repeat_after_minutes is required when repeat_max > 0")
	ErrRepeatMaxNegative   = errors.New("repeat_max must be >= 0")
	ErrDelayNegative       = errors.New("step delay must be >= 0")
)

// Service exposes the escalation-policy operations.
type Service struct {
	db db.Service
}

// NewService builds a service.
func NewService(dbService db.Service) *Service {
	return &Service{db: dbService}
}

// TargetInput is the request shape for one target inside a step.
type TargetInput struct {
	Type EscalationTargetType
	UID  string
}

// EscalationTargetType is re-exported as a string alias to keep the public
// API stable across model package changes.
type EscalationTargetType = models.EscalationTargetType

// StepInput is the request shape for one step.
type StepInput struct {
	DelayMinutes int
	Targets      []TargetInput
}

// CreatePolicyInput captures the create-policy request.
type CreatePolicyInput struct {
	OrganizationUID    string
	Slug               string
	Name               string
	Description        string
	RepeatMax          int
	RepeatAfterMinutes *int
	Steps              []StepInput
}

// CreatePolicy validates and persists a policy together with its steps and targets.
func (s *Service) CreatePolicy(ctx context.Context, input *CreatePolicyInput) (*models.EscalationPolicy, error) {
	if err := validatePolicyShape(input.RepeatMax, input.RepeatAfterMinutes); err != nil {
		return nil, err
	}

	for i := range input.Steps {
		if err := validateStep(&input.Steps[i]); err != nil {
			return nil, err
		}
	}

	policy := models.NewEscalationPolicy(input.OrganizationUID, input.Slug, input.Name)
	policy.RepeatMax = input.RepeatMax
	policy.RepeatAfterMinutes = input.RepeatAfterMinutes

	if input.Description != "" {
		policy.Description = &input.Description
	}

	if err := s.db.CreateEscalationPolicy(ctx, policy); err != nil {
		return nil, err
	}

	if err := s.replaceSteps(ctx, policy.UID, input.Steps); err != nil {
		return nil, err
	}

	return policy, nil
}

// GetPolicyBySlug returns a policy plus its expanded steps and targets.
func (s *Service) GetPolicyBySlug(
	ctx context.Context, orgUID, slug string,
) (*PolicyDetail, error) {
	policy, err := s.db.GetEscalationPolicyBySlug(ctx, orgUID, slug)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrPolicyNotFound
	}

	if err != nil {
		return nil, err
	}

	return s.loadDetail(ctx, policy)
}

// ListPolicies returns all policies for an org. Steps and targets are NOT
// expanded — the list response is intentionally light.
func (s *Service) ListPolicies(ctx context.Context, orgUID string) ([]*models.EscalationPolicy, error) {
	return s.db.ListEscalationPolicies(ctx, orgUID)
}

// UpdatePolicyInput captures partial update fields. Steps replace the entire
// step list when non-nil (matches the spec's PATCH semantics).
type UpdatePolicyInput struct {
	Slug               *string
	Name               *string
	Description        *string
	RepeatMax          *int
	RepeatAfterMinutes *int
	Steps              *[]StepInput

	ClearDescription        bool
	ClearRepeatAfterMinutes bool
}

// UpdatePolicy applies a partial update.
func (s *Service) UpdatePolicy(
	ctx context.Context, orgUID, slug string, input *UpdatePolicyInput,
) (*PolicyDetail, error) {
	policy, err := s.db.GetEscalationPolicyBySlug(ctx, orgUID, slug)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrPolicyNotFound
	}

	if err != nil {
		return nil, err
	}

	repeatMax := policy.RepeatMax
	if input.RepeatMax != nil {
		repeatMax = *input.RepeatMax
	}

	repeatAfter := policy.RepeatAfterMinutes
	if input.RepeatAfterMinutes != nil {
		repeatAfter = input.RepeatAfterMinutes
	}

	if input.ClearRepeatAfterMinutes {
		repeatAfter = nil
	}

	if shapeErr := validatePolicyShape(repeatMax, repeatAfter); shapeErr != nil {
		return nil, shapeErr
	}

	if input.Steps != nil {
		for i := range *input.Steps {
			step := &(*input.Steps)[i]
			if stepErr := validateStep(step); stepErr != nil {
				return nil, stepErr
			}
		}
	}

	update := &models.EscalationPolicyUpdate{
		Slug:                    input.Slug,
		Name:                    input.Name,
		Description:             input.Description,
		RepeatMax:               input.RepeatMax,
		RepeatAfterMinutes:      input.RepeatAfterMinutes,
		ClearDescription:        input.ClearDescription,
		ClearRepeatAfterMinutes: input.ClearRepeatAfterMinutes,
	}

	if updErr := s.db.UpdateEscalationPolicy(ctx, policy.UID, update); updErr != nil {
		return nil, updErr
	}

	if input.Steps != nil {
		if replaceErr := s.replaceSteps(ctx, policy.UID, *input.Steps); replaceErr != nil {
			return nil, replaceErr
		}
	}

	refreshed, err := s.db.GetEscalationPolicy(ctx, orgUID, policy.UID)
	if err != nil {
		return nil, err
	}

	return s.loadDetail(ctx, refreshed)
}

// DeletePolicy soft-deletes the policy.
func (s *Service) DeletePolicy(ctx context.Context, orgUID, slug string) error {
	policy, err := s.db.GetEscalationPolicyBySlug(ctx, orgUID, slug)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrPolicyNotFound
	}

	if err != nil {
		return err
	}

	return s.db.DeleteEscalationPolicy(ctx, policy.UID)
}

// PolicyDetail is the expanded view returned by GET endpoints.
type PolicyDetail struct {
	Policy *models.EscalationPolicy
	Steps  []*StepDetail
}

// StepDetail is one step plus its targets.
type StepDetail struct {
	Step    *models.EscalationPolicyStep
	Targets []*models.EscalationPolicyTarget
}

func (s *Service) loadDetail(ctx context.Context, policy *models.EscalationPolicy) (*PolicyDetail, error) {
	steps, err := s.db.ListEscalationPolicySteps(ctx, policy.UID)
	if err != nil {
		return nil, err
	}

	stepUIDs := make([]string, 0, len(steps))
	for _, step := range steps {
		stepUIDs = append(stepUIDs, step.UID)
	}

	targets, err := s.db.ListEscalationPolicyTargets(ctx, stepUIDs)
	if err != nil {
		return nil, err
	}

	byStep := make(map[string][]*models.EscalationPolicyTarget, len(steps))
	for _, target := range targets {
		byStep[target.StepUID] = append(byStep[target.StepUID], target)
	}

	details := make([]*StepDetail, 0, len(steps))
	for _, step := range steps {
		details = append(details, &StepDetail{Step: step, Targets: byStep[step.UID]})
	}

	return &PolicyDetail{Policy: policy, Steps: details}, nil
}

func (s *Service) replaceSteps(ctx context.Context, policyUID string, inputs []StepInput) error {
	steps := make([]*models.EscalationPolicyStep, 0, len(inputs))
	targetsByIdx := make(map[int][]*models.EscalationPolicyTarget, len(inputs))

	for idx := range inputs {
		input := &inputs[idx]
		step := models.NewEscalationPolicyStep(policyUID, idx, input.DelayMinutes)
		steps = append(steps, step)

		targets := make([]*models.EscalationPolicyTarget, 0, len(input.Targets))
		for pos := range input.Targets {
			target := &input.Targets[pos]

			var uid *string
			if target.UID != "" {
				value := target.UID
				uid = &value
			}

			targets = append(targets, models.NewEscalationPolicyTarget(step.UID, target.Type, uid, pos))
		}

		targetsByIdx[idx] = targets
	}

	return s.db.ReplaceEscalationPolicySteps(ctx, policyUID, steps, targetsByIdx)
}

func validatePolicyShape(repeatMax int, repeatAfter *int) error {
	if repeatMax < 0 {
		return ErrRepeatMaxNegative
	}

	if repeatMax > 0 && repeatAfter == nil {
		return ErrRepeatRequiresAfter
	}

	return nil
}

func validateStep(step *StepInput) error {
	if step.DelayMinutes < 0 {
		return ErrDelayNegative
	}

	for i := range step.Targets {
		if err := validateTarget(&step.Targets[i]); err != nil {
			return err
		}
	}

	return nil
}

func validateTarget(target *TargetInput) error {
	switch target.Type {
	case models.EscalationTargetUser, models.EscalationTargetSchedule, models.EscalationTargetConnection:
		if target.UID == "" {
			return ErrTargetUIDRequired
		}
	case models.EscalationTargetAllAdmins:
		if target.UID != "" {
			return ErrTargetUIDForbidden
		}
	default:
		return fmt.Errorf("%w: %q", ErrInvalidTargetType, target.Type)
	}

	return nil
}
