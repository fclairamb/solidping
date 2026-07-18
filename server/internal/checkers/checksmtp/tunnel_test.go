package checksmtp_test

import (
	"bufio"
	"net"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/checkers/checksmtp"
	"github.com/fclairamb/solidping/server/internal/integrations/sshtunnel/sshtunneltest"
)

// tunnelHost cannot resolve locally — `.invalid` is reserved by RFC 2606 and
// never resolves anywhere. Only the fake bastion's forwarding table knows what
// it means, so a passing probe PROVES checksmtp skipped its local resolveHost:
// had it resolved locally first, the check would have errored before dialing.
const (
	tunnelHost   = "smtp.private.invalid"
	tunnelTarget = "smtp.private.invalid:2525"
)

func TestExecuteThroughTunnelSkipsLocalResolution(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	backend := newSMTPStub(t)

	srv := sshtunneltest.Start(t)
	srv.Forward(tunnelTarget, backend)

	checker := &checksmtp.SMTPChecker{}
	config := &checksmtp.SMTPConfig{}
	r.NoError(config.FromMap(map[string]any{
		"host": tunnelHost,
		"port": float64(2525),
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

	checker := &checksmtp.SMTPChecker{}
	config := &checksmtp.SMTPConfig{}
	r.NoError(config.FromMap(map[string]any{"host": tunnelHost, "port": float64(2525)}))

	result, err := checker.Execute(t.Context(), config)
	r.NoError(err)
	r.Equal(checkerdef.StatusError, result.Status)
	r.Contains(result.Output[checkerdef.OutputKeyError], "failed to resolve hostname")
}

// newSMTPStub starts a minimal SMTP server: greeting, EHLO, QUIT — enough for a
// plaintext no-auth check to reach StatusUp without a real mail backend.
func newSMTPStub(t *testing.T) string {
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

			go serveSMTP(conn)
		}
	}()

	return listener.Addr().String()
}

func serveSMTP(conn net.Conn) {
	defer func() { _ = conn.Close() }()

	reader := bufio.NewReader(conn)
	_, _ = conn.Write([]byte("220 stub.private ESMTP ready\r\n"))

	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return
		}

		switch {
		case strings.HasPrefix(line, "EHLO"), strings.HasPrefix(line, "HELO"):
			_, _ = conn.Write([]byte("250 stub.private at your service\r\n"))
		case strings.HasPrefix(line, "QUIT"):
			_, _ = conn.Write([]byte("221 bye\r\n"))

			return
		default:
			_, _ = conn.Write([]byte("250 ok\r\n"))
		}
	}
}
