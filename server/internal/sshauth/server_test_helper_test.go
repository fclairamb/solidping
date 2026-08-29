package sshauth_test

import (
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"

	"github.com/fclairamb/solidping/server/internal/sshauth"
)

var errAuthFailed = errors.New("authentication failed")

// startServer brings up a minimal in-process SSH server on localhost for the
// duration of the test, accepting only whatever auth callback(s) config
// sets. It never serves a subsystem or shell — the tests here only care
// about whether the handshake (auth) succeeds.
func startServer(t *testing.T, config *ssh.ServerConfig) string {
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

// dial attempts a handshake against addr using sshauth.PasswordMethods(password)
// as the client's auth methods, asserting success or failure per wantSuccess.
func dial(t *testing.T, addr, password string, wantSuccess bool) {
	t.Helper()
	r := require.New(t)

	clientConfig := &ssh.ClientConfig{
		User:            "tester",
		Auth:            sshauth.PasswordMethods(password),
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         5 * time.Second,
	}

	conn, err := (&net.Dialer{Timeout: 5 * time.Second}).DialContext(t.Context(), "tcp", addr)
	r.NoError(err)

	defer func() { _ = conn.Close() }()

	sshConn, _, _, err := ssh.NewClientConn(conn, addr, clientConfig)
	if wantSuccess {
		r.NoError(err)
		_ = sshConn.Close()

		return
	}

	r.Error(err)
}
