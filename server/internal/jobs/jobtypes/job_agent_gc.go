package jobtypes

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/jobs/jobdef"
	"github.com/fclairamb/solidping/server/internal/jobs/jobsvc"
)

// agentGCInterval is how often the fleet-hygiene sweep runs. Retiring a dead
// machine is never urgent — a revoked-by-absence agent holds no lease longer
// than the lease TTL — so a 6h cadence is ample and keeps the write volume of
// an enroll-on-boot fleet negligible.
const agentGCInterval = 6 * time.Hour

// defaultAgentStaleAfter is how long a system agent may go unheard-from before
// it is retired. It has to be comfortably longer than any plausible network
// partition or deploy freeze (a fly machine reconnects within seconds), but
// short enough that the agent list means something. 7 days is the spec default.
const defaultAgentStaleAfter = 7 * 24 * time.Hour

// defaultRevokedPurgeAfter is how long a revoked agent stays listed before it
// is purged (soft-deleted). Revocation, not last-seen, is the clock: an admin
// cut this agent's access on purpose, and how recently it phoned in before
// that says nothing about whether the row should stay. Two days is long
// enough for an admin to notice and act on a mistaken revocation, short
// enough that the fleet list stays meaningful (spec 2026-08-16-03).
const defaultRevokedPurgeAfter = 2 * 24 * time.Hour

// agentNonceRetention is how long a consumed reconnect nonce is kept before the
// sweep prunes it. The per-agent prune on insert already covers agents that keep
// reconnecting; this catches the rows of machines that never came back. It must
// stay >= the handler's own retention window (2x the accepted clock skew) — an
// hour is that with a very large margin.
const agentNonceRetention = time.Hour

// AgentGCJobDefinition is the factory for the platform-agent GC sweep.
type AgentGCJobDefinition struct{}

// Type returns the agent GC job type.
func (d *AgentGCJobDefinition) Type() jobdef.JobType {
	return jobdef.JobTypeAgentGC
}

// AgentGCJobConfig configures the sweep.
type AgentGCJobConfig struct {
	// StaleAfterSeconds overrides how long a system agent may stay silent
	// before being retired. Zero (the seeded default) means
	// defaultAgentStaleAfter.
	StaleAfterSeconds int `json:"staleAfterSeconds,omitempty"`
	// RevokedPurgeAfterSeconds overrides how long a revoked agent (org or
	// system) stays listed before being purged. Zero (the seeded default)
	// means defaultRevokedPurgeAfter.
	RevokedPurgeAfterSeconds int `json:"revokedPurgeAfterSeconds,omitempty"`
}

// CreateJobRun builds an executable instance.
func (d *AgentGCJobDefinition) CreateJobRun(config json.RawMessage) (jobdef.JobRunner, error) {
	var cfg AgentGCJobConfig
	if len(config) > 0 {
		if err := json.Unmarshal(config, &cfg); err != nil {
			return nil, err
		}
	}

	return &AgentGCJobRun{config: cfg}, nil
}

// AgentGCJobRun is the runtime state for one sweep.
type AgentGCJobRun struct {
	config AgentGCJobConfig
}

// Run retires stale platform agents, purges long-revoked agents of any kind,
// and prunes consumed reconnect nonces, then reschedules itself.
//
// Why the stale-system-agent pass exists (spec 2026-07-27-01): a platform
// region on fly.io is a fleet of machines that each generate their own
// keypair and enroll on boot — that is what lets an app-wide fly secret hold
// a single multi-use enrollment token without any private key ever being
// shared. The price is that every machine replacement leaves an agent row
// (and its workers row) behind forever.
//
// Org agents are deliberately untouched by that pass: they are
// customer-managed, long-lived, and a customer's agent being offline for a
// fortnight is not a reason for the platform to delete it.
//
// Why the revoked-agent purge pass exists (spec 2026-08-16-03): revocation is
// a different case entirely — an admin cut this agent's access on purpose, so
// nobody is waiting for it to come back, whether it is an org or a system
// agent. Left alone, a revoked row lingers in the fleet list forever. The
// clock is revoked_at, never last_seen_at (see ListPurgeableRevokedAgents).
func (r *AgentGCJobRun) Run(ctx context.Context, jctx *jobdef.JobContext) error {
	log := jctx.Logger

	if jctx.DBService == nil {
		log.InfoContext(ctx, "Skipping agent GC (database service not available)")

		return nil
	}

	now := time.Now()

	stale, err := jctx.DBService.ListStaleSystemAgents(ctx, now.Add(-r.staleAfter()))
	if err != nil {
		log.ErrorContext(ctx, "Failed to list stale system agents", "error", err)

		return jobdef.NewRetryableError(fmt.Errorf("list stale system agents: %w", err))
	}

	retired := 0

	for _, agent := range stale {
		if r.retire(ctx, jctx, agent) {
			retired++
		}
	}

	if retired > 0 {
		log.InfoContext(ctx, "Retired stale system agents",
			"retired", retired, "candidates", len(stale), "staleAfter", r.staleAfter().String())
	}

	revoked, err := jctx.DBService.ListPurgeableRevokedAgents(ctx, now.Add(-r.revokedPurgeAfter()))
	if err != nil {
		log.ErrorContext(ctx, "Failed to list purgeable revoked agents", "error", err)

		return jobdef.NewRetryableError(fmt.Errorf("list purgeable revoked agents: %w", err))
	}

	purged := 0

	for _, agent := range revoked {
		if r.purge(ctx, jctx, agent) {
			purged++
		}
	}

	if purged > 0 {
		log.InfoContext(ctx, "Purged stale revoked agents",
			"purged", purged, "candidates", len(revoked), "revokedPurgeAfter", r.revokedPurgeAfter().String())
	}

	if pruned, pruneErr := jctx.DBService.PruneAgentNonces(ctx, now.Add(-agentNonceRetention)); pruneErr != nil {
		// Nonce rows are bounded by the per-agent prune on insert, so a failed
		// global sweep is a housekeeping miss, not a correctness problem: it
		// must not fail (and retry) the whole GC run.
		log.WarnContext(ctx, "Failed to prune agent nonces", "error", pruneErr)
	} else if pruned > 0 {
		log.InfoContext(ctx, "Pruned consumed agent nonces", "pruned", pruned)
	}

	r.rescheduleSelf(ctx, jctx)

	return nil
}

