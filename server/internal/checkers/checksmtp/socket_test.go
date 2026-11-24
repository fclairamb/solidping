package checksmtp_test

import (
	"bufio"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/checkers/checksmtp"
)

// smtpSocketServer is a minimal SMTP server on a Unix socket: a 220 banner,
// a 250 to EHLO and a 221 to QUIT. The socket lives in a short directory
// (macOS caps socket paths at ~104 bytes).
func smtpSocketServer(t *testing.T) string {
	t.Helper()

	dir, err := os.MkdirTemp("", "smtp")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	path := filepath.Join(dir, "s.sock")
	listener, err := (&net.ListenConfig{}).Listen(t.Context(), "unix", path)
	require.NoError(t, err)

	t.Cleanup(func() { _ = listener.Close() })

	go func() {
		for {
			conn, acceptErr := listener.Accept()
			if acceptErr != nil {
				return
			}

			go serveSMTPSocket(conn)
		}
	}()

	return path
}

func serveSMTPSocket(conn net.Conn) {
	defer func() { _ = conn.Close() }()

	_, _ = conn.Write([]byte("220 mx.acme.test ESMTP ready\r\n"))

	reader := bufio.NewReader(conn)

	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return
		}

		switch cmd := strings.ToUpper(strings.TrimSpace(line)); {
		case strings.HasPrefix(cmd, "EHLO"):
			_, _ = conn.Write([]byte("250 mx.acme.test\r\n"))
		case strings.HasPrefix(cmd, "QUIT"):
			_, _ = conn.Write([]byte("221 bye\r\n"))

			return
		default:
			_, _ = conn.Write([]byte("250 ok\r\n"))
		}
	}
}

func runSMTPSocket(t *testing.T, path string, allow []string) *checkerdef.Result {
	t.Helper()

	cfg := &checksmtp.SMTPConfig{}
	require.NoError(t, cfg.FromMap(map[string]any{"socket": path, "timeout": "3s"}))

	ctx := checkerdef.WithLocalSocketAllowlist(t.Context(), allow)

	result, err := (&checksmtp.SMTPChecker{}).Execute(ctx, cfg)
	require.NoError(t, err)

	return result
}

func TestSMTPSocket(t *testing.T) {
	t.Parallel()

	path := smtpSocketServer(t)
	allow := []string{filepath.Dir(path) + "/"}

	t.Run("allowlisted socket is up", func(t *testing.T) {
		t.Parallel()

		result := runSMTPSocket(t, path, allow)
		require.Equal(t, checkerdef.StatusUp, result.Status, result.Output)
		require.Equal(t, path, result.Output["socket"])
		require.Contains(t, result.Output["greeting"], "ESMTP ready")
	})

	t.Run("missing socket is down with ENOENT", func(t *testing.T) {
		t.Parallel()

		result := runSMTPSocket(t, filepath.Join(filepath.Dir(path), "missing.sock"), allow)
		require.Equal(t, checkerdef.StatusDown, result.Status, result.Output)
		require.Contains(t, result.Output[checkerdef.OutputKeyError], "ENOENT")
	})

	t.Run("path outside the allowlist is refused without dialing", func(t *testing.T) {
		t.Parallel()

		result := runSMTPSocket(t, path, []string{"/run/other/"})
		require.NotEqual(t, checkerdef.StatusUp, result.Status)
		require.Contains(t, result.Output[checkerdef.OutputKeyError], checkerdef.LocalSocketSetting)

		result = runSMTPSocket(t, path, nil)
		require.NotEqual(t, checkerdef.StatusUp, result.Status, "an empty allowlist refuses every path")
	})
}

func TestSMTPSocketConfig(t *testing.T) {
	t.Parallel()

	validate := func(cfg map[string]any) (*checkerdef.CheckSpec, error) {
		spec := &checkerdef.CheckSpec{Config: cfg}

		return spec, (&checksmtp.SMTPChecker{}).Validate(spec)
	}

	spec, err := validate(map[string]any{"socket": "/run/postfix/submission.sock"})
	require.NoError(t, err)
	require.Equal(t, "smtp-run-postfix-submission-sock", spec.Slug)

	_, err = validate(map[string]any{"socket": "/run/postfix/submission.sock", "host": "acme.com"})
	require.ErrorContains(t, err, "mutually exclusive")

	_, err = validate(map[string]any{"socket": "/run/postfix/submission.sock", "starttls": true})
	require.Error(t, err, "no STARTTLS over a socket")

	_, err = validate(map[string]any{"socket": "submission.sock"})
	require.Error(t, err, "relative path")

	cfg := &checksmtp.SMTPConfig{}
	require.NoError(t, cfg.FromMap(map[string]any{"url": "smtp+unix:///run/postfix/submission.sock"}))
	require.Equal(t, "/run/postfix/submission.sock", cfg.Socket)
	require.Equal(t, "/run/postfix/submission.sock", cfg.GetConfig()["socket"])
}
