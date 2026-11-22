package agentws

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/uptrace/bun"

	agentcrypto "github.com/fclairamb/solidping/server/internal/agents"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/version"
)

// The rolling agent upgrade (spec 2026-11-22-04).
//
// A connected agent whose build version differs from the server's (neither
// side a dev build) is asked, with an `upgrade` frame, to restart: it drains
// and exits 0, and its supervisor starts it again on the image the server's
// registry resolves `current` to. At most ONE agent per region is upgrading at
// a time, across every server replica, so the coordination lives in the
// database:
//
//   - agent_upgrade_slots holds, per region, the agent currently upgrading and
//     its deadline. Taking the slot is a compare-and-swap on `generation`.
//   - agent_upgrade_candidates lists the drifted agents currently connected
//     (their connection refreshes seen_at) and how many times each was asked.
//     The next one asked is the one asked the fewest times, so an agent that
//     timed out is not retried before the others.
//
// The slot is released when the agent comes back reporting the server's
// version; otherwise it expires at the deadline, the next contender logs a
// WARN naming the agent that did not come back, and moves on.

const (
	// defaultUpgradeCheckInterval is how often a drifted connection tries to
	// take its region's upgrade slot (and refreshes its candidate row).
	defaultUpgradeCheckInterval = 30 * time.Second
	// defaultUpgradeTimeout is how long an agent asked to upgrade has to come
	// back on the server's version before the next agent is asked.
	defaultUpgradeTimeout = 10 * time.Minute
	// maxUpgradeAttempts caps how many times one agent is asked to upgrade to
	// one version. An agent whose supervisor restarts it on the same image
	// (a Docker restart policy, imagePullPolicy: IfNotPresent) would
	// otherwise restart every timeout, forever.
	maxUpgradeAttempts = 3
	// candidateStaleAfter (in check intervals) drops a candidate whose
	// connection stopped refreshing it.
	candidateStaleIntervals = 3
	// systemScope prefixes the region key of a system agent's shared region.
	systemScope = "system"
)

type upgradeSlotRow struct {
	bun.BaseModel `bun:"table:agent_upgrade_slots"`

	RegionKey     string    `bun:"region_key,pk"`
	AgentUID      string    `bun:"agent_uid,notnull"`
	TargetVersion string    `bun:"target_version,notnull"`
	Deadline      time.Time `bun:"deadline,notnull"`
	Released      bool      `bun:"released,notnull"`
	Generation    int64     `bun:"generation,notnull"`
	UpdatedAt     time.Time `bun:"updated_at,notnull"`
}

type upgradeCandidateRow struct {
	bun.BaseModel `bun:"table:agent_upgrade_candidates,alias:cand"`

	AgentUID      string    `bun:"agent_uid,pk"`
	RegionKey     string    `bun:"region_key,notnull"`
	TargetVersion string    `bun:"target_version,notnull"`
	Attempts      int       `bun:"attempts,notnull"`
	SeenAt        time.Time `bun:"seen_at,notnull"`
	CreatedAt     time.Time `bun:"created_at,notnull"`
}

// upgradeState is one connection's view of the rollout. Touched only from the
// connection's own loop.
type upgradeState struct {
	// agentVersion is the version the agent reported on this connection.
	agentVersion string
	// sent is set once this connection carried an upgrade frame.
	sent bool
	// gaveUpLogged keeps the "giving up" WARN to one per connection.
	gaveUpLogged bool
}

// SetAutoUpgradeTimings overrides the upgrade check cadence and timeout for
// connections accepted afterwards. It exists for tests.
func (h *Handler) SetAutoUpgradeTimings(checkInterval, timeout time.Duration) {
	if checkInterval > 0 {
		h.upgradeCheckInterval = checkInterval
	}

	if timeout > 0 {
		h.upgradeTimeout = timeout
	}
}

// upgradeRegionKey scopes a region to its owner: an org agent's region is
// org-relative (`@dc1`), so two orgs' `@dc1` are different regions.
func upgradeRegionKey(agent *models.Agent) string {
	if agent.IsSystem() {
		return systemScope + "|" + agent.Region
	}

	return agent.OrgUID() + "|" + agent.Region
}

