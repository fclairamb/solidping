package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/uptrace/bun"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

// CreateEscalationPolicy inserts a new policy header row.
func (s *Service) CreateEscalationPolicy(ctx context.Context, policy *models.EscalationPolicy) error {
	_, err := s.db.NewInsert().Model(policy).Exec(ctx)
	if err != nil {
		return fmt.Errorf("create escalation policy: %w", err)
	}

	return nil
}

// GetEscalationPolicy fetches a policy by UID. When orgUID is non-empty
// the lookup is scoped to that organization (the normal CRUD path); an
// empty orgUID returns the policy regardless of org (used by the
// escalation runtime which only knows the policy UID).
func (s *Service) GetEscalationPolicy(
	ctx context.Context, orgUID, policyUID string,
) (*models.EscalationPolicy, error) {
	var policy models.EscalationPolicy

	query := s.db.NewSelect().
		Model(&policy).
		Where("uid = ?", policyUID).
		Where("deleted_at IS NULL")

	if orgUID != "" {
		query = query.Where("organization_uid = ?", orgUID)
	}

	if err := query.Scan(ctx); err != nil {
		return nil, fmt.Errorf("get escalation policy: %w", err)
	}

	return &policy, nil
}

// GetEscalationPolicyBySlug fetches by (org, slug).
func (s *Service) GetEscalationPolicyBySlug(
	ctx context.Context, orgUID, slug string,
) (*models.EscalationPolicy, error) {
	var policy models.EscalationPolicy

	err := s.db.NewSelect().
		Model(&policy).
		Where("organization_uid = ?", orgUID).
		Where("slug = ?", slug).
		Where("deleted_at IS NULL").
		Scan(ctx)
	if err != nil {
		return nil, fmt.Errorf("get escalation policy by slug: %w", err)
	}

	return &policy, nil
}

// GetEscalationPolicyByUidOrSlug fetches by (org, identifier) where the
// identifier is a UID when it parses as a UUID, otherwise a slug. Mirrors
// the resolution used by checks and status pages so the same path param can
// address a policy by uid or slug.
//
//nolint:revive // ByUidOrSlug matches the db.Service interface naming
func (s *Service) GetEscalationPolicyByUidOrSlug(
	ctx context.Context, orgUID, identifier string,
) (*models.EscalationPolicy, error) {
	var policy models.EscalationPolicy

	query := s.db.NewSelect().
		Model(&policy).
		Where("organization_uid = ?", orgUID).
		Where("deleted_at IS NULL")

	if _, err := uuid.Parse(identifier); err == nil {
		query = query.Where("uid = ?", identifier)
	} else {
		query = query.Where("slug = ?", identifier)
	}

	if err := query.Scan(ctx); err != nil {
		return nil, fmt.Errorf("get escalation policy by uid or slug: %w", err)
	}

	return &policy, nil
}

// ListEscalationPolicies returns all policies for an org, ordered by name.
func (s *Service) ListEscalationPolicies(
	ctx context.Context, orgUID string,
) ([]*models.EscalationPolicy, error) {
	var policies []*models.EscalationPolicy

	err := s.db.NewSelect().
		Model(&policies).
		Where("organization_uid = ?", orgUID).
		Where("deleted_at IS NULL").
		Order("name ASC").
		Scan(ctx)
	if err != nil {
		return nil, fmt.Errorf("list escalation policies: %w", err)
	}

	return policies, nil
}

// UpdateEscalationPolicy writes the supplied fields. Empty update is a no-op.
func (s *Service) UpdateEscalationPolicy(
	ctx context.Context, policyUID string, update *models.EscalationPolicyUpdate,
) error {
	query := s.db.NewUpdate().
		Model((*models.EscalationPolicy)(nil)).
		Where("uid = ?", policyUID).
		Where("deleted_at IS NULL").
		Set("updated_at = ?", time.Now())

	if update.Slug != nil {
		query = query.Set("slug = ?", *update.Slug)
	}

	if update.Name != nil {
		query = query.Set("name = ?", *update.Name)
	}

	if update.Description != nil {
		query = query.Set("description = ?", *update.Description)
	}

	if update.RepeatMax != nil {
		query = query.Set("repeat_max = ?", *update.RepeatMax)
	}

	if update.RepeatAfterSeconds != nil {
		query = query.Set("repeat_after_seconds = ?", *update.RepeatAfterSeconds)
	}

	if update.ClearDescription {
		query = query.Set("description = NULL")
	}

	if update.ClearRepeatAfterSeconds {
		query = query.Set("repeat_after_seconds = NULL")
	}

	_, err := query.Exec(ctx)
	if err != nil {
		return fmt.Errorf("update escalation policy: %w", err)
	}

	return nil
}

