package backend_test

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/agents"
	"github.com/fclairamb/solidping/server/internal/checkworker/backend"
)

// TestWSBackendDial5xxIsConnLoss: a 503 on the WebSocket handshake (the
// server's database is unreachable) reads as a lost connection the worker
// recovers from, while a 401 stays a hard authentication error.
func TestWSBackendDial5xxIsConnLoss(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		status   int
		connLost bool
	}{
		{http.StatusServiceUnavailable, true},
		{http.StatusInternalServerError, true},
		{http.StatusUnauthorized, false},
	} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(tc.status)
		}))
		t.Cleanup(server.Close)

		keys, err := agents.GenerateAgentKeys()
		require.NoError(t, err)

		ws := backend.NewWSBackend(server.URL, "spe_token", "agent", &backend.Identity{AgentKeys: *keys}, nil)

		_, _, err = ws.ClaimJobs(t.Context(), "", nil, 1, 0, 0)
		require.Error(t, err)
		require.Equal(t, tc.connLost, errors.Is(err, backend.ErrConnLost), "status %d: %v", tc.status, err)
	}
}
