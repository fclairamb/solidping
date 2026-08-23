package backend_test

import (
	"crypto/ed25519"
	"encoding/base64"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/agents"
	"github.com/fclairamb/solidping/server/internal/nettrace"
)

// The agent half of traceroute-on-failure (spec 2026-08-21-10), end to end
// against the same fake master the screenshot upload tests use: the server asks
// for a trace over the WS control channel, the agent RUNS it, and the capture
// arrives at the attachment endpoint with the agent's normal signed headers.
//
// The target is a loopback listener rather than a real host, so the trace is
// one hop, finishes in milliseconds, and needs no privilege of any kind — the
// TCP rung of the ladder is always available.

const traceTopic = "incidents/inc-1/traceroute"

// loopbackTarget binds a port that accepts and closes, so a TCP-mode trace to
// it completes at TTL 1.
func loopbackTarget(t *testing.T) int {
	t.Helper()

	var config net.ListenConfig

	listener, err := config.Listen(t.Context(), "tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = listener.Close() })

	go func() {
		for {
			conn, acceptErr := listener.Accept()
			if acceptErr != nil {
				return
			}

			_ = conn.Close()
		}
	}()

	addr, ok := listener.Addr().(*net.TCPAddr)
	require.True(t, ok)

	return addr.Port
}

func TestAgentRunsTheRequestedTraceAndUploadsIt(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := t.Context()

	port := loopbackTarget(t)

	fake := newCaptureServer(t)
	fake.traceTopic = traceTopic
	fake.traceAsk = &agents.TraceRequestFrame{
		Host:     "localhost",
		Address:  "127.0.0.1",
		Port:     port,
		Rounds:   1,
		MaxHops:  4,
		BudgetMs: 5000,
	}

	wsBackend := newCaptureBackend(t, fake)

	// Any result will do: the trace request is unsolicited and uncorrelated,
	// so it rides back on the socket the result arrived on.
	r.NoError(wsBackend.SubmitResult(ctx, testJob(), "worker-1", captureSubmitReq(nil)))

	select {
	case <-fake.uploaded:
	case <-time.After(20 * time.Second):
		t.Fatal("the agent never uploaded a path capture")
	}

	uploads, agentPub := fake.uploadSnapshot()
	r.Len(uploads, 1)

	upload := uploads[0]
	r.Equal(traceTopic, upload.topic, "the topic must be the SERVER's, echoed verbatim")

	// The bytes are a real capture — parsed with the same strict parser the
	// server's attachment endpoint sniffs with, so an agent that uploaded
	// something unusable would fail here rather than in production.
	capture, err := nettrace.ParseCapture(upload.body)
	r.NoError(err)
	r.Equal("127.0.0.1", capture.Address)
	r.Equal(port, capture.Port)
	r.NotEmpty(capture.Hops)

	// The agent is NEVER the authority on where it ran: region and trigger are
	// stamped server-side from the persisted result row.
	r.Empty(capture.Region)
	r.Empty(capture.Trigger)

	// The upload carries the agent's ordinary signed headers — the same
	// credential the WS reconnect uses, no second secret.
	r.Equal("agent-1", upload.agentUID)
	r.NotEmpty(upload.signature)
	r.NotEmpty(upload.nonce)
	r.NotEmpty(upload.timestamp)
	sig, err := base64.StdEncoding.DecodeString(upload.signature)
	r.NoError(err)
	r.True(ed25519.Verify(agentPub,
		agents.SignatureChallenge(
			http.MethodPost, "/api/v1/agent/attachments", upload.timestamp, upload.nonce),
		sig,
	), "the trace upload must be signed with the agent's own key")
}

// TestAgentIgnoresATraceRequestWithNoUsableTarget: a malformed frame must be a
// silent drop, never a panic and never an upload of something meaningless.
func TestAgentIgnoresATraceRequestWithNoUsableTarget(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := t.Context()

	fake := newCaptureServer(t)
	fake.traceTopic = traceTopic
	fake.traceAsk = &agents.TraceRequestFrame{Address: "not-an-ip", Port: 443, BudgetMs: 2000}

	wsBackend := newCaptureBackend(t, fake)
	r.NoError(wsBackend.SubmitResult(ctx, testJob(), "worker-1", captureSubmitReq(nil)))

	select {
	case <-fake.uploaded:
		t.Fatal("the agent uploaded something for an unusable trace request")
	case <-time.After(2 * time.Second):
	}

	uploads, _ := fake.uploadSnapshot()
	r.Empty(uploads)
}
