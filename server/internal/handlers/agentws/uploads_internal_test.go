package agentws

import (
	"testing"

	"github.com/stretchr/testify/require"

	agentcrypto "github.com/fclairamb/solidping/server/internal/agents"
)

func testFrame(captureID string) *agentcrypto.ServerFrame {
	return &agentcrypto.ServerFrame{
		Type: agentcrypto.MsgTypeUploadRequest, CaptureID: captureID, Topic: "incidents/inc-1/screenshot",
	}
}

// TestRegistryDeliversToTheRegisteredConnection is the positive control the
// negatives below depend on.
func TestRegistryDeliversToTheRegisteredConnection(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	registry := newConnRegistry()

	outbound := registry.add("worker-1")
	r.True(registry.send("worker-1", testFrame("cap-1")))

	got := <-outbound
	r.Equal(agentcrypto.MsgTypeUploadRequest, got.Type)
	r.Equal("cap-1", got.CaptureID)
}

// TestRegistryDropsWhenNobodyIsConnected is the "agent is gone" path: an
// unknown worker is a silent drop, not an error and not a block.
func TestRegistryDropsWhenNobodyIsConnected(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	registry := newConnRegistry()

	r.False(registry.send("worker-nobody", testFrame("cap-1")))
	r.False(registry.send("", testFrame("cap-1")))

	outbound := registry.add("worker-1")
	registry.remove("worker-1", outbound)

	r.False(registry.send("worker-1", testFrame("cap-1")),
		"a disconnected agent must not be reachable")
}

// TestRegistryNeverBlocksOnAFullQueue pins the non-blocking contract: an agent
// that is not draining its queue must not be able to stall the incident
// pipeline that is asking it for a capture.
func TestRegistryNeverBlocksOnAFullQueue(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	registry := newConnRegistry()
	registry.add("worker-1")

	for range outboundBuffer {
		r.True(registry.send("worker-1", testFrame("cap-fill")))
	}

	r.False(registry.send("worker-1", testFrame("cap-overflow")),
		"a full queue drops rather than blocking")
}

// TestStaleTeardownCannotUnregisterTheLiveConnection is the reconnect race.
//
// An agent whose socket dropped reconnects before the old connection's read
// loop notices. Both connections carry the same worker uid. The old one's
// deferred cleanup must NOT unregister the new one — otherwise the agent looks
// connected but silently stops receiving upload requests.
func TestStaleTeardownCannotUnregisterTheLiveConnection(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	registry := newConnRegistry()

	stale := registry.add("worker-1")
	live := registry.add("worker-1")

	// The old connection tears down, late.
	registry.remove("worker-1", stale)

	r.True(registry.send("worker-1", testFrame("cap-1")),
		"the live connection must still be registered")

	got := <-live
	r.Equal("cap-1", got.CaptureID)

	// And the live connection's own teardown does unregister it.
	registry.remove("worker-1", live)
	r.False(registry.send("worker-1", testFrame("cap-2")))
}
