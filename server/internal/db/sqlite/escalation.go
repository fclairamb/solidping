package sqlite

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
	// Match the Postgres backend: a non-UUID identifier can never name a
	// policy (uids are UUIDs), so short-circuit to ErrNoRows → 404.
	if _, err := uuid.Parse(policyUID); err != nil {
		return nil, sql.ErrNoRows
	}

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

// policyCountRow is the shared scan target for the grouped count queries below.
type policyCountRow struct {
	PolicyUID string `bun:"policy_uid"`
	Count     int    `bun:"count"`
}

func rowsToCountMap(rows []policyCountRow) map[string]int {
	out := make(map[string]int, len(rows))
	for _, r := range rows {
		out[r.PolicyUID] = r.Count
	}

	return out
}

// CountEscalationPolicyStepsByPolicy returns step counts keyed by policy UID.
func (s *Service) CountEscalationPolicyStepsByPolicy(
	ctx context.Context, policyUIDs []string,
) (map[string]int, error) {
	if len(policyUIDs) == 0 {
		return map[string]int{}, nil
	}

	var rows []policyCountRow

	err := s.db.NewSelect().
		Model((*models.EscalationPolicyStep)(nil)).
		ColumnExpr("policy_uid").
		ColumnExpr("COUNT(*) AS count").
		Where("policy_uid IN (?)", bun.List(policyUIDs)).
		GroupExpr("policy_uid").
		Scan(ctx, &rows)
	if err != nil {
		return nil, fmt.Errorf("count escalation policy steps: %w", err)
	}

	return rowsToCountMap(rows), nil
}

// CountChecksByEscalationPolicy counts live checks per directly-referenced policy.
func (s *Service) CountChecksByEscalationPolicy(
	ctx context.Context, orgUID string,
) (map[string]int, error) {
	var rows []policyCountRow

	err := s.db.NewSelect().
		Model((*models.Check)(nil)).
		ColumnExpr("escalation_policy_uid AS policy_uid").
		ColumnExpr("COUNT(*) AS count").
		Where("organization_uid = ?", orgUID).
		Where("escalation_policy_uid IS NOT NULL").
		Where("deleted_at IS NULL").
		GroupExpr("escalation_policy_uid").
		Scan(ctx, &rows)
	if err != nil {
		return nil, fmt.Errorf("count checks by escalation policy: %w", err)
	}

	return rowsToCountMap(rows), nil
}

// CountCheckGroupsByEscalationPolicy counts live groups per referenced policy.
func (s *Service) CountCheckGroupsByEscalationPolicy(
	ctx context.Context, orgUID string,
) (map[string]int, error) {
	var rows []policyCountRow

	err := s.db.NewSelect().
		Model((*models.CheckGroup)(nil)).
		ColumnExpr("escalation_policy_uid AS policy_uid").
		ColumnExpr("COUNT(*) AS count").
		Where("organization_uid = ?", orgUID).
		Where("escalation_policy_uid IS NOT NULL").
		Where("deleted_at IS NULL").
		GroupExpr("escalation_policy_uid").
		Scan(ctx, &rows)
	if err != nil {
		return nil, fmt.Errorf("count check groups by escalation policy: %w", err)
	}

	return rowsToCountMap(rows), nil
}

// CountChecksInheritingOrgDefault counts live checks that resolve to no policy
// of their own — no direct policy, and either no group or a group whose own
// policy is null. This is the blast radius of the org default.
func (s *Service) CountChecksInheritingOrgDefault(
	ctx context.Context, orgUID string,
) (int, error) {
	count, err := s.db.NewSelect().
		Model((*models.Check)(nil)).
		ModelTableExpr("checks AS c").
		Where("c.organization_uid = ?", orgUID).
		Where("c.deleted_at IS NULL").
		Where("c.escalation_policy_uid IS NULL").
		Where(`(c.check_group_uid IS NULL OR NOT EXISTS (
			SELECT 1 FROM check_groups g
			WHERE g.uid = c.check_group_uid
			AND g.deleted_at IS NULL
			AND g.escalation_policy_uid IS NOT NULL))`).
		Count(ctx)
	if err != nil {
		return 0, fmt.Errorf("count checks inheriting org default: %w", err)
	}

	return count, nil
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
