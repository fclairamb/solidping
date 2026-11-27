package agentws

import (
	"context"
	"errors"
	"testing"
	"time"

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

// newReportHarness is a handler whose UpdateWorkerHeartbeat calls are
// captured, and a refresh func that feeds it one claim frame past the throttle.
func newReportHarness(t *testing.T) (*captureHeartbeatDB, *connState, func(*agentcrypto.ClientFrame) heartbeatCall) {
	t.Helper()

	h := newUpgradeHarness(t)
	capture := &captureHeartbeatDB{Service: h.svc}
	h.handler.dbService = capture
	state := newUpgradeState(newOrgAgent())

	return capture, state, func(frame *agentcrypto.ClientFrame) heartbeatCall {
		noteAgentReport(state, frame)
		state.egressWrittenAt = time.Time{} // past the throttle
		h.handler.recordAgentEgress(t.Context(), state, frame)

		return capture.calls[len(capture.calls)-1]
	}
}

// TestCurrentAgentReportIsWrittenOnce proves an agent that reports its set and
// version only when they are new gets them written once, then only liveness,
// and that a claim omitting them keeps the connection's set for scoping. A
// failed write is retried on the next refresh.
func TestCurrentAgentReportIsWrittenOnce(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	capture, state, refresh := newReportHarness(t)
	caps := []string{"ipv4", "ipv6"}

	// The first claim of the connection carries both; its write fails.
	capture.failNext = true
	r.Equal(heartbeatCall{caps, "1.2.0"}, refresh(&agentcrypto.ClientFrame{Version: "1.2.0", Capabilities: caps}))

	// Later claims carry neither: the unstored report is retried, then only
	// liveness is written.
	r.Equal(heartbeatCall{caps, "1.2.0"}, refresh(&agentcrypto.ClientFrame{}))
	r.Equal(heartbeatCall{nil, ""}, refresh(&agentcrypto.ClientFrame{}))
	r.Equal(caps, state.caps, "claims that omit the set still scope with it")
	r.Equal("1.2.0", state.version)

	// The agent's set changed: it sends it again, without the version.
	r.Equal(heartbeatCall{[]string{"ipv4"}, ""}, refresh(&agentcrypto.ClientFrame{Capabilities: []string{"ipv4"}}))
	r.Equal(heartbeatCall{nil, ""}, refresh(&agentcrypto.ClientFrame{}))

	// An empty set ("none") is a report, unlike nil.
	r.Equal(heartbeatCall{[]string{}, ""}, refresh(&agentcrypto.ClientFrame{Capabilities: []string{}}))
}

// TestOlderAgentReportIsWrittenEveryRefresh proves an agent that sends its set
// and version on every claim (older than ReportsOnce) gets both written on
// every refresh, as before.
func TestOlderAgentReportIsWrittenEveryRefresh(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	_, _, refresh := newReportHarness(t)
	frame := &agentcrypto.ClientFrame{Version: "0.56.0", Capabilities: []string{"ipv4"}}

	r.Equal(heartbeatCall{[]string{"ipv4"}, "0.56.0"}, refresh(frame))
	r.Equal(heartbeatCall{[]string{"ipv4"}, "0.56.0"}, refresh(frame))
}
