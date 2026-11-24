package agentws_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	agentcrypto "github.com/fclairamb/solidping/server/internal/agents"
)

// Agents older than the rolling upgrade send no autoUpgrade field and do not
// know the upgrade frame: they ignore it, so asking them would only burn their
// attempts and hold their region's slot for the whole timeout.

// connectLegacy enrolls an agent on e and reports ver on a first claim
// without the autoUpgrade field, like an agent predating the rolling upgrade.
func connectLegacy(t *testing.T, e *env, name, ver string) (string, *frameWatcher) {
	t.Helper()

	conn, _, enrolled := e.enroll(e.mintToken(), name)
	resp := roundTrip(t, conn, agentcrypto.ClientFrame{
		Type: agentcrypto.MsgTypeClaim, ID: "claim-" + name, MaxJobs: 1, Version: ver,
	})
	require.Equal(t, agentcrypto.MsgTypeJobs, resp.Type)

	return enrolled.AgentUID, watchFrames(t, conn)
}

// TestAgentPredatingAutoUpgradeIsNeverAsked: a drifted agent whose claim has
// no autoUpgrade field gets no upgrade frame; it is logged once, at INFO, as
// needing a manual upgrade, and is not recorded as pinned.
//
//nolint:paralleltest // mutates version.Version and slog.Default()
func TestAgentPredatingAutoUpgradeIsNeverAsked(t *testing.T) {
	withServerVersion(t, upgradeServer)
	logs := captureLogs(t)

	r := require.New(t)
	e := newEnvConfigured(t, nil, fastUpgrades(time.Minute))
	drainOnCleanup(t, e)

	agentUID, watch := connectLegacy(t, e, "dc1-legacy", upgradeDrifted)

	_, ok := watch.gotUpgrade(quietWindow)
	r.False(ok, "an agent predating auto-upgrade is never sent an upgrade frame")
	r.False(workerPinned(t, e, agentUID), "it is not pinned, only older")

	out := logs.String()
	r.Contains(out, "agent predates auto-upgrade: upgrade it by hand")
	r.NotContains(out, "asking the only agent of its region to upgrade")
}

// TestAgentPredatingAutoUpgradeDoesNotHoldUpItsRegion: the older agent
// connected first (it would have been next in line), yet the agent that knows
// the upgrade frame is asked right away.
//
//nolint:paralleltest // mutates version.Version
func TestAgentPredatingAutoUpgradeDoesNotHoldUpItsRegion(t *testing.T) {
	withServerVersion(t, upgradeServer)

	r := require.New(t)
	e := newEnvConfigured(t, nil, fastUpgrades(time.Minute))
	drainOnCleanup(t, e)

	_, watchLegacy := connectLegacy(t, e, "dc1-legacy", upgradeDrifted)
	connOther, _, _ := connectReporting(t, e, "dc1-other", upgradeDrifted)

	frame, ok := watchFrames(t, connOther).gotUpgrade(frameWait)
	r.True(ok, "the newer agent is asked without waiting on the older one")
	r.Equal(upgradeServer, frame.TargetVersion)

	_, ok = watchLegacy.gotUpgrade(quietWindow)
	r.False(ok, "the older agent is never asked")
}
