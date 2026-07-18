package checkimap_test

import (
	"bufio"
	"net"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/checkers/checkimap"
	"github.com/fclairamb/solidping/server/internal/integrations/sshtunnel/sshtunneltest"
)

// tunnelHost cannot resolve locally — `.invalid` is reserved by RFC 2606 and
// never resolves anywhere. Only the fake bastion's forwarding table knows what
// it means, so a passing probe PROVES checkimap skipped its local resolveHost.
const (
	tunnelHost   = "imap.private.invalid"
	tunnelTarget = "imap.private.invalid:1143"
)

func TestExecuteThroughTunnelSkipsLocalResolution(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	backend := newIMAPStub(t)

	srv := sshtunneltest.Start(t)
	srv.Forward(tunnelTarget, backend)

	checker := &checkimap.IMAPChecker{}
	config := &checkimap.IMAPConfig{}
	r.NoError(config.FromMap(map[string]any{
		"host": tunnelHost,
		"port": float64(1143),
	}))

	ctx := checkerdef.WithTunnelDialer(t.Context(), srv.Dialer(t))

	result, err := checker.Execute(ctx, config)
	r.NoError(err)
	r.Equal(checkerdef.StatusUp, result.Status, result.Output)

	// The bastion received the hostname verbatim and resolved it itself.
	r.Equal([]string{tunnelTarget}, srv.Requested())
	r.Equal(tunnelHost, result.Output[checkerdef.OutputKeyHost])
	r.Equal(true, result.Output["tunneled"])
}

// Untunneled, a `.invalid` host must still fail on local resolution — proving
// the tunnel branch is what skips the lookup, not a blanket removal.
func TestExecuteWithoutTunnelStillResolvesLocally(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	checker := &checkimap.IMAPChecker{}
	config := &checkimap.IMAPConfig{}
	r.NoError(config.FromMap(map[string]any{"host": tunnelHost, "port": float64(1143)}))

	result, err := checker.Execute(t.Context(), config)
	r.NoError(err)
	r.Equal(checkerdef.StatusError, result.Status)
	r.Contains(result.Output[checkerdef.OutputKeyError], "failed to resolve hostname")
}

// newIMAPStub starts a minimal IMAP server that only sends the `* OK` greeting —
// enough for a plaintext no-auth check to reach StatusUp (it then sends LOGOUT).
func newIMAPStub(t *testing.T) string {
	t.Helper()

	listener, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	require.NoError(t, err)

	t.Cleanup(func() { _ = listener.Close() })

	go func() {
		for {
			conn, acceptErr := listener.Accept()
			if acceptErr != nil {
				return
			}

			go func() {
				defer func() { _ = conn.Close() }()

				_, _ = conn.Write([]byte("* OK [CAPABILITY IMAP4rev1] stub.private ready\r\n"))

				reader := bufio.NewReader(conn)
				for {
					if _, err := reader.ReadString('\n'); err != nil {
						return
					}
				}
			}()
		}
	}()

	return listener.Addr().String()
}