// retire revokes+soft-deletes one system agent and soft-deletes the workers row
// that carried its leases and result attribution. The worker row is resolved
// through the same deterministic slug the WS handler registers it under, so a
// row that was adopted by slug (rather than created with the agent's UID) is
// still found. Reports whether the agent row was retired.
func (r *AgentGCJobRun) retire(ctx context.Context, jctx *jobdef.JobContext, agent *models.Agent) bool {
	log := jctx.Logger

	if err := jctx.DBService.RetireSystemAgent(ctx, agent.UID); err != nil {
		log.WarnContext(ctx, "Failed to retire stale system agent", "agent", agent.UID, "error", err)

		return false
	}

	r.cleanupWorkerRow(ctx, jctx, agent)

	return true
}

// purge soft-deletes one revoked agent (org or system kind — see
// ListPurgeableRevokedAgents) and the workers row that carried its leases and
// result attribution. Reports whether the agent row was purged.
func (r *AgentGCJobRun) purge(ctx context.Context, jctx *jobdef.JobContext, agent *models.Agent) bool {
	log := jctx.Logger

	if err := jctx.DBService.PurgeAgent(ctx, agent.UID); err != nil {
		log.WarnContext(ctx, "Failed to purge revoked agent", "agent", agent.UID, "error", err)

		return false
	}

	r.cleanupWorkerRow(ctx, jctx, agent)

	return true
}

// cleanupWorkerRow soft-deletes the workers row belonging to a
// retired/purged agent, resolved through the same deterministic slug the WS
// handler registers it under, so a row that was adopted by slug (rather than
// created with the agent's UID) is still found.
//
// The resolution and the soft delete live in db.RetireAgentWorkerRows, shared
// with the supersede-on-enroll path so the two cannot drift. An agent that
// enrolled and never connected has no workers row at all; that is a no-op, not
// an error.
func (r *AgentGCJobRun) cleanupWorkerRow(ctx context.Context, jctx *jobdef.JobContext, agent *models.Agent) {
	if err := jctx.DBService.RetireAgentWorkerRow(ctx, agent.UID); err != nil {
		jctx.Logger.WarnContext(ctx, "Failed to delete agent's worker row",
			"agent", agent.UID, "error", err)
	}
}

// staleAfter resolves the silence window from the job config, falling back to
// the package default.
func (r *AgentGCJobRun) staleAfter() time.Duration {
	if r.config.StaleAfterSeconds > 0 {
		return time.Duration(r.config.StaleAfterSeconds) * time.Second
	}

	return defaultAgentStaleAfter
}

// revokedPurgeAfter resolves the revoked-agent retention window from the job
// config, falling back to the package default.
func (r *AgentGCJobRun) revokedPurgeAfter() time.Duration {
	if r.config.RevokedPurgeAfterSeconds > 0 {
		return time.Duration(r.config.RevokedPurgeAfterSeconds) * time.Second
	}

	return defaultRevokedPurgeAfter
}

func (r *AgentGCJobRun) rescheduleSelf(ctx context.Context, jctx *jobdef.JobContext) {
	if jctx.Services == nil || jctx.Services.Jobs == nil {
		return
	}

	scheduledAt := time.Now().Add(agentGCInterval)

	// Carry this run's configuration into the follow-up run. Passing nil here
	// made an operator override of StaleAfterSeconds /
	// RevokedPurgeAfterSeconds survive exactly one sweep before silently
	// reverting to the defaults. The zero config marshals to `{}`, which is
	// byte-identical to what the job service derives from a nil config, so the
	// default path's pending-job dedup is unchanged.
	config, err := json.Marshal(r.config)
	if err != nil {
		jctx.Logger.WarnContext(ctx, "Failed to marshal agent GC config", "error", err)

		config = nil
	}

	if _, err := jctx.Services.Jobs.CreateJob(
		ctx, "", string(jobdef.JobTypeAgentGC), config, &jobsvc.JobOptions{ScheduledAt: &scheduledAt},
	); err != nil {
		jctx.Logger.WarnContext(ctx, "Failed to reschedule agent GC", "error", err)
	}
}
