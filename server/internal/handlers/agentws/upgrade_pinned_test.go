package agentws_test

import (
	"net/http"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/stretchr/testify/require"

	agentcrypto "github.com/fclairamb/solidping/server/internal/agents"
	"github.com/fclairamb/solidping/server/internal/db/models"
)

// Agents pinned with SP_AGENT_IGNORE_VERSION_DRIFT=true announce
// autoUpgrade: false: the server never asks them to upgrade and skips them in
// the rollout (spec 2026-11-22-04).

// pinnedClaim is a claim reporting ver with autoUpgrade: false.
func pinnedClaim(id, ver string) agentcrypto.ClientFrame {
	follow := false

	return agentcrypto.ClientFrame{
		Type: agentcrypto.MsgTypeClaim, ID: id, MaxJobs: 1, Version: ver, AutoUpgrade: &follow,
	}
}

// connectPinned enrolls an agent on e and reports ver, pinned, on a first claim.
func connectPinned(
	t *testing.T, e *env, name, ver string,
) (*websocket.Conn, *agentcrypto.AgentKeys, agentcrypto.ServerFrame) {
	t.Helper()

	conn, keys, enrolled := e.enroll(e.mintToken(), name)
	resp := roundTrip(t, conn, pinnedClaim("claim-"+name, ver))
	require.Equal(t, agentcrypto.MsgTypeJobs, resp.Type)

	return conn, keys, enrolled
}

// workerPinned reads the agent's workers.version_pinned.
func workerPinned(t *testing.T, e *env, agentUID string) bool {
	t.Helper()

	worker := new(models.Worker)
	require.NoError(t, e.dbSvc.DB().NewSelect().Model(worker).
		Where("slug = ?", agentcrypto.WorkerSlug(agentUID)).Scan(t.Context()))

	return worker.VersionPinned
}

// TestPinnedAgentIsNeverAskedToUpgrade: a drifted agent announcing
// autoUpgrade: false gets no upgrade frame, and its pin is stored for the
// dashboard. The drift WARN stays.
//
//nolint:paralleltest // mutates version.Version and slog.Default()
func TestPinnedAgentIsNeverAskedToUpgrade(t *testing.T) {
	withServerVersion(t, upgradeServer)
	logs := captureLogs(t)

	r := require.New(t)
	e := newEnvConfigured(t, nil, fastUpgrades(time.Minute))
	drainOnCleanup(t, e)

	conn, _, enrolled := connectPinned(t, e, "dc1-pinned", upgradeDrifted)

	_, ok := watchFrames(t, conn).gotUpgrade(quietWindow)
	r.False(ok, "a pinned agent is never asked to upgrade")
	r.True(workerPinned(t, e, enrolled.AgentUID), "the pin is stored on the workers row")

	out := logs.String()
	r.Contains(out, "agent build version differs from this server", "the drift WARN stays")
	r.Contains(out, "agent is pinned to its version")
}

// TestPinnedAgentOnEnrollFrame: the pin rides the enroll frame too; an agent
// that enrolls pinned and drifted is not asked before its first claim.
//
//nolint:paralleltest // mutates version.Version
func TestPinnedAgentOnEnrollFrame(t *testing.T) {
	withServerVersion(t, upgradeServer)

	r := require.New(t)
	e := newEnvConfigured(t, nil, fastUpgrades(time.Minute))
	drainOnCleanup(t, e)
	ctx := t.Context()

	keys, err := agentcrypto.GenerateAgentKeys()
	r.NoError(err)

	conn, _, err := e.dial(http.Header{"Authorization": []string{"Bearer " + e.mintToken()}})
	r.NoError(err)
	t.Cleanup(func() { _ = conn.CloseNow() })

	var hello agentcrypto.ServerFrame
	r.NoError(wsjson.Read(ctx, conn, &hello))

	follow := false
	r.NoError(wsjson.Write(ctx, conn, agentcrypto.ClientFrame{
		Type: agentcrypto.MsgTypeEnroll, ID: "enroll-1", Name: "dc1-pinned",
		Ed25519PublicKey: keys.Ed25519PublicKey, X25519PublicKey: keys.X25519Recipient,
		Version: upgradeDrifted, AutoUpgrade: &follow,
	}))

	var enrolled agentcrypto.ServerFrame
	r.NoError(wsjson.Read(ctx, conn, &enrolled))
	r.Equal(agentcrypto.MsgTypeEnrolled, enrolled.Type)

	_, ok := watchFrames(t, conn).gotUpgrade(quietWindow)
	r.False(ok, "an agent pinned on its enroll frame is not asked")
	r.True(workerPinned(t, e, enrolled.AgentUID))
}

// TestPinnedAgentDoesNotHoldUpItsRegion: in a region with a pinned and an
// unpinned drifted agent, the unpinned one is asked right away, even though
// the pinned one connected first (it would have been next in line).
//
//nolint:paralleltest // mutates version.Version and slog.Default()
func TestPinnedAgentDoesNotHoldUpItsRegion(t *testing.T) {
	withServerVersion(t, upgradeServer)

	r := require.New(t)
	e := newEnvConfigured(t, nil, fastUpgrades(time.Minute))
	drainOnCleanup(t, e)

	connPinned, _, _ := connectPinned(t, e, "dc1-pinned", upgradeDrifted)
	connOther, _, _ := connectReporting(t, e, "dc1-other", upgradeDrifted)

	watchPinned, watchOther := watchFrames(t, connPinned), watchFrames(t, connOther)

	frame, ok := watchOther.gotUpgrade(frameWait)
	r.True(ok, "the unpinned agent is asked without waiting on the pinned one")
	r.Equal(upgradeServer, frame.TargetVersion)

	_, ok = watchPinned.gotUpgrade(quietWindow)
	r.False(ok, "the pinned agent is never asked")
}

// TestAgentPinnedAfterBeingAskedFreesTheSlot: an agent asked to upgrade that
// restarts pinned (still drifted) gives its region's slot back at once: the
// next agent is asked without waiting for the upgrade timeout.
//
//nolint:paralleltest // mutates version.Version and slog.Default()
func TestAgentPinnedAfterBeingAskedFreesTheSlot(t *testing.T) {
	withServerVersion(t, upgradeServer)

	r := require.New(t)
	e := newEnvConfigured(t, nil, fastUpgrades(time.Hour))
	drainOnCleanup(t, e)

	connA, keysA, enrolledA := connectReporting(t, e, "dc1-a", upgradeDrifted)
	connB, _, _ := connectReporting(t, e, "dc1-b", upgradeDrifted)

	watchB := watchFrames(t, connB)

	_, ok := watchFrames(t, connA).gotUpgrade(frameWait)
	r.True(ok, "A is asked first")

	_, ok = watchB.gotUpgrade(quietWindow)
	r.False(ok, "B waits while A holds the slot")

	// A restarts with SP_AGENT_IGNORE_VERSION_DRIFT=true, on the old image.
	_ = connA.Close(websocket.StatusNormalClosure, "restarting pinned")
	connA2 := reconnect(t, e, keysA, enrolledA.AgentUID, "nonce-a-pinned")
	resp := roundTrip(t, connA2, pinnedClaim("claim-a-pinned", upgradeDrifted))
	r.Equal(agentcrypto.MsgTypeJobs, resp.Type)

	watchA2 := watchFrames(t, connA2)

	_, ok = watchB.gotUpgrade(frameWait)
	r.True(ok, "B is asked as soon as A is pinned, not after the timeout")

	_, ok = watchA2.gotUpgrade(quietWindow)
	r.False(ok, "the pinned A is not asked again")
}
