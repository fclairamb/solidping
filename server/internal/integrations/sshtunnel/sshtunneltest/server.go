// Package sshtunneltest provides an in-process SSH server that answers
// `direct-tcpip` (port-forward) requests, so tunneling can be exercised
// end-to-end in unit tests without a real bastion or any network privilege.
//
// It is the piece that makes the interesting assertion possible: the fake
// bastion is the ONLY resolver of its forwarding table, so a test can target a
// hostname that cannot resolve locally (a `.invalid` name) and prove the
// checker never attempted a local lookup — if it had, the probe would have
// failed before a byte reached the tunnel.
package sshtunneltest

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"io"
	"net"
	"strconv"
	"sync"
	"testing"

	"golang.org/x/crypto/ssh"

	"github.com/fclairamb/solidping/server/internal/checkers/checkssh"
)

// Rejection makes a forwarding-table entry refuse the channel instead of
// connecting, so both sides of the failure classification can be tested: the
// bastion refusing to forward at all (a TUNNEL failure) versus the bastion
// forwarding fine and the target refusing (a plain target failure).
type Rejection struct {
	Reason  ssh.RejectionReason
	Message string
}

// Server is a running in-process SSH bastion.
type Server struct {
	// Host and Port address the bastion itself.
	Host string
	Port int
	// Fingerprint is the bastion's host key in the `SHA256:<base64>` form the
	// `expected_fingerprint` check config uses.
	Fingerprint string
	// Username / Password are the accepted password credentials.
	Username string
	Password string
	// ClientKeyPEM is a private key whose public half the server accepts, in
	// the PEM form the `private_key` check config carries.
	ClientKeyPEM string

	listener net.Listener

	mu sync.Mutex
	// forwards maps a requested "host:port" to the address to dial for it.
	forwards map[string]string
	// rejections maps a requested "host:port" to a channel-open rejection.
	rejections map[string]Rejection
	// requested records every "host:port" asked for, verbatim — that is how a
	// test proves the HOSTNAME (not a pre-resolved IP) crossed the tunnel.
	requested []string
}

// Start brings up a bastion on localhost and tears it down with the test.
// It accepts both password and public-key auth.
func Start(t *testing.T) *Server {
	t.Helper()

	hostSigner, _ := newSigner(t)
	clientSigner, clientPEM := newSigner(t)

	srv := &Server{
		Host:         "127.0.0.1",
		Fingerprint:  checkssh.Fingerprint(hostSigner.PublicKey()),
		Username:     "bastion",
		Password:     "s3cret",
		ClientKeyPEM: clientPEM,
		forwards:     map[string]string{},
		rejections:   map[string]Rejection{},
	}

	config := &ssh.ServerConfig{
		PasswordCallback: func(meta ssh.ConnMetadata, password []byte) (*ssh.Permissions, error) {
			if meta.User() != srv.Username || string(password) != srv.Password {
				return nil, errAuthFailed
			}

			return &ssh.Permissions{}, nil
		},
		PublicKeyCallback: func(meta ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
			if meta.User() != srv.Username ||
				!bytes.Equal(key.Marshal(), clientSigner.PublicKey().Marshal()) {
				return nil, errAuthFailed
			}

			return &ssh.Permissions{}, nil
		},
	}
	config.AddHostKey(hostSigner)

	srv.start(t, config)

	return srv
}

// StartKeyboardInteractiveOnly brings up a bastion that advertises ONLY
// "keyboard-interactive" (no "password", no public key) — the shape a
// sshd ChallengeResponseAuthentication configuration presents. It accepts
// the single challenge answered with Password, mirroring what a real such
// server does when it wraps password auth this way.
func StartKeyboardInteractiveOnly(t *testing.T) *Server {
	t.Helper()

	hostSigner, _ := newSigner(t)

	srv := &Server{
		Host:        "127.0.0.1",
		Fingerprint: checkssh.Fingerprint(hostSigner.PublicKey()),
		Username:    "bastion",
		Password:    "s3cret",
		forwards:    map[string]string{},
		rejections:  map[string]Rejection{},
	}

	config := &ssh.ServerConfig{
		KeyboardInteractiveCallback: func(
			meta ssh.ConnMetadata, challenge ssh.KeyboardInteractiveChallenge,
		) (*ssh.Permissions, error) {
			answers, err := challenge("", "", []string{"Password: "}, []bool{false})
			if err != nil {
				return nil, err
			}

			if meta.User() != srv.Username || len(answers) != 1 || answers[0] != srv.Password {
				return nil, errAuthFailed
			}

			return &ssh.Permissions{}, nil
		},
	}
	config.AddHostKey(hostSigner)

	srv.start(t, config)

	return srv
}

