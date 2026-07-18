package checkpop3_test

import (
	"bufio"
	"net"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/checkers/checkpop3"
	"github.com/fclairamb/solidping/server/internal/integrations/sshtunnel/sshtunneltest"
)

// tunnelHost cannot resolve locally — `.invalid` is reserved by RFC 2606 and
// never resolves anywhere. Only the fake bastion's forwarding table knows what
// it means, so a passing probe PROVES checkpop3 skipped its local resolveHost.
const (
	tunnelHost   = "pop3.private.invalid"
	tunnelTarget = "pop3.private.invalid:1110"
)

func TestExecuteThroughTunnelSkipsLocalResolution(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	backend := newPOP3Stub(t)

	srv := sshtunneltest.Start(t)
	srv.Forward(tunnelTarget, backend)

	checker := &checkpop3.POP3Checker{}
	config := &checkpop3.POP3Config{}
	r.NoError(config.FromMap(map[string]any{
		"host": tunnelHost,
		"port": float64(1110),
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

	checker := &checkpop3.POP3Checker{}
	config := &checkpop3.POP3Config{}
	r.NoError(config.FromMap(map[string]any{"host": tunnelHost, "port": float64(1110)}))

	result, err := checker.Execute(t.Context(), config)
	r.NoError(err)
	r.Equal(checkerdef.StatusError, result.Status)
	r.Contains(result.Output[checkerdef.OutputKeyError], "failed to resolve hostname")
}

// newPOP3Stub starts a minimal POP3 server: `+OK` greeting then QUIT — enough
// for a plaintext no-auth check to reach StatusUp without a real backend.
func newPOP3Stub(t *testing.T) string {
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

				_, _ = conn.Write([]byte("+OK stub.private POP3 ready\r\n"))

				reader := bufio.NewReader(conn)
				for {
					line, err := reader.ReadString('\n')
					if err != nil {
						return
					}

					if strings.HasPrefix(line, "QUIT") {
						_, _ = conn.Write([]byte("+OK bye\r\n"))

						return
					}
				}
			}()
		}
	}()

	return listener.Addr().String()
}
