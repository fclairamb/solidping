package backend_test

import (
	"testing"

	"github.com/coder/websocket"
	"github.com/stretchr/testify/require"
)

// TestWSBackendConnected: Connected follows the link, which is what agent
// mode's health listener reports to the container healthcheck (issue #995).
func TestWSBackendConnected(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	fake := newFakeAgentServer(t, "@acme/dc1")
	fake.handle(func(_ int, conn *websocket.Conn) { fake.serveReads(conn) })

	wsBackend := newWSBackend(t, fake)
	r.False(wsBackend.Connected(), "not connected before Register")

	_, err := wsBackend.Register(t.Context(), nil)
	r.NoError(err)
	r.True(wsBackend.Connected(), "connected once registered")

	wsBackend.Close()
	r.False(wsBackend.Connected(), "not connected after Close")
}