// start brings the listener up and begins accepting connections under config.
func (s *Server) start(t *testing.T, config *ssh.ServerConfig) {
	t.Helper()

	listener, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	s.listener = listener

	tcpAddr, ok := listener.Addr().(*net.TCPAddr)
	if !ok {
		t.Fatalf("unexpected listener address type %T", listener.Addr())
	}

	s.Port = tcpAddr.Port

	go s.acceptLoop(config)

	t.Cleanup(func() { _ = listener.Close() })
}

// Forward makes the bastion connect `requested` (the address the check asks
// for) to `actual` (a real listener in the test). Only the bastion knows this
// mapping — nothing resolves `requested` locally.
func (s *Server) Forward(requested, actual string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.forwards[requested] = actual
}

// Reject makes the bastion refuse to open a forward to `requested`.
func (s *Server) Reject(requested string, rejection Rejection) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.rejections[requested] = rejection
}

// Requested returns every forward target the bastion was asked for, verbatim.
func (s *Server) Requested() []string {
	s.mu.Lock()
	defer s.mu.Unlock()

	out := make([]string, len(s.requested))
	copy(out, s.requested)

	return out
}

// Addr is the bastion's own `host:port`.
func (s *Server) Addr() string {
	return net.JoinHostPort(s.Host, strconv.Itoa(s.Port))
}

func (s *Server) acceptLoop(config *ssh.ServerConfig) {
	for {
		conn, err := s.listener.Accept()
		if err != nil {
			return // listener closed by t.Cleanup
		}

		go s.serveConn(conn, config)
	}
}

func (s *Server) serveConn(conn net.Conn, config *ssh.ServerConfig) {
	sshConn, chans, reqs, err := ssh.NewServerConn(conn, config)
	if err != nil {
		_ = conn.Close()

		return
	}

	defer func() { _ = sshConn.Close() }()

	go ssh.DiscardRequests(reqs)

	for newChannel := range chans {
		if newChannel.ChannelType() != "direct-tcpip" {
			_ = newChannel.Reject(ssh.UnknownChannelType, "only direct-tcpip is supported")

			continue
		}

		go s.serveDirectTCPIP(newChannel)
	}
}

// directTCPIPPayload is the wire shape of an RFC 4254 §7.2 direct-tcpip
// request: the ORIGINATOR's hostname travels in it, which is exactly why the
// bastion — not the client — is the one that resolves the target.
type directTCPIPPayload struct {
	Host       string
	Port       uint32
	OriginHost string
	OriginPort uint32
}

func (s *Server) serveDirectTCPIP(newChannel ssh.NewChannel) {
	var payload directTCPIPPayload
	if err := ssh.Unmarshal(newChannel.ExtraData(), &payload); err != nil {
		_ = newChannel.Reject(ssh.ConnectionFailed, "bad payload")

		return
	}

	requested := net.JoinHostPort(payload.Host, strconv.Itoa(int(payload.Port)))

	s.mu.Lock()
	s.requested = append(s.requested, requested)
	rejection, rejected := s.rejections[requested]
	actual, known := s.forwards[requested]
	s.mu.Unlock()

	if rejected {
		_ = newChannel.Reject(rejection.Reason, rejection.Message)

		return
	}

	if !known {
		// Same answer a real bastion gives for a target it cannot reach.
		_ = newChannel.Reject(ssh.ConnectionFailed, "connect failed")

		return
	}

	upstream, err := (&net.Dialer{}).DialContext(context.Background(), "tcp", actual)
	if err != nil {
		_ = newChannel.Reject(ssh.ConnectionFailed, "connect failed")

		return
	}

	channel, requests, err := newChannel.Accept()
	if err != nil {
		_ = upstream.Close()

		return
	}

	go ssh.DiscardRequests(requests)
	pipe(channel, upstream)
}

// pipe copies bytes both ways until either side closes.
func pipe(channel ssh.Channel, upstream net.Conn) {
	var once sync.Once

	closeBoth := func() {
		once.Do(func() {
			_ = channel.Close()
			_ = upstream.Close()
		})
	}

	go func() {
		defer closeBoth()

		_, _ = io.Copy(upstream, channel)
	}()

	go func() {
		defer closeBoth()

		_, _ = io.Copy(channel, upstream)
	}()
}

// newSigner generates an ed25519 key, returning a signer plus its PEM encoding.
func newSigner(t *testing.T) (ssh.Signer, string) {
	t.Helper()

	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}

	block, err := ssh.MarshalPrivateKey(priv, "")
	if err != nil {
		t.Fatalf("marshal key: %v", err)
	}

	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatalf("new signer: %v", err)
	}

	return signer, string(pem.EncodeToMemory(block))
}
