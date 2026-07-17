package checktcp_test

import (
	"net"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/checkers/checktcp"
	"github.com/fclairamb/solidping/server/internal/integrations/sshtunnel/sshtunneltest"
)

// tunnelHost cannot resolve locally — `.invalid` is reserved by RFC 2606 and
// never resolves anywhere. Only the fake bastion's forwarding table knows what
// it means, so a passing probe PROVES checktcp skipped its local LookupIPAddr:
// had it resolved locally first, the check would have errored before dialing.
const (
	tunnelHost = "private.invalid"
	tunnelPort = 5432
)

func TestExecuteThroughTunnelSkipsLocalResolution(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	greeting := "postgres-ish\n"
	backend := newGreetingListener(t, greeting)

	srv := sshtunneltest.Start(t)
	srv.Forward(net.JoinHostPort(tunnelHost, "5432"), backend)

	checker := &checktcp.TCPChecker{}
	config := &checktcp.TCPConfig{}
	r.NoError(config.FromMap(map[string]any{
		"host":        tunnelHost,
		"port":        float64(tunnelPort),
		"expect_data": "postgres-ish",
	}))

	ctx := checkerdef.WithTunnelDialer(t.Context(), srv.Dialer(t))

	result, err := checker.Execute(ctx, config)
	r.NoError(err)
	r.Equal(checkerdef.StatusUp, result.Status, result.Output)

	// The bastion received the hostname verbatim and resolved it itself.
	r.Equal([]string{net.JoinHostPort(tunnelHost, "5432")}, srv.Requested())

	// The output reports the configured host and flags the tunnel; no
	// `ip_version` is claimed, because the worker never learns which address
	// the bastion picked.
	r.Equal(tunnelHost, result.Output[checkerdef.OutputKeyHost])
	r.Equal(true, result.Output["tunneled"])
	r.NotContains(result.Output, "ip_version")
}

// Untunneled, a `.invalid` host must still fail on local resolution — proving
// the tunnel branch is what skips the lookup, not a blanket removal.
func TestExecuteWithoutTunnelStillResolvesLocally(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	checker := &checktcp.TCPChecker{}
	config := &checktcp.TCPConfig{}
	r.NoError(config.FromMap(map[string]any{"host": tunnelHost, "port": float64(tunnelPort)}))

	result, err := checker.Execute(t.Context(), config)
	r.NoError(err)
	r.Equal(checkerdef.StatusError, result.Status)
	r.Contains(result.Output[checkerdef.OutputKeyError], "failed to resolve hostname")
}

// A target the bastion cannot reach reports the TARGET as down: the tunnel
// worked, the service behind it did not.
func TestExecuteThroughTunnelTargetDown(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	srv := sshtunneltest.Start(t) // no Forward registered → "connect failed"
	dialer := srv.Dialer(t)

	checker := &checktcp.TCPChecker{}
	config := &checktcp.TCPConfig{}
	r.NoError(config.FromMap(map[string]any{"host": tunnelHost, "port": float64(tunnelPort)}))

	ctx := checkerdef.WithTunnelDialer(t.Context(), dialer)

	result, err := checker.Execute(ctx, config)
	r.NoError(err)
	r.Equal(checkerdef.StatusDown, result.Status)
	r.NoError(dialer.TunnelFailure())
}

// newGreetingListener starts a TCP server that greets each connection and
// returns its address.
func newGreetingListener(t *testing.T, greeting string) string {
	t.Helper()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
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

				_, _ = conn.Write([]byte(greeting))
			}()
		}
	}()

	return listener.Addr().String()
}