// upgradeTarget returns the server's version when an agent on reported must
// upgrade to it: auto-upgrade on, both versions known and tagged, and
// different.
func (h *Handler) upgradeTarget(reported string) (string, bool) {
	if !h.autoUpgrade || reported == "" {
		return "", false
	}

	serverVersion := version.Get().Version
	if isDevBuild(serverVersion) || isDevBuild(reported) || reported == serverVersion {
		return "", false
	}

	return serverVersion, true
}

// noteAgentVersion records the version an agent reported on this connection.
// On the server's version, it releases the agent's upgrade slot and drops its
// candidate row: the upgrade worked. Drifted, it registers the agent as a
// candidate; the connection's upgrade ticker does the rest.
func (h *Handler) noteAgentVersion(ctx context.Context, state *connState, reported string) {
	if reported == "" || reported == state.upgrade.agentVersion {
		return
	}

	state.upgrade.agentVersion = reported

	if !h.autoUpgrade {
		return
	}

	if reported == version.Get().Version {
		h.completeUpgrade(ctx, state.agent, reported)

		return
	}

	if target, ok := h.upgradeTarget(reported); ok {
		if _, err := h.touchCandidate(ctx, state.agent, target, time.Now()); err != nil {
			h.logger.WarnContext(ctx, "could not register the agent for an upgrade",
				"agent", state.agent.UID, "error", err)
		}
	}
}

// completeUpgrade releases the agent's slot (when it holds it) and forgets it
// as a candidate.
func (h *Handler) completeUpgrade(ctx context.Context, agent *models.Agent, reported string) {
	db := h.dbService.DB()

	res, err := db.NewUpdate().Model((*upgradeSlotRow)(nil)).
		Set("released = ?", true).
		Set("generation = generation + 1").
		Set("updated_at = ?", time.Now().UTC()).
		Where("region_key = ?", upgradeRegionKey(agent)).
		Where("agent_uid = ?", agent.UID).
		Where("released = ?", false).
		Exec(ctx)
	if err != nil {
		h.logger.WarnContext(ctx, "could not release the agent's upgrade slot", "agent", agent.UID, "error", err)
	} else if n, _ := res.RowsAffected(); n > 0 {
		h.logger.InfoContext(ctx, "agent came back on the server version",
			"agent", agent.UID, "region", agent.Region, "version", reported)
	}

	if _, err := db.NewDelete().Model((*upgradeCandidateRow)(nil)).
		Where("agent_uid = ?", agent.UID).
		Exec(ctx); err != nil {
		h.logger.WarnContext(ctx, "could not clear the agent's upgrade candidacy", "agent", agent.UID, "error", err)
	}
}

// touchCandidate upserts the agent's candidate row (resetting its attempts
// when the target version changed) and returns it.
func (h *Handler) touchCandidate(
	ctx context.Context, agent *models.Agent, target string, now time.Time,
) (*upgradeCandidateRow, error) {
	db := h.dbService.DB()
	now = now.UTC()

	row := &upgradeCandidateRow{
		AgentUID: agent.UID, RegionKey: upgradeRegionKey(agent), TargetVersion: target,
		SeenAt: now, CreatedAt: now,
	}

	// SET expressions read the row's OLD values (Postgres and SQLite alike),
	// so the CASE compares against the previous target.
	if _, err := db.NewInsert().Model(row).
		On("CONFLICT (agent_uid) DO UPDATE").
		Set("attempts = CASE WHEN cand.target_version = EXCLUDED.target_version " +
			"THEN cand.attempts ELSE 0 END").
		Set("region_key = EXCLUDED.region_key").
		Set("target_version = EXCLUDED.target_version").
		Set("seen_at = EXCLUDED.seen_at").
		Exec(ctx); err != nil {
		return nil, fmt.Errorf("upsert upgrade candidate: %w", err)
	}

	stored := new(upgradeCandidateRow)
	if err := db.NewSelect().Model(stored).Where("agent_uid = ?", agent.UID).Scan(ctx); err != nil {
		return nil, fmt.Errorf("read upgrade candidate: %w", err)
	}

	return stored, nil
}

