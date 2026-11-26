package backend_test

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/coder/websocket"
	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/agents"
	"github.com/fclairamb/solidping/server/internal/checkworker/backend"
)

// TestWSBackendReEnrollsAWithdrawnIdentity: an agent started on a volume that
// still holds a deleted agent's identity gets a 401 on reconnect. With an
// enrollment token configured it enrolls again and persists the new identity.
// A 403 (revoked) never re-enrolls, and neither does a 401 without a token.
func TestWSBackendReEnrollsAWithdrawnIdentity(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name           string
		reconnectCode  int
		token          string
		wantReEnrolled bool
	}{
		{"deleted agent re-enrolls", http.StatusUnauthorized, "spe_token", true},
		{"revoked agent stays out", http.StatusForbidden, "spe_token", false},
		{"no token stays out", http.StatusUnauthorized, "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)

			fake := &fakeAgentServer{region: "@home"}
			fake.handle(func(_ int, conn *websocket.Conn) { fake.serveReads(conn) })
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				if req.Header.Get("X-Sp-Agent-Uid") != "" {
					w.WriteHeader(tc.reconnectCode)

					return
				}

				fake.serve(w, req)
			}))
			t.Cleanup(server.Close)

			keys, err := agents.GenerateAgentKeys()
			r.NoError(err)

			var (
				mu        sync.Mutex
				persisted *backend.Identity
			)

			ws := backend.NewWSBackend(server.URL, tc.token, "agent",
				&backend.Identity{AgentKeys: *keys, AgentUID: "deleted-agent", Region: "@home"},
				func(id *backend.Identity) {
					mu.Lock()
					persisted = id
					mu.Unlock()
				})

			_, err = ws.Register(t.Context(), nil)

			mu.Lock()
			defer mu.Unlock()

			if !tc.wantReEnrolled {
				r.Error(err)
				r.Nil(persisted)
				r.Equal("deleted-agent", ws.Identity().AgentUID)

				return
			}

			r.NoError(err)
			r.NotNil(persisted)
			r.Equal("agent-1", persisted.AgentUID)
			r.Equal("agent-1", ws.Identity().AgentUID)
			r.Equal(keys.Ed25519PublicKey, persisted.Ed25519PublicKey)
		})
	}
}
