package agentws

import (
	"bytes"
	"log/slog"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/version"
)

const upgradeTestTarget = "1.2.3"

// upgradeHarness is a handler over an in-memory SQLite database whose logs
// are captured.
type upgradeHarness struct {
	handler *Handler
	svc     *sqlite.Service
	logs    *bytes.Buffer
}

func newUpgradeHarness(t *testing.T) *upgradeHarness {
	t.Helper()

	ctx := t.Context()

	svc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	require.NoError(t, err)
	require.NoError(t, svc.Initialize(ctx))
	t.Cleanup(func() { _ = svc.Close() })

	handler := NewHandler(&config.Config{}, svc, nil, nil, nil, nil, nil, nil)
	t.Cleanup(handler.Close)

	logs := &bytes.Buffer{}
	handler.logger = slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))

	return &upgradeHarness{handler: handler, svc: svc, logs: logs}
}

func newOrgAgent() *models.Agent {
	orgUID := uuid.NewString()

	return &models.Agent{UID: uuid.NewString(), OrganizationUID: &orgUID, Kind: models.AgentKindOrg, Region: "@dc1"}
}

func newUpgradeState(agent *models.Agent) *connState {
	return &connState{agent: agent, workerUID: uuid.NewString(), upgrade: upgradeState{agentVersion: "1.2.0"}}
}

func TestUpgradeRegionKeyIsScopedToItsOwner(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	orgAgent := newOrgAgent()
	other := newOrgAgent()
	system := &models.Agent{UID: uuid.NewString(), Kind: models.AgentKindSystem, Region: "eu"}

	r.NotEqual(upgradeRegionKey(orgAgent), upgradeRegionKey(other), "two orgs' @dc1 are different regions")
	r.Equal(systemScope+"|eu", upgradeRegionKey(system))
}

func TestUpgradeCoordinationReportsDatabaseFailures(t *testing.T) {
	t.Parallel()

	h := newUpgradeHarness(t)
	ctx := t.Context()
	agent := newOrgAgent()
	state := newUpgradeState(agent)

	require.NoError(t, h.svc.Close())

	r := require.New(t)

	// Errors are returned.
	_, err := h.handler.touchCandidate(ctx, agent, upgradeTestTarget, time.Now())
	r.Error(err)

	_, err = h.handler.loadUpgradeSlot(ctx, "region")
	r.Error(err)

	_, _, err = h.handler.nextUpgradeCandidate(ctx, "region", upgradeTestTarget, time.Now())
	r.Error(err)

	_, err = h.handler.takeUpgradeSlot(ctx, nil, "region", agent.UID, upgradeTestTarget, time.Now())
	r.Error(err)

	_, err = h.handler.takeUpgradeSlot(ctx, &upgradeSlotRow{RegionKey: "region"}, "region", agent.UID,
		upgradeTestTarget, time.Now())
	r.Error(err)

	_, err = h.handler.countActiveAgents(ctx, agent)
	r.Error(err)

	_, err = h.handler.tryAcquireUpgradeSlot(ctx, state, upgradeTestTarget)
	r.Error(err)

	// Best-effort writes are logged.
	h.logs.Reset()
	h.handler.recordVersionPin(ctx, state, true)
	r.Contains(h.logs.String(), "could not record the agent's version pin")
	r.False(state.upgrade.pinReported, "a failed write is retried on the next report")

	h.logs.Reset()
	h.handler.withdrawFromUpgrade(ctx, state)
	r.Contains(h.logs.String(), "could not release the pinned agent's upgrade slot")
	r.Contains(h.logs.String(), "could not clear the pinned agent's upgrade candidacy")

	h.logs.Reset()
	h.handler.completeUpgrade(ctx, agent, upgradeTestTarget)
	r.Contains(h.logs.String(), "could not release the agent's upgrade slot")
	r.Contains(h.logs.String(), "could not clear the agent's upgrade candidacy")

	h.logs.Reset()
	h.handler.logUpgradeStart(ctx, state, upgradeTestTarget, 1)
	r.Contains(h.logs.String(), "asking agent to upgrade to the server version",
		"when the region cannot be counted, the generic line is logged")
}

//nolint:paralleltest // mutates version.Version
func TestFailingTickKeepsTheConnectionOpen(t *testing.T) {
	withTargetVersion(t)

	h := newUpgradeHarness(t)
	state := newUpgradeState(newOrgAgent())

	require.NoError(t, h.svc.Close())

	require.True(t, h.handler.handleUpgradeTick(t.Context(), nil, state))
	require.Contains(t, h.logs.String(), "agent upgrade coordination failed")
}

