package checkssh_test

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/checkers/checkssh"
)

// startExecServer runs an SSH server that accepts any password and answers
// every exec request with out and the given exit status.
func startExecServer(t *testing.T, out string, exitStatus uint32) (string, int) {
	t.Helper()

	_, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)

	signer, err := ssh.NewSignerFromKey(priv)
	require.NoError(t, err)

	cfg := &ssh.ServerConfig{
		PasswordCallback: func(ssh.ConnMetadata, []byte) (*ssh.Permissions, error) { return &ssh.Permissions{}, nil },
		PublicKeyCallback: func(ssh.ConnMetadata, ssh.PublicKey) (*ssh.Permissions, error) {
			return &ssh.Permissions{}, nil
		},
		BannerCallback: func(ssh.ConnMetadata) string { return "welcome acme\n" },
	}
	cfg.AddHostKey(signer)

	lis, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = lis.Close() })

	go func() {
		for {
			conn, acceptErr := lis.Accept()
			if acceptErr != nil {
				return
			}

			go serveExec(conn, cfg, out, exitStatus)
		}
	}()

	tcp, ok := lis.Addr().(*net.TCPAddr)
	require.True(t, ok)

	return "127.0.0.1", tcp.Port
}

func serveExec(conn net.Conn, cfg *ssh.ServerConfig, out string, exitStatus uint32) {
	sshConn, chans, reqs, err := ssh.NewServerConn(conn, cfg)
	if err != nil {
		_ = conn.Close()

		return
	}

	defer func() { _ = sshConn.Close() }()

	go ssh.DiscardRequests(reqs)

	for newCh := range chans {
		ch, chReqs, acceptErr := newCh.Accept()
		if acceptErr != nil {
			return
		}

		go func() {
			for req := range chReqs {
				if req.Type != "exec" {
					_ = req.Reply(false, nil)

					continue
				}

				_ = req.Reply(true, nil)
				_, _ = ch.Write([]byte(out))
				_, _ = ch.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{exitStatus}))
				_ = ch.Close()
			}
		}()
	}
}

func TestExecuteCommands(t *testing.T) {
	t.Parallel()

	host, port := startExecServer(t, "hello world", 0)
	hostFail, portFail := startExecServer(t, "boom", 3)

	tests := []struct {
		name    string
		host    string
		port    int
		mutate  func(*checkssh.SSHConfig)
		status  checkerdef.Status
		errPart string
	}{
		{"ok", host, port, func(c *checkssh.SSHConfig) { c.Command = "echo" }, checkerdef.StatusUp, ""},
		{"expected output", host, port, func(c *checkssh.SSHConfig) {
			c.Command = "echo"
			c.ExpectedOutput = "world"
		}, checkerdef.StatusUp, ""},
		{"missing output", host, port, func(c *checkssh.SSHConfig) {
			c.Command = "echo"
			c.ExpectedOutput = "nope"
		}, checkerdef.StatusDown, "not found"},
		{"pattern ok (compiled lazily)", host, port, func(c *checkssh.SSHConfig) {
			c.Command = "echo"
			c.ExpectedOutputPattern = `^hello\s+w`
		}, checkerdef.StatusUp, ""},
		{"pattern mismatch", host, port, func(c *checkssh.SSHConfig) {
			c.Command = "echo"
			c.ExpectedOutputPattern = `^bye`
		}, checkerdef.StatusDown, "does not match"},
		{"invalid pattern", host, port, func(c *checkssh.SSHConfig) {
			c.Command = "echo"
			c.ExpectedOutputPattern = `(`
		}, checkerdef.StatusError, "invalid regex"},
		{
			"unexpected exit code", hostFail, portFail, func(c *checkssh.SSHConfig) { c.Command = "false" },
			checkerdef.StatusDown, "exit code 3, expected 0",
		},
		{"expected non-zero exit code", hostFail, portFail, func(c *checkssh.SSHConfig) {
			c.Command = "false"
			c.ExpectedExitCode = 3
		}, checkerdef.StatusUp, ""},
		{"fingerprint mismatch", host, port, func(c *checkssh.SSHConfig) {
			c.ExpectedFingerprint = "SHA256:deadbeef"
		}, checkerdef.StatusDown, ""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)

			cfg := &checkssh.SSHConfig{
				Host: tc.host, Port: tc.port, Timeout: 5 * time.Second,
				Username: "tester", Password: "pw",
			}
			tc.mutate(cfg)

			res, err := (&checkssh.SSHChecker{}).Execute(t.Context(), cfg)
			r.NoError(err)
			r.Equal(tc.status, res.Status, "output: %v", res.Output)

			if tc.errPart != "" {
				r.Contains(res.Output[checkerdef.OutputKeyError], tc.errPart)
			}
		})
	}
}

