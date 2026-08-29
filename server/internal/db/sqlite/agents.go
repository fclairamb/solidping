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

// ListAgentEnrollmentTokens lists an org's live (unused, unexpired) tokens,
// plus recently-used ones (db.UsedEnrollmentTokenListWindow) so the UI can
// report a consumed token's outcome. View-only: enrollment still requires an
// unused token.
func (s *Service) ListAgentEnrollmentTokens(
	ctx context.Context, orgUID string,
) ([]*models.AgentEnrollmentToken, error) {
	var tokens []*models.AgentEnrollmentToken

	now := time.Now()

	err := s.db.NewSelect().
		Model(&tokens).
		Where("organization_uid = ?", orgUID).
		Where("deleted_at IS NULL").
		Where("(used_at IS NULL AND expires_at > ?) OR used_at > ?",
			now, now.Add(-db.UsedEnrollmentTokenListWindow)).
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
// Kind-aware validity: an org token must still be unused (strictly one-shot),
// while a system token stays valid for as long as its use budget allows
// (max_uses NULL = unlimited) so a whole fly fleet can enroll on boot.
func (s *Service) GetAgentEnrollmentTokenByHash(
	ctx context.Context, tokenHash string,
) (*models.AgentEnrollmentToken, error) {
	var token models.AgentEnrollmentToken

	err := s.db.NewSelect().
		Model(&token).
		Where("token_hash = ?", tokenHash).
		Where("deleted_at IS NULL").
		Where("expires_at > ?", time.Now()).
		WhereGroup(" AND ", enrollmentTokenUsableScope).
		Scan(ctx)
	if err != nil {
		return nil, db.ErrEnrollmentTokenInvalid
	}

	return &token, nil
}

// enrollmentTokenUsableScope is the "this token may still enroll an agent"
// predicate, shared by the non-consuming lookup and the enrollment transaction
// so the two can never drift.
func enrollmentTokenUsableScope(q *bun.SelectQuery) *bun.SelectQuery {
	return q.
		WhereOr("kind = ? AND used_at IS NULL", models.AgentKindOrg).
		WhereOr("kind = ? AND (max_uses IS NULL OR use_count < max_uses)", models.AgentKindSystem)
}

// EnrollAgent consumes a valid enrollment token and creates the bound agent row.
//
// Org tokens keep their exact historical semantics: the single-use guard is the
// conditional UPDATE (`used_at IS NULL`), so under a concurrent double-enroll
// only one UPDATE affects a row and the loser rolls back with
// ErrEnrollmentTokenInvalid. System tokens are multi-use: the same UPDATE
// increments use_count under an `(max_uses IS NULL OR use_count < max_uses)`
// guard, which is equally race-safe (the loser of a budget-exhausting race sees
// zero rows) while letting every machine of a fleet enroll with its own keypair.
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
			Where("expires_at > ?", now).
			WhereGroup(" AND ", enrollmentTokenUsableScope).
			Scan(ctx)
		if err != nil {
			return db.ErrEnrollmentTokenInvalid
		}

		newAgent := agentForToken(&token, name, ed25519Pub, x25519Pub, fingerprint)

		update := tx.NewUpdate().
			Model((*models.AgentEnrollmentToken)(nil)).
			Set("used_at = ?", now).
			Set("used_by_agent_uid = ?", newAgent.UID).
			Set("use_count = use_count + 1").
			Where("uid = ?", token.UID)

		if token.IsSystem() {
			update = update.Where("max_uses IS NULL OR use_count < max_uses")
		} else {
			update = update.Where("used_at IS NULL")
		}

		res, err := update.Exec(ctx)
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

		// A system agent that reappears with the same (region, name) is a
		// machine replacement, not a fleet peer: retire the row it replaces
		// here rather than leaving it to the agent_gc job's 7-day silence
		// window. Inside the transaction on purpose — the newcomer's row and
		// the retirement of its predecessors must commit together.
		if _, err := db.SupersedeReplacedSystemAgents(ctx, tx, newAgent, now); err != nil {
			return err
		}

		agent = newAgent

		return nil
	})
	if err != nil {
		return nil, err
	}

	return agent, nil
}