// handleUpgradeTick is the connection's periodic rollout step. It returns
// false when writing to the socket failed (the connection is over).
func (h *Handler) handleUpgradeTick(ctx context.Context, conn *websocket.Conn, state *connState) bool {
	if state.upgrade.sent {
		return true
	}

	target, ok := h.upgradeTarget(state.upgrade.agentVersion)
	if !ok {
		return true
	}

	acquired, err := h.tryAcquireUpgradeSlot(ctx, state, target)
	if err != nil {
		h.logger.WarnContext(ctx, "agent upgrade coordination failed", "agent", state.agent.UID, "error", err)

		return true
	}

	if !acquired {
		return true
	}

	state.upgrade.sent = true

	if err := wsjson.Write(ctx, conn, agentcrypto.ServerFrame{
		Type: agentcrypto.MsgTypeUpgrade, TargetVersion: target,
	}); err != nil {
		return false
	}

	return true
}

// tryAcquireUpgradeSlot takes the agent's region slot when it is free and
// this agent is next in line.
func (h *Handler) tryAcquireUpgradeSlot(ctx context.Context, state *connState, target string) (bool, error) {
	agent := state.agent
	now := time.Now().UTC()
	regionKey := upgradeRegionKey(agent)

	mine, err := h.touchCandidate(ctx, agent, target, now)
	if err != nil {
		return false, err
	}

	if mine.Attempts >= maxUpgradeAttempts {
		if !state.upgrade.gaveUpLogged {
			state.upgrade.gaveUpLogged = true
			h.logger.WarnContext(ctx,
				"agent did not come back on the server version after repeated upgrade requests; giving up "+
					"(check that its supervisor pulls the image again, e.g. imagePullPolicy: Always)",
				"agent", agent.UID, "region", agent.Region, "agent_version", state.upgrade.agentVersion,
				"server_version", target, "attempts", mine.Attempts)
		}

		return false, nil
	}

	slot, err := h.loadUpgradeSlot(ctx, regionKey)
	if err != nil {
		return false, err
	}

	timedOut := ""

	if slot != nil && !slot.Released {
		if now.Before(slot.Deadline) {
			return false, nil
		}

		timedOut = slot.AgentUID
	}

	next, live, err := h.nextUpgradeCandidate(ctx, regionKey, target, now)
	if err != nil {
		return false, err
	}

	if next != agent.UID {
		return false, nil
	}

	won, err := h.takeUpgradeSlot(ctx, slot, regionKey, agent.UID, target, now)
	if err != nil || !won {
		return false, err
	}

	if _, err := h.dbService.DB().NewUpdate().Model((*upgradeCandidateRow)(nil)).
		Set("attempts = attempts + 1").
		Where("agent_uid = ?", agent.UID).
		Exec(ctx); err != nil {
		return false, fmt.Errorf("count upgrade attempt: %w", err)
	}

	if timedOut != "" {
		h.logger.WarnContext(ctx, "agent did not come back on the server version within the upgrade timeout; "+
			"moving on to the next agent",
			"agent", timedOut, "region", agent.Region, "timeout", h.upgradeTimeout)
	}

	h.logUpgradeStart(ctx, state, target, live)

	return true, nil
}

// logUpgradeStart logs the upgrade request; a region with a single agent is
// still upgraded, with a brief gap, which is said at INFO.
func (h *Handler) logUpgradeStart(ctx context.Context, state *connState, target string, live int) {
	agent := state.agent
	attrs := []any{
		"agent", agent.UID, "region", agent.Region,
		"agent_version", state.upgrade.agentVersion, "server_version", target,
	}

	if count, err := h.countActiveAgents(ctx, agent); err == nil && count <= 1 && live <= 1 {
		h.logger.InfoContext(ctx, "asking the only agent of its region to upgrade: "+
			"the region has no agent until it reconnects", attrs...)

		return
	}

	h.logger.InfoContext(ctx, "asking agent to upgrade to the server version", attrs...)
}

func (h *Handler) loadUpgradeSlot(ctx context.Context, regionKey string) (*upgradeSlotRow, error) {
	slot := new(upgradeSlotRow)

	err := h.dbService.DB().NewSelect().Model(slot).Where("region_key = ?", regionKey).Scan(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil //nolint:nilnil // no slot row yet: the region's slot is free
	}

	if err != nil {
		return nil, fmt.Errorf("read upgrade slot: %w", err)
	}

	return slot, nil
}

