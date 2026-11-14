package backend_test

import (
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/stretchr/testify/require"
)

// TestWSBackendSendsOneBootIDPerProcess covers the agent half of spec
// 2026-11-14-03: the enroll frame and every reconnect carry the same boot ID,
// so the server can tell a network reconnect (keep the leases) from a new
// process (free them), and another backend instance sends another one.
func TestWSBackendSendsOneBootIDPerProcess(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := t.Context()

	fake := newFakeAgentServer(t, "@acme/dc1")

	closeFirst := make(chan struct{})

	fake.handle(func(idx int, conn *websocket.Conn) {
		if idx == 0 {
			<-closeFirst
			_ = conn.Close(websocket.StatusNormalClosure, "server closing")

			return
		}

		fake.serveReads(conn)
	})

	wsBackend := newWSBackend(t, fake)

	_, err := wsBackend.Register(ctx, nil)
	r.NoError(err)

	close(closeFirst)

	r.Eventually(func() bool { return len(fake.seenBootIDs()) >= 2 }, 10*time.Second, 10*time.Millisecond,
		"the agent must reconnect after the server closed the connection")

	seen := fake.seenBootIDs()
	r.NotEmpty(seen[0], "the enroll frame carries a boot ID")
	r.Equal(seen[0], seen[1], "a reconnect from the same process sends the same boot ID")

	// Another process (here, another backend) is another boot.
	other := newFakeAgentServer(t, "@acme/dc1")
	other.handle(func(_ int, conn *websocket.Conn) { other.serveReads(conn) })

	_, err = newWSBackend(t, other).Register(ctx, nil)
	r.NoError(err)
	r.Len(other.seenBootIDs(), 1)
	r.NotEqual(seen[0], other.seenBootIDs()[0])
}
