package agentws

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	agentcrypto "github.com/fclairamb/solidping/server/internal/agents"
	"github.com/fclairamb/solidping/server/internal/db"
)

// errHeartbeatUnavailable is the failure the capture backend injects.
var errHeartbeatUnavailable = errors.New("database unavailable")

// heartbeatCall is one UpdateWorkerHeartbeat call as the handler made it.
type heartbeatCall struct {
	capabilities []string
	version      string
}

// captureHeartbeatDB records every UpdateWorkerHeartbeat call, and fails the
// next one when failNext is set. Everything else goes to the real database.
type captureHeartbeatDB struct {
	db.Service

	calls    []heartbeatCall
	failNext bool
}

func (c *captureHeartbeatDB) UpdateWorkerHeartbeat(
	_ context.Context, _ string, capabilities []string, version string,
) error {
	c.calls = append(c.calls, heartbeatCall{capabilities: capabilities, version: version})

	if c.failNext {
		c.failNext = false

		return errHeartbeatUnavailable
	}

	return nil
}

// TestAgentRefreshWritesCapabilitiesAndVersionOnce proves the claim path
// writes the capability set and the version on the connection's first refresh,
// then only liveness, then the set again only when it changes. A failed write
// is retried on the next refresh.
func TestAgentRefreshWritesCapabilitiesAndVersionOnce(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	h := newUpgradeHarness(t)
	capture := &captureHeartbeatDB{Service: h.svc}
	h.handler.dbService = capture

	state := newUpgradeState(newOrgAgent())
	frame := &agentcrypto.ClientFrame{Version: "1.2.0", Capabilities: []string{"ipv4", "ipv6"}}

	refresh := func() heartbeatCall {
		state.egressWrittenAt = state.egressWrittenAt.AddDate(-1, 0, 0) // past the throttle
		h.handler.recordAgentEgress(t.Context(), state, frame)

		return capture.calls[len(capture.calls)-1]
	}

	// The first write fails: nothing is recorded as stored, so the next refresh
	// sends everything again.
	capture.failNext = true
	r.Equal(heartbeatCall{[]string{"ipv4", "ipv6"}, "1.2.0"}, refresh())
	r.Equal(heartbeatCall{[]string{"ipv4", "ipv6"}, "1.2.0"}, refresh())

	// Stored: from now on only liveness.
	r.Equal(heartbeatCall{nil, ""}, refresh())

	// The set changed: it is written once, the version is not.
	frame.Capabilities = []string{"ipv4"}
	r.Equal(heartbeatCall{[]string{"ipv4"}, ""}, refresh())
	r.Equal(heartbeatCall{nil, ""}, refresh())

	// An empty set ("none") is a different report from nil ("unknown").
	frame.Capabilities = []string{}
	r.Equal(heartbeatCall{[]string{}, ""}, refresh())
	r.Equal(heartbeatCall{nil, ""}, refresh())
}
