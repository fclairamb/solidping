package db

import (
	"context"
	"fmt"
	"time"

	"github.com/uptrace/bun"

	agentcrypto "github.com/fclairamb/solidping/server/internal/agents"
	"github.com/fclairamb/solidping/server/internal/db/models"
)

// SupersededSystemAgentDisconnectWindow is how long a same-named system agent
// must have been silent before a newcomer is allowed to supersede it.
//
// It is the cross-replica proxy for "is this row a live fleet peer?": live WS
// connection state is per-replica and in-process, so last_seen_at staleness is
// the only thing every replica can agree on. Fifteen minutes sits comfortably
// above the WS heartbeat/reconnect cadence, so a peer that is actually
// connected is never mistaken for a machine that has been replaced. Erring
// long is cheap (the duplicate row lives until the next restart or the 7-day
// GC); erring short retires a working agent.
const SupersededSystemAgentDisconnectWindow = 15 * time.Minute

// SupersedeReplacedSystemAgents retires the system agents a freshly enrolled
// one replaces, and reports how many it retired.
//
// A system agent that boots without a pinned identity (SP_AGENT_KEYS)
// generates a keypair and enrolls anew every time — that is the intended
// enroll-on-boot fleet design, but it means every pod restart or redeploy
// leaves the previous row behind until the agent_gc job's 7-day silence
// window. A same-name system agent reappearing in the same region is a machine
// replacement, not a fleet peer, so the predecessor is retired here instead:
// the fleet list collapses to one live row within one restart.
//
// The match is deliberately narrow. Only kind='system' rows are candidates —
// org agents are customer-managed and offline never means replaced (the same
// reasoning as the GC's org exclusion). Only rows that are provably not a live
// fleet peer are touched, meaning last_seen_at is NULL or older than
// SupersededSystemAgentDisconnectWindow: genuine fleets with per-machine names
// (fly machine IDs) never collide on name in the first place, and a same-name
// peer that is actually connected is protected by that guard.
//
// It takes a bun.IDB rather than a *bun.DB so the enrollment path can hand it
// its own transaction: the new row and the retirement of its predecessors must
// commit together or not at all.
func SupersedeReplacedSystemAgents(
	ctx context.Context, idb bun.IDB, newAgent *models.Agent, now time.Time,
) (int, error) {
	if newAgent == nil || !newAgent.IsSystem() {
		return 0, nil
	}

	var predecessors []*models.Agent

	err := idb.NewSelect().
		Model(&predecessors).
		Column("uid").
		Where("kind = ?", models.AgentKindSystem).
		Where("region = ?", newAgent.Region).
		Where("name = ?", newAgent.Name).
		Where("status = ?", models.AgentStatusActive).
		Where("deleted_at IS NULL").
		Where("uid != ?", newAgent.UID).
		Where("(last_seen_at IS NULL OR last_seen_at < ?)",
			now.Add(-SupersededSystemAgentDisconnectWindow)).
		Scan(ctx)
	if err != nil {
		return 0, fmt.Errorf("failed to list superseded system agents: %w", err)
	}

	if len(predecessors) == 0 {
		return 0, nil
	}

	uids := make([]string, 0, len(predecessors))
	for _, agent := range predecessors {
		uids = append(uids, agent.UID)
	}

	// Same terminal state as RetireSystemAgent, and scoped to kind='system'
	// for the same reason: this statement must never be able to touch a
	// customer-managed agent, whatever the UID list says.
	if _, err := idb.NewUpdate().
		Model((*models.Agent)(nil)).
		Set("status = ?", models.AgentStatusRevoked).
		Set("revoked_at = ?", now).
		Set("deleted_at = ?", now).
		Set("updated_at = ?", now).
		Where("uid IN (?)", bun.List(uids)).
		Where("kind = ?", models.AgentKindSystem).
		Where("deleted_at IS NULL").
		Exec(ctx); err != nil {
		return 0, fmt.Errorf("failed to supersede system agents: %w", err)
	}

	if err := RetireAgentWorkerRows(ctx, idb, uids, now); err != nil {
		return 0, err
	}

	return len(uids), nil
}

// RetireAgentWorkerRows soft-deletes the workers rows belonging to
// retired/purged/superseded agents, so their leases and result attribution stop
// lingering.
//
// The rows are resolved through the same deterministic slug the WS handler
// registers them under (agentcrypto.WorkerSlug), so a row that was adopted by
// slug — rather than created with the agent's UID — is still found. Shared by
// the agent_gc job and the supersede-on-enroll path so the two can never drift.
func RetireAgentWorkerRows(ctx context.Context, idb bun.IDB, agentUIDs []string, now time.Time) error {
	if len(agentUIDs) == 0 {
		return nil
	}

	slugs := make([]string, 0, len(agentUIDs))
	for _, uid := range agentUIDs {
		slugs = append(slugs, agentcrypto.WorkerSlug(uid))
	}

	if _, err := idb.NewUpdate().
		Model((*models.Worker)(nil)).
		Set("deleted_at = ?", now).
		Where("slug IN (?)", bun.List(slugs)).
		Where("deleted_at IS NULL").
		Exec(ctx); err != nil {
		return fmt.Errorf("failed to retire agent worker rows: %w", err)
	}

	return nil
}
