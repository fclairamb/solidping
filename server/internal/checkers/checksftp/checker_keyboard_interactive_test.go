package checksftp_test

import (
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"net"
	"strconv"
	"testing"
	"time"

	"github.com/pkg/sftp"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/checkers/checksftp"
)

const testPassword = "s3cret"

var errAuthFailed = errors.New("authentication failed")

// TestExecute_KeyboardInteractiveOnlyServer is the spec's primary case: a
// server whose ServerConfig sets ONLY KeyboardInteractiveCallback (no
// PasswordCallback) — sshd's ChallengeResponseAuthentication shape, and
// what test.rebex.net switched to on 2026-08-25. The checker must still
// authenticate successfully with a configured password.
func TestExecute_KeyboardInteractiveOnlyServer(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	addr := startTestServer(t, &ssh.ServerConfig{
		KeyboardInteractiveCallback: func(
			_ ssh.ConnMetadata, challenge ssh.KeyboardInteractiveChallenge,
		) (*ssh.Permissions, error) {
			answers, err := challenge("", "", []string{"Password: "}, []bool{false})
			if err != nil {
				return nil, err
			}

			if len(answers) != 1 || answers[0] != testPassword {
				return nil, errAuthFailed
			}

			return &ssh.Permissions{}, nil
		},
	})

	result := execute(t, addr, testPassword)
	r.Equal(checkerdef.StatusUp, result.Status, "output: %#v", result.Output)
}

// TestExecute_PasswordOnlyServer is the positive control: a server
// advertising only "password" keeps working exactly as before.
func TestExecute_PasswordOnlyServer(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	addr := startTestServer(t, &ssh.ServerConfig{
		PasswordCallback: func(_ ssh.ConnMetadata, password []byte) (*ssh.Permissions, error) {
			if string(password) != testPassword {
				return nil, errAuthFailed
			}

			return &ssh.Permissions{}, nil
		},
	})

	result := execute(t, addr, testPassword)
	r.Equal(checkerdef.StatusUp, result.Status, "output: %#v", result.Output)
}

// TestExecute_KeyboardInteractiveOnlyServerWrongPassword proves a wrong
// password does not silently succeed.
func TestExecute_KeyboardInteractiveOnlyServerWrongPassword(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	addr := startTestServer(t, &ssh.ServerConfig{
		KeyboardInteractiveCallback: func(
			_ ssh.ConnMetadata, challenge ssh.KeyboardInteractiveChallenge,
		) (*ssh.Permissions, error) {
			answers, err := challenge("", "", []string{"Password: "}, []bool{false})
			if err != nil {
				return nil, err
			}

			if len(answers) != 1 || answers[0] != testPassword {
				return nil, errAuthFailed
			}

			return &ssh.Permissions{}, nil
		},
	})

	result := execute(t, addr, "wrong-password")
	r.NotEqual(checkerdef.StatusUp, result.Status)
}

// execute runs SFTPChecker.Execute against a running test server at addr,
// with username+password auth and no path check (auth + SFTP-session-open
// probe).
func execute(t *testing.T, addr, password string) *checkerdef.Result {
	t.Helper()
	r := require.New(t)

	host, portStr, err := net.SplitHostPort(addr)
	r.NoError(err)

	port, err := strconv.Atoi(portStr)
	r.NoError(err)

	cfg := &checksftp.SFTPConfig{
		Host:     host,
		Port:     port,
		Timeout:  5 * time.Second,
		Username: "tester",
		Password: password,
	}

	checker := &checksftp.SFTPChecker{}

	result, err := checker.Execute(t.Context(), cfg)
	r.NoError(err)
	r.NotNil(result)

	return result
}

// startTestServer brings up a minimal in-process SSH+SFTP server on
// localhost for the duration of the test, accepting whatever auth
// callback(s) config sets and serving the "sftp" subsystem via pkg/sftp's
// server side.
func startTestServer(t *testing.T, config *ssh.ServerConfig) string {
	t.Helper()

	_, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)

	signer, err := ssh.NewSignerFromKey(priv)
	require.NoError(t, err)

	config.AddHostKey(signer)

	listener, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	require.NoError(t, err)

	t.Cleanup(func() { _ = listener.Close() })

	go func() {
		for {
			conn, acceptErr := listener.Accept()
			if acceptErr != nil {
				return // listener closed by t.Cleanup
			}

			go serveConn(conn, config)
		}
	}()

	return listener.Addr().String()
}

func serveConn(conn net.Conn, config *ssh.ServerConfig) {
	sshConn, chans, reqs, err := ssh.NewServerConn(conn, config)
	if err != nil {
		_ = conn.Close()

		return
	}

	defer func() { _ = sshConn.Close() }()

	go ssh.DiscardRequests(reqs)

	for newChannel := range chans {
		if newChannel.ChannelType() != "session" {
			_ = newChannel.Reject(ssh.UnknownChannelType, "unsupported channel type")

			continue
		}

		channel, requests, err := newChannel.Accept()
		if err != nil {
			continue
		}

		go serveSession(channel, requests)
	}
}

// serveSession answers the "subsystem sftp" request the client sends after
// opening its session channel, then hands the channel to pkg/sftp's server.
func serveSession(channel ssh.Channel, requests <-chan *ssh.Request) {
	for req := range requests {
		isSFTP := req.Type == "subsystem" && len(req.Payload) >= 4 && string(req.Payload[4:]) == "sftp"
		if req.WantReply {
			_ = req.Reply(isSFTP, nil)
		}

		if isSFTP {
			server, err := sftp.NewServer(channel)
			if err != nil {
				return
			}

			_ = server.Serve()
			_ = server.Close()

			return
		}
	}
}