//nolint:paralleltest // mutates version.Version
func TestHandleUpgradeTickSkipsWhenThereIsNothingToDo(t *testing.T) {
	withTargetVersion(t)

	h := newUpgradeHarness(t)
	ctx := t.Context()

	sent := newUpgradeState(newOrgAgent())
	sent.upgrade.sent = true
	require.True(t, h.handler.handleUpgradeTick(ctx, nil, sent))

	pinned := newUpgradeState(newOrgAgent())
	pinned.upgrade.pinned = true
	require.True(t, h.handler.handleUpgradeTick(ctx, nil, pinned))

	current := newUpgradeState(newOrgAgent())
	current.upgrade.agentVersion = upgradeTestTarget
	require.True(t, h.handler.handleUpgradeTick(ctx, nil, current))

	// A tick that does not win the slot sends nothing and registers nothing
	// beyond the candidate row.
	var rows []upgradeSlotRow

	require.NoError(t, h.svc.DB().NewSelect().Model(&rows).Scan(ctx))
	require.Empty(t, rows)
}

//nolint:paralleltest // mutates version.Version
func TestGivesUpAfterRepeatedUpgradeRequests(t *testing.T) {
	withTargetVersion(t)

	r := require.New(t)
	h := newUpgradeHarness(t)
	ctx := t.Context()
	agent := newOrgAgent()
	state := newUpgradeState(agent)

	_, err := h.handler.touchCandidate(ctx, agent, upgradeTestTarget, time.Now())
	r.NoError(err)

	_, err = h.svc.DB().NewUpdate().Model((*upgradeCandidateRow)(nil)).
		Set("attempts = ?", maxUpgradeAttempts).Where("agent_uid = ?", agent.UID).Exec(ctx)
	r.NoError(err)

	for range 2 {
		acquired, acquireErr := h.handler.tryAcquireUpgradeSlot(ctx, state, upgradeTestTarget)
		r.NoError(acquireErr)
		r.False(acquired)
	}

	r.Equal(1, bytes.Count(h.logs.Bytes(), []byte("giving up")), "the WARN is logged once per connection")
}

//nolint:paralleltest // mutates version.Version
func TestTimedOutSlotIsTakenOverByTheNextAgent(t *testing.T) {
	withTargetVersion(t)

	r := require.New(t)
	h := newUpgradeHarness(t)
	ctx := t.Context()
	agent := newOrgAgent()
	state := newUpgradeState(agent)
	regionKey := upgradeRegionKey(agent)
	now := time.Now().UTC()

	// Another agent holds the slot, and its deadline has passed.
	_, err := h.svc.DB().NewInsert().Model(&upgradeSlotRow{
		RegionKey: regionKey, AgentUID: "stuck-agent", TargetVersion: upgradeTestTarget,
		Deadline: now.Add(-time.Minute), UpdatedAt: now.Add(-time.Hour),
	}).Exec(ctx)
	r.NoError(err)

	acquired, err := h.handler.tryAcquireUpgradeSlot(ctx, state, upgradeTestTarget)
	r.NoError(err)
	r.True(acquired)
	r.Contains(h.logs.String(), "did not come back on the server version within the upgrade timeout")
	r.Contains(h.logs.String(), "stuck-agent")

	slot, err := h.handler.loadUpgradeSlot(ctx, regionKey)
	r.NoError(err)
	r.Equal(agent.UID, slot.AgentUID)
	r.False(slot.Released)
	r.Equal(int64(1), slot.Generation)

	// A slot still within its deadline is not taken.
	other := newUpgradeState(&models.Agent{
		UID: uuid.NewString(), OrganizationUID: agent.OrganizationUID, Kind: models.AgentKindOrg, Region: "@dc1",
	})
	acquired, err = h.handler.tryAcquireUpgradeSlot(ctx, other, upgradeTestTarget)
	r.NoError(err)
	r.False(acquired)
}

func TestTakeUpgradeSlotLosesWhenTheGenerationMoved(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	h := newUpgradeHarness(t)
	ctx := t.Context()
	now := time.Now().UTC()

	_, err := h.svc.DB().NewInsert().Model(&upgradeSlotRow{
		RegionKey: "region", AgentUID: "a", TargetVersion: upgradeTestTarget,
		Deadline: now, Released: true, Generation: 5, UpdatedAt: now,
	}).Exec(ctx)
	r.NoError(err)

	stale := &upgradeSlotRow{RegionKey: "region", Generation: 4}
	won, err := h.handler.takeUpgradeSlot(ctx, stale, "region", "b", upgradeTestTarget, now)
	r.NoError(err)
	r.False(won, "a contender that read an older generation loses the compare-and-swap")

	// Inserting where a row already exists is also a loss, not an error.
	won, err = h.handler.takeUpgradeSlot(ctx, nil, "region", "b", upgradeTestTarget, now)
	r.NoError(err)
	r.False(won)

	fresh := &upgradeSlotRow{RegionKey: "region", Generation: 5}
	won, err = h.handler.takeUpgradeSlot(ctx, fresh, "region", "b", upgradeTestTarget, now)
	r.NoError(err)
	r.True(won)
}

