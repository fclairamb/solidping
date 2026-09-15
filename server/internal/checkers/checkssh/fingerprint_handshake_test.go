package checkssh_test

import (
	"crypto/ed25519"
	"crypto/rand"
	"net"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/checkers/checkssh"
)

// fakeSSHServer is a minimal in-process SSH server whose only job is to say
// whether a client ever got as far as ASKING to authenticate. That is the
// observable difference the fix is about: with the comparison inside the
// HostKeyCallback, a client facing an unexpected host key aborts during the
// key exchange and never sends a userauth request.
type fakeSSHServer struct {
	host        string
	port        int
	fingerprint string
	authTried   atomic.Int32
}

func startFakeSSHServer(t *testing.T) *fakeSSHServer {
	t.Helper()

	r := require.New(t)

	_, priv, err := ed25519.GenerateKey(rand.Reader)
	r.NoError(err)

	signer, err := ssh.NewSignerFromKey(priv)
	r.NoError(err)

	srv := &fakeSSHServer{host: "127.0.0.1", fingerprint: checkssh.Fingerprint(signer.PublicKey())}

	config := &ssh.ServerConfig{
		PasswordCallback: func(ssh.ConnMetadata, []byte) (*ssh.Permissions, error) {
			srv.authTried.Add(1)

			return nil, errAuthRefused
		},
		NoClientAuth: false,
	}
	config.AddHostKey(signer)

	listener, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	r.NoError(err)

	tcpAddr, ok := listener.Addr().(*net.TCPAddr)
	r.True(ok)

	srv.port = tcpAddr.Port

	go func() {
		for {
			conn, acceptErr := listener.Accept()
			if acceptErr != nil {
				return
			}

			go func() {
				defer func() { _ = conn.Close() }()

				sshConn, chans, reqs, hsErr := ssh.NewServerConn(conn, config)
				if hsErr != nil {
					return
				}

				go ssh.DiscardRequests(reqs)

				for newChan := range chans {
					_ = newChan.Reject(ssh.Prohibited, "not supported")
				}

				_ = sshConn.Close()
			}()
		}
	}()

	t.Cleanup(func() { _ = listener.Close() })

	// The banner probe the checker runs first needs the server to be reachable.
	dialer := &net.Dialer{Timeout: time.Second}
	address := net.JoinHostPort(srv.host, strconv.Itoa(srv.port))

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		probe, dialErr := dialer.DialContext(t.Context(), "tcp", address)
		if dialErr == nil {
			_ = probe.Close()

			break
		}

		time.Sleep(10 * time.Millisecond)
	}

	return srv
}

func (s *fakeSSHServer) config(expected string) *checkssh.SSHConfig {
	return &checkssh.SSHConfig{
		Host:                s.host,
		Port:                s.port,
		Timeout:             5 * time.Second,
		ExpectedFingerprint: expected,
	}
}

// A mismatching host key must be refused BY THE HANDSHAKE, not recorded and
// judged after the dial. The proof is that the server never sees a userauth
// request: the client aborted during key exchange.
func TestVerifyFingerprintRefusesAtTheHandshake(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	srv := startFakeSSHServer(t)
	checker := &checkssh.SSHChecker{}

	// Positive control: the real fingerprint is accepted, the check is up, and
	// the server DID see an auth attempt — so "no auth attempt" below really
	// means the handshake stopped early rather than the server being deaf.
	result, err := checker.Execute(t.Context(), srv.config(srv.fingerprint))
	r.NoError(err)
	r.Equal(checkerdef.StatusUp, result.Status, "output: %v", result.Output)
	r.Positive(srv.authTried.Load(), "the matching run must reach authentication")

	before := srv.authTried.Load()

	const wrong = "SHA256:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"

	result, err = checker.Execute(t.Context(), srv.config(wrong))
	r.NoError(err)

	// Operator-visible behavior is unchanged: still down, still quoting both
	// fingerprints.
	r.Equal(checkerdef.StatusDown, result.Status)

	message, ok := result.Output[checkerdef.OutputKeyError].(string)
	r.True(ok, "expected an error string in %v", result.Output)
	r.Contains(message, "fingerprint")
	r.Contains(message, wrong)
	r.Contains(message, srv.fingerprint)

	// Only the point of rejection moved: no authentication was attempted.
	r.Equal(before, srv.authTried.Load(),
		"a mismatching host key must abort the handshake before userauth")
}
