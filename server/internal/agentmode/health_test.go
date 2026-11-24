package agentmode

import (
	"bytes"
	"context"
	"net"
	"testing"

	"github.com/stretchr/testify/require"
)

// A port already held (a server on the same host network) must not stop the
// agent: the listener is skipped with a warning.
func TestStartHealthListener_PortBusy(t *testing.T) {
	t.Parallel()

	held, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	require.NoError(t, err)

	t.Cleanup(func() { _ = held.Close() })

	var logs bytes.Buffer

	startHealthListener(t.Context(), bufferLogger(&logs), held.Addr().String(), func() bool { return true })

	require.Contains(t, logs.String(), "Agent health listener not started")
}