func TestExecuteReturnsCommandOutputAndBanner(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	host, port := startExecServer(t, "hello world", 0)
	cfg := &checkssh.SSHConfig{
		Host: host, Port: port, Timeout: 5 * time.Second,
		Username: "tester", Password: "pw", Command: "echo",
	}

	res, err := (&checkssh.SSHChecker{}).Execute(t.Context(), cfg)
	r.NoError(err)
	r.Equal(checkerdef.StatusUp, res.Status)
	r.Equal("hello world", res.Output["stdout"])
	r.Equal(0, res.Output["exit_code"])
	r.Equal("welcome acme", res.Output["banner"])
	r.Contains(res.Metrics, "command_time_ms")
}

func TestExecutePrivateKeyAuth(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	host, port := startExecServer(t, "ok", 0)

	_, priv, err := ed25519.GenerateKey(rand.Reader)
	r.NoError(err)

	der, err := x509.MarshalPKCS8PrivateKey(priv)
	r.NoError(err)

	pemKey := string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
	keyCfg := checkssh.SSHConfig{Host: host, Username: "u", PrivateKey: pemKey}
	r.NoError(keyCfg.Validate())

	cfg := &checkssh.SSHConfig{
		Host: host, Port: port, Timeout: 5 * time.Second,
		Username: "tester", PrivateKey: pemKey, Command: "x",
	}

	res, err := (&checkssh.SSHChecker{}).Execute(t.Context(), cfg)
	r.NoError(err)
	r.Equal(checkerdef.StatusUp, res.Status, "output: %v", res.Output)
}

func TestExecuteFingerprintMatch(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	host, port := startExecServer(t, "", 0)

	// Learn the real fingerprint from the mismatch message of a first probe.
	probe := &checkssh.SSHConfig{
		Host: host, Port: port, Timeout: 5 * time.Second,
		Username: "tester", Password: "pw", ExpectedFingerprint: "SHA256:x",
	}
	res, err := (&checkssh.SSHChecker{}).Execute(t.Context(), probe)
	r.NoError(err)
	r.Equal(checkerdef.StatusDown, res.Status)

	fp, ok := res.Output["fingerprint"].(string)
	r.True(ok, "output: %v", res.Output)

	probe.ExpectedFingerprint = fp
	res, err = (&checkssh.SSHChecker{}).Execute(t.Context(), probe)
	r.NoError(err)
	r.Equal(checkerdef.StatusUp, res.Status, "output: %v", res.Output)
}

func TestSSHConfigValidate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		cfg  checkssh.SSHConfig
		bad  string
	}{
		{"ok banner only", checkssh.SSHConfig{Host: "h"}, ""},
		{"no host", checkssh.SSHConfig{}, "host"},
		{"bad port", checkssh.SSHConfig{Host: "h", Port: 70000}, "port"},
		{"bad timeout", checkssh.SSHConfig{Host: "h", Timeout: time.Minute}, "timeout"},
		{"command needs user", checkssh.SSHConfig{Host: "h", Command: "x"}, "command"},
		{"output needs command", checkssh.SSHConfig{Host: "h", ExpectedOutput: "x"}, "expected_output"},
		{"pattern needs command", checkssh.SSHConfig{Host: "h", ExpectedOutputPattern: "x"}, "expected_output_pattern"},
		{"both secrets", checkssh.SSHConfig{Host: "h", Password: "a", PrivateKey: "b"}, "password"},
		{"user needs secret", checkssh.SSHConfig{Host: "h", Username: "u"}, "username"},
		{"bad regex", checkssh.SSHConfig{
			Host: "h", Username: "u", Password: "p", Command: "c", ExpectedOutputPattern: "(",
		}, "expected_output_pattern"},
		{"bad pem", checkssh.SSHConfig{Host: "h", Username: "u", PrivateKey: "nope"}, "private_key"},
		{"openssh-ish pem accepted", checkssh.SSHConfig{
			Host: "h", Username: "u",
			PrivateKey: "-----BEGIN OPENSSH PRIVATE KEY-----\nAAAA\n-----END OPENSSH PRIVATE KEY-----\n",
		}, ""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			cfg := tc.cfg
			err := cfg.Validate()
			if tc.bad == "" {
				require.NoError(t, err)

				return
			}

			require.Error(t, err)
			require.Contains(t, err.Error(), tc.bad)
		})
	}
}
