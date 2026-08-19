package agentws_test

import (
	"bytes"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"github.com/coder/websocket"
	"github.com/stretchr/testify/require"

	agentcrypto "github.com/fclairamb/solidping/server/internal/agents"
	"github.com/fclairamb/solidping/server/internal/version"
)

// withServerVersion pins version.Get().Version for the duration of the test
// and restores it on cleanup. Tests using this are deliberately NOT
// t.Parallel(): the package-level version.Version var is process-wide state,
// same reasoning as the entitlements warn-once tests.
func withServerVersion(t *testing.T, v string) {
	t.Helper()

	previous := version.Version
	version.Version = v
	t.Cleanup(func() { version.Version = previous })
}

// captureLogs redirects slog.Default() to a buffer for the duration of the
// test. MUST be called BEFORE constructing the handler under test — NewHandler
// captures slog.Default() once, at construction time, into Handler.logger.
func captureLogs(t *testing.T) *bytes.Buffer {
	t.Helper()

	buf := &bytes.Buffer{}
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(previous) })

	return buf
}

// TestVersionDriftWarnsOncePerConnection is the core proof for spec
// 2026-08-19-07's WARN: a claim frame reporting a version that differs from
// this server's own logs exactly once, no matter how many claims land on the
// SAME connection — a busy agent claiming several times a second must not
// spam the log once per claim.
//
//nolint:paralleltest // mutates process-wide slog.Default() and version.Version
func TestVersionDriftWarnsOncePerConnection(t *testing.T) {
	withServerVersion(t, "1.0.0")
	logs := captureLogs(t)

	r := require.New(t)
	e := newEnv(t)

	conn, _, _ := e.enroll(e.mintToken(), "dc1-agent")

	for i := range 3 {
		resp := roundTrip(t, conn, agentcrypto.ClientFrame{
			Type: agentcrypto.MsgTypeClaim, ID: fmt.Sprintf("c%d", i+1), MaxJobs: 5,
			Version: "2.0.0",
		})
		r.Equal(agentcrypto.MsgTypeJobs, resp.Type)
	}

	out := logs.String()
	r.Equal(1, strings.Count(out, "agent build version differs"),
		"the drift WARN must fire once per connection, not once per claim")
	r.Contains(out, "2.0.0")
	r.Contains(out, "1.0.0")
}

// TestVersionDriftWarnsAgainOnNewConnection: the "once" in "once per
// connection" is per CONNECTION, not per process — a fresh connection from
// the same (still-drifted) agent gets its own warning.
//
//nolint:paralleltest // mutates process-wide slog.Default() and version.Version
func TestVersionDriftWarnsAgainOnNewConnection(t *testing.T) {
	withServerVersion(t, "1.0.0")
	logs := captureLogs(t)

	r := require.New(t)
	e := newEnv(t)

	conn, keys, enrolled := e.enroll(e.mintToken(), "dc1-agent")
	roundTrip(t, conn, agentcrypto.ClientFrame{
		Type: agentcrypto.MsgTypeClaim, ID: "c1", MaxJobs: 5, Version: "2.0.0",
	})
	_ = conn.Close(websocket.StatusNormalClosure, "bye")

	conn2 := reconnect(t, e, keys, enrolled.AgentUID, "nonce-drift-2")
	roundTrip(t, conn2, agentcrypto.ClientFrame{
		Type: agentcrypto.MsgTypeClaim, ID: "c2", MaxJobs: 5, Version: "2.0.0",
	})

	r.Equal(2, strings.Count(logs.String(), "agent build version differs"),
		"a fresh connection must log its own drift warning")
}

// TestNoVersionDriftWarnWhenMatching is the negative control: identical
// versions on both sides must never warn.
//
//nolint:paralleltest // mutates process-wide slog.Default() and version.Version
func TestNoVersionDriftWarnWhenMatching(t *testing.T) {
	withServerVersion(t, "1.0.0")
	logs := captureLogs(t)

	e := newEnv(t)
	conn, _, _ := e.enroll(e.mintToken(), "dc1-agent")

	roundTrip(t, conn, agentcrypto.ClientFrame{
		Type: agentcrypto.MsgTypeClaim, ID: "c1", MaxJobs: 5, Version: "1.0.0",
	})

	require.NotContains(t, logs.String(), "agent build version differs")
}