// DeleteEscalationPolicy soft-deletes the policy.
func (s *Service) DeleteEscalationPolicy(ctx context.Context, policyUID string) error {
	now := time.Now()

	_, err := s.db.NewUpdate().
		Model((*models.EscalationPolicy)(nil)).
		Set("deleted_at = ?", now).
		Set("updated_at = ?", now).
		Where("uid = ?", policyUID).
		Where("deleted_at IS NULL").
		Exec(ctx)
	if err != nil {
		return fmt.Errorf("delete escalation policy: %w", err)
	}

	return nil
}

// GetEscalationPolicyStep loads a single step by UID. Used by the
// escalation runtime when a job fires and only knows its step UID.
func (s *Service) GetEscalationPolicyStep(
	ctx context.Context, stepUID string,
) (*models.EscalationPolicyStep, error) {
	step := new(models.EscalationPolicyStep)

	err := s.db.NewSelect().
		Model(step).
		Where("uid = ?", stepUID).
		Scan(ctx)
	if err != nil {
		return nil, fmt.Errorf("get escalation policy step: %w", err)
	}

	return step, nil
}

// ListEscalationPolicySteps returns the steps of a policy ordered by position.
func (s *Service) ListEscalationPolicySteps(
	ctx context.Context, policyUID string,
) ([]*models.EscalationPolicyStep, error) {
	var steps []*models.EscalationPolicyStep

	err := s.db.NewSelect().
		Model(&steps).
		Where("policy_uid = ?", policyUID).
		Order("position ASC").
		Scan(ctx)
	if err != nil {
		return nil, fmt.Errorf("list escalation policy steps: %w", err)
	}

	return steps, nil
}

// ReplaceEscalationPolicySteps atomically rewrites the entire step list and
// the targets attached to each step. Inputs are constructed by the service
// layer with fresh UIDs.
func (s *Service) ReplaceEscalationPolicySteps(
	ctx context.Context,
	policyUID string,
	steps []*models.EscalationPolicyStep,
	targetsByStepIdx map[int][]*models.EscalationPolicyTarget,
) error {
	return s.db.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		if _, err := tx.NewDelete().
			Model((*models.EscalationPolicyStep)(nil)).
			Where("policy_uid = ?", policyUID).
			Exec(ctx); err != nil {
			return fmt.Errorf("clear steps: %w", err)
		}

		if len(steps) == 0 {
			return nil
		}

		if _, err := tx.NewInsert().Model(&steps).Exec(ctx); err != nil {
			return fmt.Errorf("insert steps: %w", err)
		}

		var allTargets []*models.EscalationPolicyTarget
		for idx, targets := range targetsByStepIdx {
			if idx < 0 || idx >= len(steps) {
				continue
			}

			for _, target := range targets {
				target.StepUID = steps[idx].UID

				allTargets = append(allTargets, target)
			}
		}

		if len(allTargets) == 0 {
			return nil
		}

		if _, err := tx.NewInsert().Model(&allTargets).Exec(ctx); err != nil {
			return fmt.Errorf("insert targets: %w", err)
		}

		return nil
	})
}

// ListEscalationPolicyTargets returns the targets attached to any of the
// given step UIDs. Empty input yields nil.
func (s *Service) ListEscalationPolicyTargets(
	ctx context.Context, stepUIDs []string,
) ([]*models.EscalationPolicyTarget, error) {
	if len(stepUIDs) == 0 {
		return nil, nil
	}

	var targets []*models.EscalationPolicyTarget

	err := s.db.NewSelect().
		Model(&targets).
		Where("step_uid IN (?)", bun.List(stepUIDs)).
		Order("step_uid ASC").
		Order("position ASC").
		Scan(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}

	if err != nil {
		return nil, fmt.Errorf("list escalation policy targets: %w", err)
	}

	return targets, nil
}
