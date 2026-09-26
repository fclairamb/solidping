package backend_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/agents"
	"github.com/fclairamb/solidping/server/internal/egress"
)

// An agent whose operator closed its egress (SP_EGRESS_ALLOW_PRIVATE=false)
// must not trace towards a private address either: the server-requested trace
// is dropped silently, nothing is uploaded. The permissive twin is
// TestAgentRunsTheRequestedTraceAndUploadsIt (same loopback target, no guard).
func TestAgentRefusesATraceTheEgressPolicyForbids(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	port := loopbackTarget(t)

	fake := newCaptureServer(t)
	fake.traceTopic = traceTopic
	fake.traceAsk = &agents.TraceRequestFrame{
		Host: "localhost", Address: "127.0.0.1", Port: port, Rounds: 1, MaxHops: 4, BudgetMs: 2000,
	}

	wsBackend := newCaptureBackend(t, fake)
	wsBackend.SetEgressGuard(egress.New(false))

	r.NoError(wsBackend.SubmitResult(t.Context(), testJob(), "worker-1", captureSubmitReq(nil)))

	select {
	case <-fake.uploaded:
		t.Fatal("the agent traced an address its egress policy refuses")
	case <-time.After(2 * time.Second):
	}

	uploads, _ := fake.uploadSnapshot()
	r.Empty(uploads)
}