// TestNoVersionDriftWarnForDevServer and TestNoVersionDriftWarnForDevAgent are
// the two halves of "skip entirely when either side is a dev/untagged
// build" — without this, every local dev loop and CI run would warn on every
// agent connection.
//
//nolint:paralleltest // mutates process-wide slog.Default() and version.Version
func TestNoVersionDriftWarnForDevServer(t *testing.T) {
	withServerVersion(t, "dev") // the package default — an unset ldflags build
	logs := captureLogs(t)

	e := newEnv(t)
	conn, _, _ := e.enroll(e.mintToken(), "dc1-agent")

	roundTrip(t, conn, agentcrypto.ClientFrame{
		Type: agentcrypto.MsgTypeClaim, ID: "c1", MaxJobs: 5, Version: "2.0.0",
	})

	require.NotContains(t, logs.String(), "agent build version differs")
}

//nolint:paralleltest // mutates process-wide slog.Default() and version.Version
func TestNoVersionDriftWarnForDevAgent(t *testing.T) {
	withServerVersion(t, "1.0.0")
	logs := captureLogs(t)

	e := newEnv(t)
	conn, _, _ := e.enroll(e.mintToken(), "dc1-agent")

	roundTrip(t, conn, agentcrypto.ClientFrame{
		Type: agentcrypto.MsgTypeClaim, ID: "c1", MaxJobs: 5, Version: "dev",
	})

	require.NotContains(t, logs.String(), "agent build version differs")
}

// TestNoVersionDriftWarnWhenUnreported: an agent predating version reporting
// sends no version at all — decodes to "", the same sentinel as "not
// reported" everywhere else on this path — and must not warn (there is
// nothing to compare against).
//
//nolint:paralleltest // mutates process-wide slog.Default() and version.Version
func TestNoVersionDriftWarnWhenUnreported(t *testing.T) {
	withServerVersion(t, "1.0.0")
	logs := captureLogs(t)

	e := newEnv(t)
	conn, _, _ := e.enroll(e.mintToken(), "dc1-agent")

	roundTrip(t, conn, agentcrypto.ClientFrame{
		Type: agentcrypto.MsgTypeClaim, ID: "c1", MaxJobs: 5,
	})

	require.NotContains(t, logs.String(), "agent build version differs")
}

// TestClaimReportsVersionToWorkerRow is the end-to-end proof (D-bis) that a
// version reported on a claim frame actually lands on the worker row through
// the throttled recordAgentEgress path — the exact same wiring capabilities
// already prove elsewhere in this package.
func TestClaimReportsVersionToWorkerRow(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	e := newEnv(t)

	conn, _, enrolled := e.enroll(e.mintToken(), "dc1-agent")

	resp := roundTrip(t, conn, agentcrypto.ClientFrame{
		Type: agentcrypto.MsgTypeClaim, ID: "c1", MaxJobs: 5, Version: "0.17.0",
	})
	r.Equal(agentcrypto.MsgTypeJobs, resp.Type)

	worker := capWorker(t, e, enrolled.AgentUID)
	r.NotNil(worker.Version)
	r.Equal("0.17.0", *worker.Version)
}

// TestClaimWithoutVersionLeavesTheStoredValueIntact mirrors
// TestClaimWithoutCapabilitiesLeavesTheStoredSetIntact for version: a claim
// that reports nothing must never downgrade a known version back to unknown
// (nil never overwrites a known answer with a guess).
func TestClaimWithoutVersionLeavesTheStoredValueIntact(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	e := newEnv(t)

	conn, keys, enrolled := e.enroll(e.mintToken(), "dc1-agent")

	roundTrip(t, conn, agentcrypto.ClientFrame{
		Type: agentcrypto.MsgTypeClaim, ID: "c1", MaxJobs: 5, Version: "0.17.0",
	})
	r.Equal("0.17.0", *capWorker(t, e, enrolled.AgentUID).Version)

	_ = conn.Close(websocket.StatusNormalClosure, "bye")

	// An agent predating the field, or one that simply omitted it, sends no
	// version key whatsoever.
	conn2 := reconnect(t, e, keys, enrolled.AgentUID, "nonce-no-version")
	stale := makeWorkerStale(t, e, enrolled.AgentUID)

	roundTrip(t, conn2, agentcrypto.ClientFrame{
		Type: agentcrypto.MsgTypeClaim, ID: "c2", MaxJobs: 5,
	})

	after := capWorker(t, e, enrolled.AgentUID)
	r.NotNil(after.Version, "an unreported version must not erase a previously known one")
	r.Equal("0.17.0", *after.Version)
	r.NotNil(after.LastActiveAt)
	r.True(after.LastActiveAt.After(stale.Add(staleMark/2)),
		"the heartbeat must still land even though no version was reported")
}
