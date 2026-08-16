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

// staleMark is how far back the worker row's last_active_at is pushed between
// claims, so "the heartbeat still landed" is an unambiguous assertion rather
// than a clock-resolution guess.
const staleMark = time.Hour

// capWorker reads the agent's worker row.
func capWorker(t *testing.T, e *env, agentUID string) *models.Worker {
	t.Helper()

	var row models.Worker
	require.NoError(t, e.dbSvc.DB().NewSelect().Model(&row).
		Where("slug = ?", agentcrypto.WorkerSlug(agentUID)).
		Scan(t.Context()))

	return &row
}

// makeWorkerStale pushes last_active_at back so the next claim's heartbeat is
// visibly a write rather than a no-op.
//
// CALL IT AFTER THE CONNECTION IS UP, NEVER BEFORE. Connecting runs
// ensureWorkerRow → RegisterOrUpdateWorker, which refreshes last_active_at on
// its own; staling ahead of the dial would let the CONNECT satisfy the liveness
// assertion and the test would pass even with recordAgentEgress broken.
func makeWorkerStale(t *testing.T, e *env, agentUID string) time.Time {
	t.Helper()

	stale := time.Now().Add(-staleMark)

	_, err := e.dbSvc.DB().NewUpdate().
		Model((*models.Worker)(nil)).
		Set("last_active_at = ?", stale).
		Where("slug = ?", agentcrypto.WorkerSlug(agentUID)).
		Exec(t.Context())
	require.NoError(t, err)

	return stale
}

// reconnect dials a fresh signed connection for an already-enrolled agent.
//
// A NEW CONNECTION IS THE POINT: recordAgentEgress throttles itself to one
// write per agentEgressReportInterval per connection state, so a second claim
// on the SAME socket would be silently skipped and the test would assert
// nothing at all.
func reconnect(t *testing.T, e *env, keys *agentcrypto.AgentKeys, agentUID, nonce string) *websocket.Conn {
	t.Helper()

	r := require.New(t)

	timestamp := time.Now().UTC().Format(time.RFC3339)
	signature, err := keys.Sign(http.MethodGet, "/api/v1/agent/ws", timestamp, nonce)
	r.NoError(err)

	headers := http.Header{}
	headers.Set("X-Sp-Agent-Uid", agentUID)
	headers.Set("X-Sp-Timestamp", timestamp)
	headers.Set("X-Sp-Nonce", nonce)
	headers.Set("X-Sp-Signature", signature)

	conn, _, err := e.dial(headers)
	r.NoError(err)

	t.Cleanup(func() { _ = conn.CloseNow() })

	var hello agentcrypto.ServerFrame
	r.NoError(wsjson.Read(t.Context(), conn, &hello))
	r.Equal(agentcrypto.MsgTypeHello, hello.Type)

	return conn
}