func TestNextUpgradeCandidateOrderingAndPruning(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	h := newUpgradeHarness(t)
	ctx := t.Context()
	now := time.Now().UTC()
	h.handler.upgradeCheckInterval = time.Second

	insert := func(uid, target string, attempts int, seen, created time.Time) {
		_, err := h.svc.DB().NewInsert().Model(&upgradeCandidateRow{
			AgentUID: uid, RegionKey: "region", TargetVersion: target, Attempts: attempts,
			SeenAt: seen, CreatedAt: created,
		}).Exec(ctx)
		r.NoError(err)
	}

	insert("stale", upgradeTestTarget, 0, now.Add(-time.Hour), now.Add(-5*time.Hour))
	insert("old-target", "1.0.0", 0, now, now.Add(-5*time.Hour))
	insert("exhausted", upgradeTestTarget, maxUpgradeAttempts, now, now.Add(-5*time.Hour))
	insert("asked-once", upgradeTestTarget, 1, now, now.Add(-4*time.Hour))
	insert("waiting-longer", upgradeTestTarget, 0, now, now.Add(-3*time.Hour))
	insert("waiting-less", upgradeTestTarget, 0, now, now.Add(-2*time.Hour))
	insert("b-tie", upgradeTestTarget, 0, now, now.Add(-2*time.Hour))

	next, live, err := h.handler.nextUpgradeCandidate(ctx, "region", upgradeTestTarget, now)
	r.NoError(err)
	r.Equal("waiting-longer", next, "fewest attempts first, then the longest waiting")
	r.Equal(4, live)

	var remaining []upgradeCandidateRow

	r.NoError(h.svc.DB().NewSelect().Model(&remaining).Where("agent_uid = ?", "stale").Scan(ctx))
	r.Empty(remaining, "a candidate that stopped refreshing is dropped")

	// Equal attempts and creation time: the agent UID breaks the tie.
	_, err = h.svc.DB().NewDelete().Model((*upgradeCandidateRow)(nil)).
		Where("agent_uid IN (?, ?, ?, ?)", "asked-once", "waiting-longer", "old-target", "exhausted").Exec(ctx)
	r.NoError(err)

	next, _, err = h.handler.nextUpgradeCandidate(ctx, "region", upgradeTestTarget, now)
	r.NoError(err)
	r.Equal("b-tie", next)

	none, count, err := h.handler.nextUpgradeCandidate(ctx, "empty-region", upgradeTestTarget, now)
	r.NoError(err)
	r.Empty(none)
	r.Zero(count)
}

func TestCountActiveAgentsScopesToTheOwner(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	h := newUpgradeHarness(t)
	ctx := t.Context()

	system := &models.Agent{UID: uuid.NewString(), Kind: models.AgentKindSystem, Region: "eu"}

	count, err := h.handler.countActiveAgents(ctx, system)
	r.NoError(err)
	r.Zero(count)

	h.logs.Reset()
	h.handler.logUpgradeStart(ctx, newUpgradeState(system), upgradeTestTarget, 1)
	r.Contains(h.logs.String(), "asking the only agent of its region to upgrade")

	h.logs.Reset()
	h.handler.logUpgradeStart(ctx, newUpgradeState(system), upgradeTestTarget, 3)
	r.Contains(h.logs.String(), "asking agent to upgrade to the server version")
}

func TestCompleteUpgradeReleasesTheHeldSlot(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	h := newUpgradeHarness(t)
	ctx := t.Context()
	agent := newOrgAgent()
	now := time.Now().UTC()

	_, err := h.handler.touchCandidate(ctx, agent, upgradeTestTarget, now)
	r.NoError(err)

	_, err = h.svc.DB().NewInsert().Model(&upgradeSlotRow{
		RegionKey: upgradeRegionKey(agent), AgentUID: agent.UID, TargetVersion: upgradeTestTarget,
		Deadline: now.Add(time.Hour), UpdatedAt: now,
	}).Exec(ctx)
	r.NoError(err)

	h.handler.completeUpgrade(ctx, agent, upgradeTestTarget)

	slot, err := h.handler.loadUpgradeSlot(ctx, upgradeRegionKey(agent))
	r.NoError(err)
	r.True(slot.Released)
	r.Contains(h.logs.String(), "agent came back on the server version")

	cands, err := h.svc.DB().NewSelect().Model((*upgradeCandidateRow)(nil)).Count(ctx)
	r.NoError(err)
	r.Zero(cands)
}

func withTargetVersion(t *testing.T) {
	t.Helper()

	previous := version.Version
	version.Version = upgradeTestTarget

	t.Cleanup(func() { version.Version = previous })
}
