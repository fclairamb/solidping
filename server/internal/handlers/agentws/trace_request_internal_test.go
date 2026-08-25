package agentws

import (
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	agentcrypto "github.com/fclairamb/solidping/server/internal/agents"
	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/handlers/incidents"
)

func traceHandler() *Handler {
	handler := &Handler{conns: newConnRegistry(), logger: slog.Default()}
	handler.SetTraceSettings(3, 30, 15*time.Second)

	return handler
}

func traceRequest() *incidents.TraceRequest {
	return &incidents.TraceRequest{
		OrgUID:      "org-1",
		IncidentUID: "inc-1",
		CheckUID:    "check-1",
		WorkerUID:   "worker-1",
		Region:      "eu-west",
		Trigger:     "incident-open",
		Topic:       "incidents/inc-1/traceroute",
		Failure: checkerdef.NetworkFailure{
			Class:   checkerdef.NetFailureConnectTimeout,
			Host:    "acme.com",
			Address: "192.0.2.10",
			Port:    443,
		},
	}
}

// TestSendTraceRequestReachesTheAgent is the positive control, and it also pins
// what the frame must carry: the SERVER's settings and the address the FAILING
// PROBE dialed. An agent that had to re-resolve the hostname could trace a
// different machine than the one that broke.
func TestSendTraceRequestReachesTheAgent(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	handler := traceHandler()

	outbound := handler.conns.add("worker-1")

	r.True(handler.SendTraceRequest(t.Context(), "worker-1", traceRequest()))

	frame := <-outbound
	r.Equal(agentcrypto.MsgTypeTraceRequest, frame.Type)
	r.Equal("incidents/inc-1/traceroute", frame.Topic)
	r.NotNil(frame.Trace)
	r.Equal("192.0.2.10", frame.Trace.Address)
	r.Equal("acme.com", frame.Trace.Host)
	r.Equal(443, frame.Trace.Port)
	r.Equal(3, frame.Trace.Rounds)
	r.Equal(30, frame.Trace.MaxHops)
	r.Equal(int64(15000), frame.Trace.BudgetMs)

	// The frame is unsolicited and uncorrelated — no id, so an agent that does
	// not understand it can ignore it without leaving anything waiting.
	r.Empty(frame.ID)

	// NOTHING about the region or the trigger travels: the server stamps those
	// onto the capture from the persisted result row, because an agent must
	// never be the authority on where it ran.
	r.Empty(frame.CaptureID)
}

// TestSendTraceRequestReportsAnUnreachableAgent is what makes the dispatcher's
// routing possible. Unlike the screenshot upload request — where a lost capture
// is simply lost — "no live connection" here is actionable: it tells the caller
// there is no vantage point, so it must NOT substitute one of its own.
func TestSendTraceRequestReportsAnUnreachableAgent(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	handler := traceHandler()

	r.False(handler.SendTraceRequest(t.Context(), "worker-nobody", traceRequest()),
		"an agent with no live connection must report failure, not silence")

	outbound := handler.conns.add("worker-1")
	handler.conns.remove("worker-1", outbound)

	r.False(handler.SendTraceRequest(t.Context(), "worker-1", traceRequest()),
		"a disconnected agent must not be reported as reachable")
}

// TestSendTraceRequestRefusesAnIncompleteRequest: a frame with no topic or no
// address would ask the agent to do something it cannot complete, and the
// agent's own guard would drop it — better to never send it.
func TestSendTraceRequestRefusesAnIncompleteRequest(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	handler := traceHandler()
	handler.conns.add("worker-1")

	noTopic := traceRequest()
	noTopic.Topic = ""
	r.False(handler.SendTraceRequest(t.Context(), "worker-1", noTopic))

	noAddress := traceRequest()
	noAddress.Failure.Address = ""
	r.False(handler.SendTraceRequest(t.Context(), "worker-1", noAddress))

	r.False(handler.SendTraceRequest(t.Context(), "", traceRequest()))

	// Positive control: the same handler and the same connection DO accept a
	// complete request, so the refusals above are about the request.
	r.True(handler.SendTraceRequest(t.Context(), "worker-1", traceRequest()))
}