// TestMalformedCapabilitySetNeverBlanksAStoredSetOrTheHeartbeat is the
// end-to-end guard for the liveness fix in recordAgentEgress.
//
// A capability set is untrusted remote input riding the frame that ALSO
// refreshes last_active_at. Two failures have to be impossible at once:
//
//  1. the malformed set must not reach the CHECK-constrained column and fail
//     the whole UPDATE — that would freeze last_active_at and let the region go
//     stale while the agent is demonstrably alive and claiming; and
//  2. the recovery must be "not reported" (nil), NOT an empty set — swapping
//     nil for []string{} here would silently rewrite a known set to "I have
//     none", turning every capability into a hard "no".
//
// So this asserts the STORED value, not merely that the call was made.
func TestMalformedCapabilitySetNeverBlanksAStoredSetOrTheHeartbeat(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	e := newEnv(t)

	conn, keys, enrolled := e.enroll(e.mintToken(), "dc1-agent")

	// A first claim establishes a known-good set on the worker row.
	resp := roundTrip(t, conn, agentcrypto.ClientFrame{
		Type: agentcrypto.MsgTypeClaim, ID: "c1", MaxJobs: 5,
		Capabilities: []string{models.CapabilityIPv4, models.CapabilityIPv6},
	})
	r.Equal(agentcrypto.MsgTypeJobs, resp.Type)
	r.Equal([]string{"ipv4", "ipv6"}, capWorker(t, e, enrolled.AgentUID).Capabilities)

	_ = conn.Close(websocket.StatusNormalClosure, "bye")

	// Reconnect and claim with a set the database cannot store. `null` is the
	// nastiest case on purpose: Postgres quotes any element equal to "NULL"
	// case-insensitively, so its CHECK refuses the name while SQLite would take
	// it — the exact cross-dialect divergence that would otherwise reach the
	// column and fail the write.
	conn2 := reconnect(t, e, keys, enrolled.AgentUID, "nonce-bad-caps")
	stale := makeWorkerStale(t, e, enrolled.AgentUID)

	resp = roundTrip(t, conn2, agentcrypto.ClientFrame{
		Type: agentcrypto.MsgTypeClaim, ID: "c2", MaxJobs: 5,
		Capabilities: []string{models.CapabilityIPv4, "null"},
	})
	r.Equal(agentcrypto.MsgTypeJobs, resp.Type, "a bad capability set must not break the claim itself")

	after := capWorker(t, e, enrolled.AgentUID)

	r.Equal([]string{"ipv4", "ipv6"}, after.Capabilities,
		"a malformed report must leave the stored set EXACTLY as it was — "+
			"neither blanked to nil nor rewritten to an empty set")
	r.Equal(models.CapabilityStatePresent, after.Capability(models.CapabilityIPv6),
		"the known capability must survive as a yes, not decay to unknown or no")
	r.NotNil(after.LastActiveAt)
	r.True(after.LastActiveAt.After(stale.Add(staleMark/2)),
		"the heartbeat must still land: a rejected capability set must never "+
			"freeze last_active_at and starve the region of liveness")

	// Positive control: the very same path DOES write when the set is valid, so
	// the assertions above are not passing because nothing reached the handler.
	_ = conn2.Close(websocket.StatusNormalClosure, "bye")

	conn3 := reconnect(t, e, keys, enrolled.AgentUID, "nonce-good-caps")
	goodStale := makeWorkerStale(t, e, enrolled.AgentUID)

	roundTrip(t, conn3, agentcrypto.ClientFrame{
		Type: agentcrypto.MsgTypeClaim, ID: "c3", MaxJobs: 5,
		Capabilities: []string{models.CapabilityIPv4},
	})

	control := capWorker(t, e, enrolled.AgentUID)
	r.Equal([]string{"ipv4"}, control.Capabilities,
		"a well-formed set on the same path must overwrite, or the test above proves nothing")
	r.True(control.LastActiveAt.After(goodStale.Add(staleMark/2)),
		"and the heartbeat writes on the good path too")
}

// TestClaimWithoutCapabilitiesLeavesTheStoredSetIntact is the older-agent case
// at the handler boundary: a claim frame carrying no capabilities at all
// decodes to nil, and nil must mean "do not touch" — never "reset to unknown".
func TestClaimWithoutCapabilitiesLeavesTheStoredSetIntact(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	e := newEnv(t)

	conn, keys, enrolled := e.enroll(e.mintToken(), "dc1-agent")

	roundTrip(t, conn, agentcrypto.ClientFrame{
		Type: agentcrypto.MsgTypeClaim, ID: "c1", MaxJobs: 5,
		Capabilities: []string{models.CapabilityIPv4, models.CapabilityIPv6},
	})
	r.Equal([]string{"ipv4", "ipv6"}, capWorker(t, e, enrolled.AgentUID).Capabilities)

	_ = conn.Close(websocket.StatusNormalClosure, "bye")

	// An agent predating the field sends no capabilities key whatsoever.
	conn2 := reconnect(t, e, keys, enrolled.AgentUID, "nonce-no-caps")
	stale := makeWorkerStale(t, e, enrolled.AgentUID)

	roundTrip(t, conn2, agentcrypto.ClientFrame{
		Type: agentcrypto.MsgTypeClaim, ID: "c2", MaxJobs: 5,
	})

	after := capWorker(t, e, enrolled.AgentUID)
	r.Equal([]string{"ipv4", "ipv6"}, after.Capabilities,
		"an agent that reports nothing must not erase what a newer one reported")
	r.NotNil(after.LastActiveAt)
	r.True(after.LastActiveAt.After(stale.Add(staleMark / 2)))
}
