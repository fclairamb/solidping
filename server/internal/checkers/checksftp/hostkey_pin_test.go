package checksftp_test

import (
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/pkg/sftp"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/checkers/checksftp"
	"github.com/fclairamb/solidping/server/internal/checkers/checkssh"
)

const (
	testUser = "probe"
	testPass = "s3cret"
)

var errBadCredentials = errors.New("bad credentials")

// fakeSFTPServer is an in-process SSH server answering the `sftp` subsystem,
// so the checker can be exercised end-to-end against a known host key.
type fakeSFTPServer struct {
	host        string
	port        int
	fingerprint string
}

func startFakeSFTPServer(t *testing.T) *fakeSFTPServer {
	t.Helper()

	r := require.New(t)

	_, priv, err := ed25519.GenerateKey(rand.Reader)
	r.NoError(err)

	signer, err := ssh.NewSignerFromKey(priv)
	r.NoError(err)

	config := &ssh.ServerConfig{
		PasswordCallback: func(meta ssh.ConnMetadata, password []byte) (*ssh.Permissions, error) {
			if meta.User() != testUser || string(password) != testPass {
				return nil, errBadCredentials
			}

			return &ssh.Permissions{}, nil
		},
	}
	config.AddHostKey(signer)

	listener, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	r.NoError(err)

	tcpAddr, ok := listener.Addr().(*net.TCPAddr)
	r.True(ok)

	go acceptSFTP(listener, config)

	t.Cleanup(func() { _ = listener.Close() })

	return &fakeSFTPServer{
		host:        "127.0.0.1",
		port:        tcpAddr.Port,
		fingerprint: checkssh.Fingerprint(signer.PublicKey()),
	}
}

func acceptSFTP(listener net.Listener, config *ssh.ServerConfig) {
	for {
		conn, err := listener.Accept()
		if err != nil {
			return
		}

		go serveSFTPConn(conn, config)
	}
}

func serveSFTPConn(conn net.Conn, config *ssh.ServerConfig) {
	defer func() { _ = conn.Close() }()

	sshConn, chans, reqs, err := ssh.NewServerConn(conn, config)
	if err != nil {
		return
	}

	defer func() { _ = sshConn.Close() }()

	go ssh.DiscardRequests(reqs)

	for newChan := range chans {
		if newChan.ChannelType() != "session" {
			_ = newChan.Reject(ssh.UnknownChannelType, "only sessions")

			continue
		}

		channel, requests, acceptErr := newChan.Accept()
		if acceptErr != nil {
			return
		}

		go serveSFTPSession(channel, requests)
	}
}

func serveSFTPSession(channel ssh.Channel, requests <-chan *ssh.Request) {
	for req := range requests {
		if req.Type == "subsystem" && len(req.Payload) >= 4 && string(req.Payload[4:]) == "sftp" {
			_ = req.Reply(true, nil)

			server, err := sftp.NewServer(channel)
			if err != nil {
				_ = channel.Close()

				return
			}

			_ = server.Serve()
			_ = server.Close()

			return
		}

		_ = req.Reply(false, nil)
	}
}

func (s *fakeSFTPServer) config(pin string) *checksftp.SFTPConfig {
	return &checksftp.SFTPConfig{
		Host:               s.host,
		Port:               s.port,
		Timeout:            5 * time.Second,
		Username:           testUser,
		Password:           testPass,
		HostKeyFingerprint: pin,
	}
}

// The three shapes of the optional pin (spec 2026-09-15-05): no pin still
// reports the observed key, a matching pin passes, a mismatching pin fails the
// check with both values quoted.
func TestSFTPHostKeyPin(t *testing.T) {
	t.Parallel()

	srv := startFakeSFTPServer(t)
	checker := &checksftp.SFTPChecker{}

	t.Run("no pin still reports the observed fingerprint", func(t *testing.T) {
		t.Parallel()

		r := require.New(t)

		result, err := checker.Execute(t.Context(), srv.config(""))
		r.NoError(err)
		r.Equal(checkerdef.StatusUp, result.Status, "output: %v", result.Output)
		r.Equal(srv.fingerprint, result.Output["host_key_fingerprint"],
			"an operator must be able to copy the pin off a passing check")
	})

	t.Run("a matching pin passes", func(t *testing.T) {
		t.Parallel()

		r := require.New(t)

		result, err := checker.Execute(t.Context(), srv.config(srv.fingerprint))
		r.NoError(err)
		r.Equal(checkerdef.StatusUp, result.Status, "output: %v", result.Output)
		r.Equal(srv.fingerprint, result.Output["host_key_fingerprint"])
	})

	t.Run("a mismatching pin fails the check", func(t *testing.T) {
		t.Parallel()

		r := require.New(t)

		const wrong = "SHA256:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"

		result, err := checker.Execute(t.Context(), srv.config(wrong))
		r.NoError(err)
		r.Equal(checkerdef.StatusDown, result.Status, "output: %v", result.Output)

		message, ok := result.Output[checkerdef.OutputKeyError].(string)
		r.True(ok, "expected an error string in %v", result.Output)
		// The typed branch, not the generic "SSH connection failed" one: the
		// mismatch must be classified, not just happen to mention the words.
		r.Equal("host key mismatch: got "+srv.fingerprint+", expected "+wrong, message)
		r.Equal(srv.fingerprint, result.Output["host_key_fingerprint"])
	})
}

// The pin must look like the fingerprint the check reports, or the operator
// gets a permanently-down check instead of a config error.
func TestSFTPHostKeyPinValidation(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	base := func(pin string) *checksftp.SFTPConfig {
		return &checksftp.SFTPConfig{
			Host: "sftp.acme.com", Username: "alice", Password: "pw", HostKeyFingerprint: pin,
		}
	}

	r.NoError(base("").Validate(), "the pin is optional")
	r.NoError(base("SHA256:uNiVztksCsDhcc0u9e8BujQXVUpKZIDTMczCvj3tD2s").Validate())
	r.Error(base("MD5:aa:bb:cc").Validate())
	r.Error(base("uNiVztksCsDhcc0u9e8BujQXVUpKZIDTMczCvj3tD2s").Validate())
}

// The config key round-trips through FromMap/GetConfig, which is what makes it
// reachable from the API and the dashboard form at all.
func TestSFTPHostKeyPinRoundTrip(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	cfg := &checksftp.SFTPConfig{}
	r.NoError(cfg.FromMap(map[string]any{
		"host":                 "sftp.acme.com",
		"username":             "alice",
		"password":             "pw",
		"host_key_fingerprint": "SHA256:uNiVztksCsDhcc0u9e8BujQXVUpKZIDTMczCvj3tD2s",
	}))
	r.Equal("SHA256:uNiVztksCsDhcc0u9e8BujQXVUpKZIDTMczCvj3tD2s", cfg.HostKeyFingerprint)
	r.Equal("SHA256:uNiVztksCsDhcc0u9e8BujQXVUpKZIDTMczCvj3tD2s",
		cfg.GetConfig()["host_key_fingerprint"])

	wrongType := &checksftp.SFTPConfig{}
	r.Error(wrongType.FromMap(map[string]any{"host_key_fingerprint": 42}))

	absent := &checksftp.SFTPConfig{Host: "sftp.acme.com", Username: "alice", Password: "pw"}
	_, present := absent.GetConfig()["host_key_fingerprint"]
	r.False(present, "an unset pin must not appear in the stored config")
}
