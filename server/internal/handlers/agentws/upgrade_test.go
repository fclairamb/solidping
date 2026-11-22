package agentws_test

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/stretchr/testify/require"

	agentcrypto "github.com/fclairamb/solidping/server/internal/agents"
	"github.com/fclairamb/solidping/server/internal/checkworker/checkjobsvc"
	"github.com/fclairamb/solidping/server/internal/checkworker/scheduling"
	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/handlers/agentws"
	"github.com/fclairamb/solidping/server/internal/handlers/incidents"
	"github.com/fclairamb/solidping/server/internal/handlers/workers"
	"github.com/fclairamb/solidping/server/internal/httpx"
	"github.com/fclairamb/solidping/server/internal/jobs/jobsvc"
	"github.com/fclairamb/solidping/server/internal/notifier"
	"github.com/fclairamb/solidping/server/internal/utils/clock"
)

// Rolling agent upgrade (spec 2026-11-22-04).

const (
	upgradeTick    = 20 * time.Millisecond
	upgradeServer  = "1.2.3"
	upgradeDrifted = "1.2.0"
	// quietWindow is how long a test watches for an upgrade frame that must
	// not come: many ticks.
	quietWindow = 400 * time.Millisecond
	frameWait   = 5 * time.Second
)

// fastUpgrades tunes a handler's rollout to test speed.
func fastUpgrades(timeout time.Duration) func(*agentws.Handler) {
	return func(h *agentws.Handler) { h.SetAutoUpgradeTimings(upgradeTick, timeout) }
}

// newHandlerEnv starts another handler (another "replica") over an existing
// env's database, so two replicas share one coordination table.
func newHandlerEnv(t *testing.T, base *env, cfg *config.Config, configure func(*agentws.Handler)) *env {
	t.Helper()

	events := notifier.NewLocalEventNotifier()
	t.Cleanup(func() { _ = events.Close() })

	checkJobSvc := checkjobsvc.NewService(base.dbSvc.DB())
	jobs := jobsvc.NewService(base.dbSvc.DB(), base.dbSvc, events, nil)
	workersSvc := workers.NewService(
		base.dbSvc, checkJobSvc, incidents.NewService(base.dbSvc, jobs, clock.Real{}, nil), scheduling.Params{},
	)

	handler := agentws.NewHandler(cfg, base.dbSvc, checkJobSvc, workersSvc, nil, events, nil, nil)
	if configure != nil {
		configure(handler)
	}

	router := httpx.New()
	router.GET("/api/v1/agent/ws", handler.Serve)

	server := httptest.NewServer(router)
	t.Cleanup(server.Close)

	return &env{t: t, dbSvc: base.dbSvc, server: server, org: base.org, handler: handler}
}

// frameWatcher drains one connection's frames from a goroutine (which also
// answers the server's keepalive pings) and records the upgrade frames.
type frameWatcher struct {
	upgrades chan agentcrypto.ServerFrame
}

func watchFrames(t *testing.T, conn *websocket.Conn) *frameWatcher {
	t.Helper()

	watcher := &frameWatcher{upgrades: make(chan agentcrypto.ServerFrame, 8)}

	go func() {
		for {
			var frame agentcrypto.ServerFrame
			if err := wsjson.Read(context.Background(), conn, &frame); err != nil {
				return
			}

			if frame.Type == agentcrypto.MsgTypeUpgrade {
				watcher.upgrades <- frame
			}
		}
	}()

	return watcher
}

func (w *frameWatcher) gotUpgrade(within time.Duration) (agentcrypto.ServerFrame, bool) {
	select {
	case frame := <-w.upgrades:
		return frame, true
	case <-time.After(within):
		return agentcrypto.ServerFrame{}, false
	}
}

// connectReporting enrolls an agent on e and reports ver on a first claim.
func connectReporting(
	t *testing.T, e *env, name, ver string,
) (*websocket.Conn, *agentcrypto.AgentKeys, agentcrypto.ServerFrame) {
	t.Helper()

	conn, keys, enrolled := e.enroll(e.mintToken(), name)
	resp := roundTrip(t, conn, agentcrypto.ClientFrame{
		Type: agentcrypto.MsgTypeClaim, ID: "claim-" + name, MaxJobs: 1, Version: ver,
	})
	require.Equal(t, agentcrypto.MsgTypeJobs, resp.Type)

	return conn, keys, enrolled
}

// reconnectReporting reconnects an enrolled agent and reports ver.
func reconnectReporting(
	t *testing.T, e *env, keys *agentcrypto.AgentKeys, agentUID, nonce, ver string,
) *websocket.Conn {
	t.Helper()

	conn := reconnect(t, e, keys, agentUID, nonce)
	resp := roundTrip(t, conn, agentcrypto.ClientFrame{
		Type: agentcrypto.MsgTypeClaim, ID: "claim-" + nonce, MaxJobs: 1, Version: ver,
	})
	require.Equal(t, agentcrypto.MsgTypeJobs, resp.Type)

	return conn
}