// nextUpgradeCandidate returns the live candidate to upgrade next (fewest
// attempts, then longest waiting) and how many live candidates there are.
// Stale rows (their connection stopped refreshing them) are dropped.
func (h *Handler) nextUpgradeCandidate(
	ctx context.Context, regionKey, target string, now time.Time,
) (string, int, error) {
	db := h.dbService.DB()

	var rows []upgradeCandidateRow
	if err := db.NewSelect().Model(&rows).Where("region_key = ?", regionKey).Scan(ctx); err != nil {
		return "", 0, fmt.Errorf("list upgrade candidates: %w", err)
	}

	staleBefore := now.Add(-candidateStaleIntervals * h.upgradeCheckInterval)
	live := rows[:0]

	var stale []string

	for i := range rows {
		switch {
		case rows[i].SeenAt.Before(staleBefore):
			stale = append(stale, rows[i].AgentUID)
		case rows[i].TargetVersion == target && rows[i].Attempts < maxUpgradeAttempts:
			live = append(live, rows[i])
		}
	}

	if len(stale) > 0 {
		if _, err := db.NewDelete().Model((*upgradeCandidateRow)(nil)).
			Where("agent_uid IN (?)", bun.In(stale)).
			Exec(ctx); err != nil {
			h.logger.DebugContext(ctx, "could not prune stale upgrade candidates", "error", err)
		}
	}

	if len(live) == 0 {
		return "", 0, nil
	}

	sort.Slice(live, func(i, j int) bool {
		if live[i].Attempts != live[j].Attempts {
			return live[i].Attempts < live[j].Attempts
		}

		if !live[i].CreatedAt.Equal(live[j].CreatedAt) {
			return live[i].CreatedAt.Before(live[j].CreatedAt)
		}

		return live[i].AgentUID < live[j].AgentUID
	})

	return live[0].AgentUID, len(live), nil
}

// takeUpgradeSlot claims the region slot: an insert when the region has none,
// a compare-and-swap on generation otherwise. Exactly one contender wins.
func (h *Handler) takeUpgradeSlot(
	ctx context.Context, slot *upgradeSlotRow, regionKey, agentUID, target string, now time.Time,
) (bool, error) {
	db := h.dbService.DB()
	deadline := now.Add(h.upgradeTimeout)

	if slot == nil {
		res, err := db.NewInsert().Model(&upgradeSlotRow{
			RegionKey: regionKey, AgentUID: agentUID, TargetVersion: target,
			Deadline: deadline, UpdatedAt: now,
		}).On("CONFLICT (region_key) DO NOTHING").Exec(ctx)
		if err != nil {
			return false, fmt.Errorf("take upgrade slot: %w", err)
		}

		n, _ := res.RowsAffected()

		return n == 1, nil
	}

	res, err := db.NewUpdate().Model((*upgradeSlotRow)(nil)).
		Set("agent_uid = ?", agentUID).
		Set("target_version = ?", target).
		Set("deadline = ?", deadline).
		Set("released = ?", false).
		Set("generation = generation + 1").
		Set("updated_at = ?", now).
		Where("region_key = ?", regionKey).
		Where("generation = ?", slot.Generation).
		Exec(ctx)
	if err != nil {
		return false, fmt.Errorf("take upgrade slot: %w", err)
	}

	n, _ := res.RowsAffected()

	return n == 1, nil
}

// countActiveAgents counts the active agents of the agent's region.
func (h *Handler) countActiveAgents(ctx context.Context, agent *models.Agent) (int, error) {
	query := h.dbService.DB().NewSelect().Model((*models.Agent)(nil)).
		Where("region = ?", agent.Region).
		Where("status = ?", models.AgentStatusActive).
		Where("deleted_at IS NULL")

	if agent.IsSystem() {
		query = query.Where("organization_uid IS NULL")
	} else {
		query = query.Where("organization_uid = ?", agent.OrgUID())
	}

	count, err := query.Count(ctx)
	if err != nil {
		return 0, fmt.Errorf("count region agents: %w", err)
	}

	return count, nil
}

// upgradeTicker returns the connection's rollout ticker channel, nil (never
// fires) when auto-upgrade is off.
func (h *Handler) upgradeTicker() (<-chan time.Time, func()) {
	if !h.autoUpgrade {
		return nil, func() {}
	}

	ticker := time.NewTicker(h.upgradeCheckInterval)

	return ticker.C, ticker.Stop
}