// agentForToken builds the agent row a token enrolls: org-bound for an org
// token, org-less (region-scoped) for a system token.
func agentForToken(
	token *models.AgentEnrollmentToken, name, ed25519Pub, x25519Pub, fingerprint string,
) *models.Agent {
	if token.IsSystem() {
		return models.NewSystemAgent(token.Region, name, ed25519Pub, x25519Pub, fingerprint)
	}

	return models.NewAgent(token.OrgUID(), token.Region, name, ed25519Pub, x25519Pub, fingerprint)
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

// ListAllAgents lists every non-deleted agent across all organizations, both
// org and system kind (active and revoked), for the fleet-wide operator view.
// Ordered by kind, region, name for a stable, grouped listing.
func (s *Service) ListAllAgents(ctx context.Context) ([]*models.Agent, error) {
	var agents []*models.Agent

	err := s.db.NewSelect().
		Model(&agents).
		Where("deleted_at IS NULL").
		Order("kind ASC", "region ASC", "name ASC").
		Scan(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to list all agents: %w", err)
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

// ListPurgeableRevokedAgents returns live agents (any kind) whose revocation
// is older than cutoff — a revoked agent is dead by admin decision, and
// nobody is waiting for it to come back. The clock is revoked_at, not
// last_seen_at (revoking a still-connected agent must not reset it, and an
// agent revoked before it ever connected has no last_seen_at at all). A
// legacy/inconsistent revoked row with a NULL revoked_at falls back to
// updated_at so it is still eventually collected instead of becoming
// permanently immortal.
func (s *Service) ListPurgeableRevokedAgents(ctx context.Context, cutoff time.Time) ([]*models.Agent, error) {
	var agents []*models.Agent

	err := s.db.NewSelect().
		Model(&agents).
		Where("status = ?", models.AgentStatusRevoked).
		Where("deleted_at IS NULL").
		Where("COALESCE(revoked_at, updated_at) < ?", cutoff).
		Order("enrolled_at ASC").
		Scan(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to list purgeable revoked agents: %w", err)
	}

	return agents, nil
}

// PurgeAgent soft-deletes a revoked agent, clearing it from every listing.
// Scoped to status='revoked' so it can never touch a live agent — an admin
// purging an already-revoked row through the API, or the agent_gc sweep
// collecting one past the retention window.
func (s *Service) PurgeAgent(ctx context.Context, uid string) error {
	now := time.Now()

	_, err := s.db.NewUpdate().
		Model((*models.Agent)(nil)).
		Set("deleted_at = ?", now).
		Set("updated_at = ?", now).
		Where("uid = ?", uid).
		Where("status = ?", models.AgentStatusRevoked).
		Where("deleted_at IS NULL").
		Exec(ctx)
	if err != nil {
		return fmt.Errorf("failed to purge agent: %w", err)
	}

	return nil
}

// UpsertSystemAgentEnrollmentToken inserts a seeded platform token, or refreshes
// the existing row with that hash (expiry, region, use budget) and un-deletes it.
// Idempotent: SP_SYSTEM_AGENT_ENROLLMENT_TOKENS is re-applied on every boot.
func (s *Service) UpsertSystemAgentEnrollmentToken(
	ctx context.Context, token *models.AgentEnrollmentToken,
) error {
	res, err := s.db.NewUpdate().
		Model((*models.AgentEnrollmentToken)(nil)).
		Set("region = ?", token.Region).
		Set("expires_at = ?", token.ExpiresAt).
		Set("max_uses = ?", token.MaxUses).
		Set("deleted_at = NULL").
		Where("token_hash = ?", token.TokenHash).
		Where("kind = ?", models.AgentKindSystem).
		Exec(ctx)
	if err != nil {
		return fmt.Errorf("failed to refresh system enrollment token: %w", err)
	}

	affected, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("failed to read rows affected: %w", err)
	}

	if affected > 0 {
		return nil
	}

	if _, err := s.db.NewInsert().Model(token).Exec(ctx); err != nil {
		return fmt.Errorf("failed to create system enrollment token: %w", err)
	}

	return nil
}

// ListSystemAgentEnrollmentTokens returns every live platform token. Never
// exposed on the org-admin API — system tokens are operator material.
func (s *Service) ListSystemAgentEnrollmentTokens(
	ctx context.Context,
) ([]*models.AgentEnrollmentToken, error) {
	var tokens []*models.AgentEnrollmentToken

	err := s.db.NewSelect().
		Model(&tokens).
		Where("kind = ?", models.AgentKindSystem).
		Where("deleted_at IS NULL").
		Order("created_at DESC").
		Scan(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to list system enrollment tokens: %w", err)
	}

	return tokens, nil
}

// RevokeSystemAgentEnrollmentTokensExcept soft-deletes every live system token
// whose hash is not in keepHashes. Dropping a token from the environment is the
// revocation path: the next boot removes it. An empty keepHashes revokes all of
// them.
func (s *Service) RevokeSystemAgentEnrollmentTokensExcept(
	ctx context.Context, keepHashes []string,
) (int64, error) {
	query := s.db.NewUpdate().
		Model((*models.AgentEnrollmentToken)(nil)).
		Set("deleted_at = ?", time.Now()).
		Where("kind = ?", models.AgentKindSystem).
		Where("deleted_at IS NULL")

	if len(keepHashes) > 0 {
		query = query.Where("token_hash NOT IN (?)", bun.List(keepHashes))
	}

	res, err := query.Exec(ctx)
	if err != nil {
		return 0, fmt.Errorf("failed to revoke system enrollment tokens: %w", err)
	}

	affected, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("failed to read rows affected: %w", err)
	}

	return affected, nil
}

// ListStaleSystemAgents returns live system agents last seen before cutoff (an
// agent that never connected is judged on enrolled_at). Org agents are
// user-managed and deliberately excluded — see the agent_gc job.
func (s *Service) ListStaleSystemAgents(ctx context.Context, cutoff time.Time) ([]*models.Agent, error) {
	var agents []*models.Agent

	err := s.db.NewSelect().
		Model(&agents).
		Where("kind = ?", models.AgentKindSystem).
		Where("deleted_at IS NULL").
		Where("(last_seen_at IS NOT NULL AND last_seen_at < ?) OR (last_seen_at IS NULL AND enrolled_at < ?)",
			cutoff, cutoff).
		Order("enrolled_at ASC").
		Scan(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to list stale system agents: %w", err)
	}

	return agents, nil
}

// RetireSystemAgent revokes and soft-deletes one system agent (enroll-on-boot
// fleets churn rows; a retired machine's row must stop being a seal recipient
// and stop cluttering the fleet list). Scoped to kind='system' so it can never
// touch a customer-managed agent.
func (s *Service) RetireSystemAgent(ctx context.Context, uid string) error {
	now := time.Now()

	_, err := s.db.NewUpdate().
		Model((*models.Agent)(nil)).
		Set("status = ?", models.AgentStatusRevoked).
		Set("revoked_at = ?", now).
		Set("deleted_at = ?", now).
		Set("updated_at = ?", now).
		Where("uid = ?", uid).
		Where("kind = ?", models.AgentKindSystem).
		Where("deleted_at IS NULL").
		Exec(ctx)
	if err != nil {
		return fmt.Errorf("failed to retire system agent: %w", err)
	}

	return nil
}

// RetireAgentWorkerRow soft-deletes the workers row an agent's connection
// registered, resolved through the deterministic agents.WorkerSlug(uid). The
// agent_gc job and the supersede-on-enroll path share this one implementation.
func (s *Service) RetireAgentWorkerRow(ctx context.Context, agentUID string) error {
	return db.RetireAgentWorkerRows(ctx, s.db, []string{agentUID}, time.Now())
}

// CheckAndStoreAgentNonce records (agentUID, nonce) as consumed, returning
// db.ErrAgentNonceReplayed when the pair was already seen inside the retention
// window. This is the cluster-wide replay guard for reconnect signatures: the
// stale rows of this agent are pruned first, then the insert either wins or
// conflicts with a live row (which is exactly a replay).
func (s *Service) CheckAndStoreAgentNonce(
	ctx context.Context, agentUID, nonce string, now time.Time, retain time.Duration,
) error {
	if _, err := s.db.NewDelete().
		Model((*models.AgentNonce)(nil)).
		Where("agent_uid = ?", agentUID).
		Where("seen_at < ?", now.Add(-retain)).
		Exec(ctx); err != nil {
		return fmt.Errorf("failed to prune agent nonces: %w", err)
	}

	res, err := s.db.NewInsert().
		Model(&models.AgentNonce{AgentUID: agentUID, Nonce: nonce, SeenAt: now}).
		On("CONFLICT (agent_uid, nonce) DO NOTHING").
		Exec(ctx)
	if err != nil {
		return fmt.Errorf("failed to store agent nonce: %w", err)
	}

	affected, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("failed to read rows affected: %w", err)
	}

	if affected == 0 {
		return db.ErrAgentNonceReplayed
	}

	return nil
}

// PruneAgentNonces deletes consumed nonces older than cutoff (the agent_gc job
// sweeps rows left behind by agents that never reconnected).
func (s *Service) PruneAgentNonces(ctx context.Context, cutoff time.Time) (int64, error) {
	res, err := s.db.NewDelete().
		Model((*models.AgentNonce)(nil)).
		Where("seen_at < ?", cutoff).
		Exec(ctx)
	if err != nil {
		return 0, fmt.Errorf("failed to prune agent nonces: %w", err)
	}

	affected, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("failed to read rows affected: %w", err)
	}

	return affected, nil
}

// GetCheckJobByUID returns one check job by UID.
func (s *Service) GetCheckJobByUID(ctx context.Context, uid string) (*models.CheckJob, error) {
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