//nolint:paralleltest // mutates version.Version and slog.Default()
func TestDriftedAgentIsAskedToUpgrade(t *testing.T) {
	withServerVersion(t, upgradeServer)
	logs := captureLogs(t)

	r := require.New(t)
	e := newEnvConfigured(t, nil, fastUpgrades(time.Minute))

	conn, _, _ := connectReporting(t, e, "dc1-a", upgradeDrifted)

	frame, ok := watchFrames(t, conn).gotUpgrade(frameWait)
	r.True(ok, "a drifted agent is asked to upgrade")
	r.Equal(upgradeServer, frame.TargetVersion)

	// The only agent of its region is still upgraded, and it is said at INFO.
	r.Contains(logs.String(), "asking the only agent of its region to upgrade")
}

//nolint:paralleltest // mutates version.Version
func TestNoUpgradeWithoutDrift(t *testing.T) {
	cases := []struct {
		name          string
		serverVersion string
		agentVersion  string
		disabled      bool
	}{
		{name: "same version", serverVersion: upgradeServer, agentVersion: upgradeServer},
		{name: "dev server", serverVersion: "dev", agentVersion: upgradeDrifted},
		{name: "dev agent", serverVersion: upgradeServer, agentVersion: "dev"},
		{name: "unreported", serverVersion: upgradeServer, agentVersion: ""},
		{name: "SP_AGENTS_AUTO_UPGRADE=false", serverVersion: upgradeServer, agentVersion: upgradeDrifted, disabled: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			withServerVersion(t, tc.serverVersion)

			base := newEnv(t)

			cfg := &config.Config{}
			cfg.Agents.AutoUpgradeDisabled = tc.disabled
			e := newHandlerEnv(t, base, cfg, fastUpgrades(time.Minute))

			conn, _, _ := connectReporting(t, e, "dc1-a", tc.agentVersion)

			_, ok := watchFrames(t, conn).gotUpgrade(quietWindow)
			require.False(t, ok, "no upgrade frame expected")
		})
	}
}

// TestUpgradesOneAgentPerRegionAtATime: two drifted agents of one region, on
// two handler instances sharing one database (two replicas), are asked one
// after the other: the second only once the first came back on the server's
// version.
//
//nolint:paralleltest // mutates version.Version and slog.Default()
func TestUpgradesOneAgentPerRegionAtATime(t *testing.T) {
	withServerVersion(t, upgradeServer)
	logs := captureLogs(t)

	r := require.New(t)
	replicaA := newEnvConfigured(t, nil, fastUpgrades(time.Minute))
	replicaB := newHandlerEnv(t, replicaA, &config.Config{}, fastUpgrades(time.Minute))

	connA, keysA, enrolledA := connectReporting(t, replicaA, "dc1-a", upgradeDrifted)
	connB, _, _ := connectReporting(t, replicaB, "dc1-b", upgradeDrifted)

	watchA, watchB := watchFrames(t, connA), watchFrames(t, connB)

	// A registered first (same attempts, older): A is asked, B waits.
	_, ok := watchA.gotUpgrade(frameWait)
	r.True(ok, "the first drifted agent is asked")

	_, ok = watchB.gotUpgrade(quietWindow)
	r.False(ok, "the second agent must wait for the first to come back")
	r.NotContains(logs.String(), "asking the only agent of its region")

	// A restarts on the server's version, through the OTHER replica.
	_ = connA.Close(websocket.StatusNormalClosure, "upgrading")
	reconnectReporting(t, replicaB, keysA, enrolledA.AgentUID, "nonce-a-back", upgradeServer)

	frame, ok := watchB.gotUpgrade(frameWait)
	r.True(ok, "once A is back, B is asked")
	r.Equal(upgradeServer, frame.TargetVersion)
	r.Contains(logs.String(), "agent came back on the server version")
}

// TestUpgradeTimeoutMovesOn: an agent that does not come back within the
// timeout is logged at WARN and the next agent is asked — and when it
// reconnects still drifted, it is not asked again before the others.
//
//nolint:paralleltest // mutates version.Version and slog.Default()
func TestUpgradeTimeoutMovesOn(t *testing.T) {
	withServerVersion(t, upgradeServer)
	logs := captureLogs(t)

	r := require.New(t)
	timeout := 300 * time.Millisecond
	e := newEnvConfigured(t, nil, fastUpgrades(timeout))

	connA, keysA, enrolledA := connectReporting(t, e, "dc1-a", upgradeDrifted)

	_, ok := watchFrames(t, connA).gotUpgrade(frameWait)
	r.True(ok, "A is asked first")

	// A restarts on the SAME old image (a restart policy that reuses the local
	// image), then B connects. B was never asked: B goes next.
	_ = connA.Close(websocket.StatusNormalClosure, "restarting")
	connA2 := reconnectReporting(t, e, keysA, enrolledA.AgentUID, "nonce-a-again", upgradeDrifted)
	connB, _, _ := connectReporting(t, e, "dc1-b", upgradeDrifted)

	watchA2, watchB := watchFrames(t, connA2), watchFrames(t, connB)

	_, ok = watchB.gotUpgrade(frameWait)
	r.True(ok, "after A's timeout, B is asked")

	_, ok = watchA2.gotUpgrade(timeout / 2)
	r.False(ok, "A is not retried while B holds the slot")

	out := logs.String()
	r.Contains(out, "within the upgrade timeout")

	timeoutLine := ""

	for line := range strings.SplitSeq(out, "\n") {
		if strings.Contains(line, "within the upgrade timeout") {
			timeoutLine = line
		}
	}

	r.Contains(timeoutLine, enrolledA.AgentUID, "the WARN names the agent that did not come back")
	r.Contains(timeoutLine, "level=WARN")
}
