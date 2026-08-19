package agentws_test

import (
	"bytes"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/stretchr/testify/require"

	agentcrypto "github.com/fclairamb/solidping/server/internal/agents"
	"github.com/fclairamb/solidping/server/internal/version"
)

// enrollWithVersion is the shared env.enroll() handshake with one addition:
// the enroll frame carries a Version, exercising the path env.enroll() alone
// cannot — dialEnroll (ws.go) really does send Version on the enroll frame,
// but the shared test helper predates this spec and does not, so the tests
// that need it get their own copy rather than changing a helper every other
// test in this package also relies on. No caller here needs the agent keys
// back (nothing reconnects), so unlike env.enroll() this does not return them.
func enrollWithVersion(
	t *testing.T, e *env, token, name, ver string,
) (*websocket.Conn, agentcrypto.ServerFrame) {
	t.Helper()
	r := require.New(t)
	ctx := t.Context()

	keys, err := agentcrypto.GenerateAgentKeys()
	r.NoError(err)

	headers := http.Header{}
	headers.Set("Authorization", "Bearer "+token)

	conn, _, err := e.dial(headers)
	r.NoError(err)
	t.Cleanup(func() { _ = conn.CloseNow() })

	var hello agentcrypto.ServerFrame
	r.NoError(wsjson.Read(ctx, conn, &hello))
	r.Equal(agentcrypto.MsgTypeHello, hello.Type)

	r.NoError(wsjson.Write(ctx, conn, agentcrypto.ClientFrame{
		Type:             agentcrypto.MsgTypeEnroll,
		ID:               "enroll-1",
		Name:             name,
		Ed25519PublicKey: keys.Ed25519PublicKey,
		X25519PublicKey:  keys.X25519Recipient,
		Version:          ver,
	}))

	var enrolled agentcrypto.ServerFrame
	r.NoError(wsjson.Read(ctx, conn, &enrolled))
	r.Equal(agentcrypto.MsgTypeEnrolled, enrolled.Type)
	r.NotEmpty(enrolled.AgentUID)

	var identityHello agentcrypto.ServerFrame
	r.NoError(wsjson.Read(ctx, conn, &identityHello))
	r.Equal(agentcrypto.MsgTypeHello, identityHello.Type)

	return conn, enrolled
}

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

// TestEnrollReportsVersionToWorkerRowBeforeAnyClaim closes the audit gap: the
// enroll frame is the agent's FIRST report of its build version (dialEnroll
// sends it, ws.go:622), and it must land on the worker row from the enroll
// handshake alone — a claim frame is throttled at 50s, and an enroll-only
// connection would otherwise never report at all.
func TestEnrollReportsVersionToWorkerRowBeforeAnyClaim(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	e := newEnv(t)

	_, enrolled := enrollWithVersion(t, e, e.mintToken(), "dc1-agent", "0.17.0")

	worker := capWorker(t, e, enrolled.AgentUID)
	r.NotNil(worker.Version, "the enroll frame's version must be stored without needing a claim first")
	r.Equal("0.17.0", *worker.Version)
}

// TestEnrollWithoutVersionLeavesWorkerRowVersionNil is the negative control
// for the test above: an agent that omits the field at enroll (an older
// build, or one that simply has nothing to report yet) must leave the column
// NULL — never an empty string masquerading as a version.
func TestEnrollWithoutVersionLeavesWorkerRowVersionNil(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	e := newEnv(t)

	_, _, enrolled := e.enroll(e.mintToken(), "dc1-agent")

	worker := capWorker(t, e, enrolled.AgentUID)
	r.Nil(worker.Version, "an agent that reports nothing at enroll must leave the column NULL, not ''")
}

// TestVersionDriftWarnsOnceAcrossEnrollThenClaim extends the once-per-
// connection guard to the sequence the real wire protocol actually produces:
// dialEnroll sends Version on the enroll frame AND on every claim, so a
// freshly-enrolled, already-drifted agent's very first claim must not warn a
// second time on top of the WARN enrollment itself already logged.
//
//nolint:paralleltest // mutates process-wide slog.Default() and version.Version
func TestVersionDriftWarnsOnceAcrossEnrollThenClaim(t *testing.T) {
	withServerVersion(t, "1.0.0")
	logs := captureLogs(t)

	r := require.New(t)
	e := newEnv(t)

	conn, _ := enrollWithVersion(t, e, e.mintToken(), "dc1-agent", "2.0.0")

	// The enroll alone must have warned already.
	r.Equal(1, strings.Count(logs.String(), "agent build version differs"),
		"enrolling with a drifted version must warn immediately, not wait for a claim")

	for i := range 3 {
		roundTrip(t, conn, agentcrypto.ClientFrame{
			Type: agentcrypto.MsgTypeClaim, ID: fmt.Sprintf("c%d", i+1), MaxJobs: 5,
			Version: "2.0.0",
		})
	}

	r.Equal(1, strings.Count(logs.String(), "agent build version differs"),
		"claims on the same connection must not warn again once enrollment already checked")
}

// TestVersionDriftWarnsOnClaimWhenEnrollOmittedVersion is the other half of
// the enroll/claim interaction: when the enroll frame carried no version at
// all (nothing to compare), the version-checked guard must NOT be
// pre-emptively latched — the first claim that actually reports one still
// gets its chance to warn.
//
//nolint:paralleltest // mutates process-wide slog.Default() and version.Version
func TestVersionDriftWarnsOnClaimWhenEnrollOmittedVersion(t *testing.T) {
	withServerVersion(t, "1.0.0")
	logs := captureLogs(t)

	r := require.New(t)
	e := newEnv(t)

	conn, _, _ := e.enroll(e.mintToken(), "dc1-agent")
	r.NotContains(logs.String(), "agent build version differs",
		"enrolling with no version at all must not warn — there is nothing to compare")

	roundTrip(t, conn, agentcrypto.ClientFrame{
		Type: agentcrypto.MsgTypeClaim, ID: "c1", MaxJobs: 5, Version: "2.0.0",
	})

	r.Equal(1, strings.Count(logs.String(), "agent build version differs"),
		"the first claim must still get its chance to warn when enrollment had nothing to check")
}
