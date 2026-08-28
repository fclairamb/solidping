package checkssh_test

import (
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"net"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/checkers/checkssh"
)

const testPassword = "s3cret"

var errAuthFailed = errors.New("authentication failed")

// TestExecute_KeyboardInteractiveOnlyServer is the checkssh half of the sweep
// (executeWithAuth): a server whose ServerConfig sets ONLY
// KeyboardInteractiveCallback (no PasswordCallback) — sshd's
// ChallengeResponseAuthentication shape — must still authenticate
// successfully with a configured password.
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

// execute runs SSHChecker.Execute against a running test server at addr,
// with username+password auth and no command (auth-only probe).
func execute(t *testing.T, addr, password string) *checkerdef.Result {
	t.Helper()
	r := require.New(t)

	host, portStr, err := net.SplitHostPort(addr)
	r.NoError(err)

	port, err := strconv.Atoi(portStr)
	r.NoError(err)

	cfg := &checkssh.SSHConfig{
		Host:     host,
		Port:     port,
		Timeout:  5 * time.Second,
		Username: "tester",
		Password: password,
	}

	checker := &checkssh.SSHChecker{}

	result, err := checker.Execute(t.Context(), cfg)
	r.NoError(err)
	r.NotNil(result)

	return result
}

// startTestServer brings up a minimal in-process SSH server on localhost for
// the duration of the test, accepting whatever auth callback(s) config sets,
// and rejecting any channel open (this test only cares about the handshake).
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

			go func() {
				sshConn, chans, reqs, handshakeErr := ssh.NewServerConn(conn, config)
				if handshakeErr != nil {
					_ = conn.Close()

					return
				}

				defer func() { _ = sshConn.Close() }()

				go ssh.DiscardRequests(reqs)

				for newChannel := range chans {
					_ = newChannel.Reject(ssh.Prohibited, "test server accepts no channels")
				}
			}()
		}
	}()

	return listener.Addr().String()
}
