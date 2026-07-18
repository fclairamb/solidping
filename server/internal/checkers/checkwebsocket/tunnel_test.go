package checkwebsocket_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/coder/websocket"
	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/checkers/checkwebsocket"
	"github.com/fclairamb/solidping/server/internal/integrations/sshtunnel/sshtunneltest"
)

// tunnelHost cannot resolve locally — `.invalid` is reserved by RFC 2606. A
// successful handshake against the backend PROVES the WebSocket dial was routed
// through the tunnel and the hostname resolved on the far side.
const (
	tunnelHost   = "ws.private.invalid"
	tunnelTarget = "ws.private.invalid:80"
)

func TestExecuteThroughTunnel(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		conn, err := websocket.Accept(w, req, nil)
		if err != nil {
			return
		}

		defer func() { _ = conn.CloseNow() }()

		// Hold the connection open until the client closes it.
		for {
			if _, _, readErr := conn.Read(req.Context()); readErr != nil {
				return
			}
		}
	}))
	defer backend.Close()

	srv := sshtunneltest.Start(t)
	srv.Forward(tunnelTarget, backend.Listener.Addr().String())

	checker := &checkwebsocket.WebSocketChecker{}
	config := &checkwebsocket.WebSocketConfig{}
	r.NoError(config.FromMap(map[string]any{
		"url": "ws://" + tunnelHost + "/",
	}))

	ctx := checkerdef.WithTunnelDialer(t.Context(), srv.Dialer(t))

	result, err := checker.Execute(ctx, config)
	r.NoError(err)
	r.Equal(checkerdef.StatusUp, result.Status, result.Output)

	// The bastion resolved the hostname; nothing local did.
	r.Equal([]string{tunnelTarget}, srv.Requested())
	r.Equal(true, result.Output["tunneled"])
}

// Untunneled, a `.invalid` host must still fail locally — proving the tunnel
// branch is what routes the handshake remotely.
func TestExecuteWithoutTunnelStillResolvesLocally(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	checker := &checkwebsocket.WebSocketChecker{}
	config := &checkwebsocket.WebSocketConfig{}
	r.NoError(config.FromMap(map[string]any{"url": "ws://" + tunnelHost + "/"}))

	result, err := checker.Execute(t.Context(), config)
	r.NoError(err)
	r.NotEqual(checkerdef.StatusUp, result.Status)
}
