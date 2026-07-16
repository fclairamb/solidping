package sqlite

import (
	"context"
	"fmt"
	"time"

	"github.com/uptrace/bun"

	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
)

// CreateAgentEnrollmentToken persists a one-shot enrollment token.
func (s *Service) CreateAgentEnrollmentToken(ctx context.Context, token *models.AgentEnrollmentToken) error {
	if _, err := s.db.NewInsert().Model(token).Exec(ctx); err != nil {
		return fmt.Errorf("failed to create agent enrollment token: %w", err)
	}

	return nil
}

// ListAgentEnrollmentTokens lists an org's live (unused, unexpired) tokens.
func (s *Service) ListAgentEnrollmentTokens(
	ctx context.Context, orgUID string,
) ([]*models.AgentEnrollmentToken, error) {
	var tokens []*models.AgentEnrollmentToken

	err := s.db.NewSelect().
		Model(&tokens).
		Where("organization_uid = ?", orgUID).
		Where("deleted_at IS NULL").
		Where("used_at IS NULL").
		Where("expires_at > ?", time.Now()).
		Order("created_at DESC").
		Scan(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to list agent enrollment tokens: %w", err)
	}

	return tokens, nil
}

// DeleteAgentEnrollmentToken soft-deletes an enrollment token.
func (s *Service) DeleteAgentEnrollmentToken(ctx context.Context, orgUID, uid string) error {
	_, err := s.db.NewUpdate().
		Model((*models.AgentEnrollmentToken)(nil)).
		Set("deleted_at = ?", time.Now()).
		Where("uid = ?", uid).
		Where("organization_uid = ?", orgUID).
		Where("deleted_at IS NULL").
		Exec(ctx)
	if err != nil {
		return fmt.Errorf("failed to delete agent enrollment token: %w", err)
	}

	return nil
}

// GetAgentEnrollmentTokenByHash returns the live token with the given hash.
func (s *Service) GetAgentEnrollmentTokenByHash(
	ctx context.Context, tokenHash string,
) (*models.AgentEnrollmentToken, error) {
	var token models.AgentEnrollmentToken

	err := s.db.NewSelect().
		Model(&token).
		Where("token_hash = ?", tokenHash).
		Where("deleted_at IS NULL").
		Where("used_at IS NULL").
		Where("expires_at > ?", time.Now()).
		Scan(ctx)
	if err != nil {
		return nil, db.ErrEnrollmentTokenInvalid
	}

	return &token, nil
}

// EnrollAgent atomically consumes a valid enrollment token and creates the bound
// agent row. The single-use guard is the conditional UPDATE (`used_at IS NULL`):
// under a concurrent double-enroll only one UPDATE affects a row, the loser gets
// zero rows and rolls back with ErrEnrollmentTokenInvalid.
func (s *Service) EnrollAgent(
	ctx context.Context, tokenHash, name, ed25519Pub, x25519Pub, fingerprint string,
) (*models.Agent, error) {
	var agent *models.Agent

	err := s.db.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		now := time.Now()

		var token models.AgentEnrollmentToken

		err := tx.NewSelect().
			Model(&token).
			Where("token_hash = ?", tokenHash).
			Where("deleted_at IS NULL").
			Where("used_at IS NULL").
			Where("expires_at > ?", now).
			Scan(ctx)
		if err != nil {
			return db.ErrEnrollmentTokenInvalid
		}

		newAgent := models.NewAgent(token.OrganizationUID, token.Region, name, ed25519Pub, x25519Pub, fingerprint)

		res, err := tx.NewUpdate().
			Model((*models.AgentEnrollmentToken)(nil)).
			Set("used_at = ?", now).
			Set("used_by_agent_uid = ?", newAgent.UID).
			Where("uid = ?", token.UID).
			Where("used_at IS NULL").
			Exec(ctx)
		if err != nil {
			return fmt.Errorf("failed to consume enrollment token: %w", err)
		}

		affected, err := res.RowsAffected()
		if err != nil {
			return fmt.Errorf("failed to read rows affected: %w", err)
		}

		if affected != 1 {
			return db.ErrEnrollmentTokenInvalid
		}

		if _, err := tx.NewInsert().Model(newAgent).Exec(ctx); err != nil {
			return fmt.Errorf("failed to create agent: %w", err)
		}

		agent = newAgent

		return nil
	})
	if err != nil {
		return nil, err
	}

	return agent, nil
}

// GetAgent returns an agent by UID.
func (s *Service) GetAgent(ctx context.Context, uid string) (*models.Agent, error) {
	var agent models.Agent

	err := s.db.NewSelect().
		Model(&agent).
		Where("uid = ?", uid).
		Where("deleted_at IS NULL").
		Scan(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get agent: %w", err)
	}

	return &agent, nil
}

// ListAgents lists an org's agents (active and revoked, not deleted).
func (s *Service) ListAgents(ctx context.Context, orgUID string) ([]*models.Agent, error) {
	var agents []*models.Agent

	err := s.db.NewSelect().
		Model(&agents).
		Where("organization_uid = ?", orgUID).
		Where("deleted_at IS NULL").
		Order("enrolled_at DESC").
		Scan(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to list agents: %w", err)
	}

	return agents, nil
}

// ListActiveAgentsByRegion returns the active agents bound to a private region.
func (s *Service) ListActiveAgentsByRegion(
	ctx context.Context, orgUID, region string,
) ([]*models.Agent, error) {
	var agents []*models.Agent

	err := s.db.NewSelect().
		Model(&agents).
		Where("organization_uid = ?", orgUID).
		Where("region = ?", region).
		Where("status = ?", models.AgentStatusActive).
		Where("deleted_at IS NULL").
		Order("enrolled_at ASC").
		Scan(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to list active agents by region: %w", err)
	}

	return agents, nil
}

// UpdateAgentLastSeen sets an agent's last_seen_at.
func (s *Service) UpdateAgentLastSeen(ctx context.Context, uid string, at time.Time) error {
	_, err := s.db.NewUpdate().
		Model((*models.Agent)(nil)).
		Set("last_seen_at = ?", at).
		Set("updated_at = ?", at).
		Where("uid = ?", uid).
		Where("deleted_at IS NULL").
		Exec(ctx)
	if err != nil {
		return fmt.Errorf("failed to update agent last_seen_at: %w", err)
	}

	return nil
}

// RevokeAgent marks an agent revoked.
func (s *Service) RevokeAgent(ctx context.Context, orgUID, uid string) error {
	now := time.Now()

	_, err := s.db.NewUpdate().
		Model((*models.Agent)(nil)).
		Set("status = ?", models.AgentStatusRevoked).
		Set("revoked_at = ?", now).
		Set("updated_at = ?", now).
		Where("uid = ?", uid).
		Where("organization_uid = ?", orgUID).
		Where("deleted_at IS NULL").
		Exec(ctx)
	if err != nil {
		return fmt.Errorf("failed to revoke agent: %w", err)
	}

	return nil
}

// GetCheckJob returns one check job by UID.
func (s *Service) GetCheckJob(ctx context.Context, uid string) (*models.CheckJob, error) {
	var job models.CheckJob

	err := s.db.NewSelect().
		Model(&job).
		Where("uid = ?", uid).
		Scan(ctx)
	if err != nil {
		return nil, err
	}

	return &job, nil
}
