package sshtunnel_test

import (
	"context"
	"errors"
	"io"
	"net"
	"testing"

	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/crypto/credentials"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/integrations/sshtunnel"
	"github.com/fclairamb/solidping/server/internal/integrations/sshtunnel/sshtunneltest"
)

const (
	testOrgUID   = "org-1"
	testCheckUID = "ssh-check-1"
)

var errBoom = errors.New("boom")

// stubLoader serves one check row (or an error), standing in for the DB.
type stubLoader struct {
	check *models.Check
	err   error
}

func (s *stubLoader) GetCheck(_ context.Context, _, _ string) (*models.Check, error) {
	if s.err != nil {
		return nil, s.err
	}

	return s.check, nil
}

// stubCreds decrypts by handing back a prepared map, so the resolver's
// merge/plaintext-fallback branches can be driven without real crypto.
type stubCreds struct {
	enabled bool
	private map[string]any
	err     error
}

func (s *stubCreds) Enabled() bool { return s.enabled }

func (s *stubCreds) DecryptForOrg(_ context.Context, _, _ string) (map[string]any, error) {
	if s.err != nil {
		return nil, s.err
	}

	return s.private, nil
}

func sshCheck(config map[string]any) *models.Check {
	slug := "bastion"

	return &models.Check{
		UID:             testCheckUID,
		OrganizationUID: testOrgUID,
		Slug:            &slug,
		Type:            string(checkerdef.CheckTypeSSH),
		Config:          config,
	}
}

func baseConfig(srv *sshtunneltest.Server) map[string]any {
	return map[string]any{
		"host":                 srv.Host,
		"port":                 float64(srv.Port),
		"expected_fingerprint": srv.Fingerprint,
		"username":             srv.Username,
		"password":             srv.Password,
	}
}

func TestLoadConfigRejections(t *testing.T) {
	t.Parallel()

	fingerprint := "SHA256:abc"

	tests := []struct {
		name    string
		loader  *stubLoader
		creds   *stubCreds
		wantErr error
	}{
		{
			name:    "check missing",
			loader:  &stubLoader{err: errBoom},
			wantErr: sshtunnel.ErrCheckNotFound,
		},
		{
			name:    "check nil",
			loader:  &stubLoader{},
			wantErr: sshtunnel.ErrCheckNotFound,
		},
		{
			// A cross-org uid is indistinguishable from a missing one: the
			// lookup is org-scoped, so another org's check is simply invisible.
			name:    "wrong org",
			loader:  &stubLoader{err: errBoom},
			wantErr: sshtunnel.ErrCheckNotFound,
		},
		{
			name: "wrong type",
			loader: &stubLoader{check: &models.Check{
				UID: testCheckUID, OrganizationUID: testOrgUID, Type: string(checkerdef.CheckTypeHTTP),
			}},
			wantErr: sshtunnel.ErrNotSSHCheck,
		},
		{
			name:    "no fingerprint",
			loader:  &stubLoader{check: sshCheck(map[string]any{"host": "h", "username": "u", "password": "p"})},
			wantErr: sshtunnel.ErrNoFingerprint,
		},
		{
			name: "chained tunnel",
			loader: &stubLoader{check: sshCheck(map[string]any{
				"host": "h", "username": "u", "password": "p",
				"expected_fingerprint":             fingerprint,
				checkerdef.TunnelCheckUIDConfigKey: "other-ssh-check",
			})},
			wantErr: sshtunnel.ErrChained,
		},
		{
			name: "no auth material",
			loader: &stubLoader{check: sshCheck(map[string]any{
				"host": "h", "expected_fingerprint": fingerprint,
			})},
			wantErr: sshtunnel.ErrNoCredentials,
		},
		{
			name: "username without secret",
			loader: &stubLoader{check: sshCheck(map[string]any{
				"host": "h", "username": "u", "expected_fingerprint": fingerprint,
			})},
			wantErr: sshtunnel.ErrNoCredentials,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)

			creds := tt.creds
			if creds == nil {
				creds = &stubCreds{}
			}

			_, err := sshtunnel.LoadConfig(t.Context(), tt.loader, creds, testOrgUID, testCheckUID)
			r.Error(err)
			r.ErrorIs(err, tt.wantErr)
			// Every rejection must be classifiable as a tunnel failure, which is
			// what earns the distinct result output.
			r.True(sshtunnel.IsTunnelError(err))
		})
	}
}

// The SSH check's secrets live in the encrypted private column; the resolver
// must merge them back in before it can authenticate.
func TestLoadConfigDecryptsPrivateConfig(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	envelope := "envelope-blob"
	check := sshCheck(map[string]any{
		"host": "bastion.example", "username": "u", "expected_fingerprint": "SHA256:abc",
	})
	check.ConfigPrivate = &envelope

	cfg, err := sshtunnel.LoadConfig(
		t.Context(),
		&stubLoader{check: check},
		&stubCreds{enabled: true, private: map[string]any{"password": "from-vault"}},
		testOrgUID, testCheckUID,
	)
	r.NoError(err)
	r.Equal("from-vault", cfg.Password)
	r.Equal("bastion.example", cfg.Host)
}

func TestLoadConfigDecryptFailures(t *testing.T) {
	t.Parallel()

	// An explicit non-plaintext (key-requiring) envelope: a well-formed v1
	// AES-GCM marker with no real ciphertext, so the case unambiguously
	// exercises the key-requiring path rather than incidentally failing
	// credentials.IsPlaintextEnvelope because the blob isn't valid JSON.
	envelope := `{"v":1,"alg":"AES-256-GCM","nonce":"x","ct":"y"}`

	tests := []struct {
		name  string
		creds *stubCreds
	}{
		{name: "encryption disabled", creds: &stubCreds{enabled: false}},
		{name: "decrypt error", creds: &stubCreds{enabled: true, err: errBoom}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)

			check := sshCheck(map[string]any{
				"host": "h", "username": "u", "expected_fingerprint": "SHA256:abc",
			})
			check.ConfigPrivate = &envelope

			_, err := sshtunnel.LoadConfig(
				t.Context(), &stubLoader{check: check}, tt.creds, testOrgUID, testCheckUID,
			)
			r.ErrorIs(err, sshtunnel.ErrSecretsUnavailable)
		})
	}
}

// A check with no config_private envelope at all keeps its secrets on the
// public config map (legacy rows predating the config_private split, or a
// check that simply has no secret fields) — the resolver must work there too,
// with no dependency on the credentials service.
func TestLoadConfigPlaintextFallback(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	cfg, err := sshtunnel.LoadConfig(
		t.Context(),
		&stubLoader{check: sshCheck(map[string]any{
			"host": "h", "username": "u", "password": "plain", "expected_fingerprint": "SHA256:abc",
		})},
		&stubCreds{enabled: false},
		testOrgUID, testCheckUID,
	)
	r.NoError(err)
	r.Equal("plain", cfg.Password)
}

// TestLoadConfigOpensPlaintextEnvelopeWithoutKey is the positive control for
// the no-master-key path: a v3 plaintext envelope (the structural-separation
// fallback that keeps secrets out of the public config once
// 2026-07-18-06 landed) must open and merge even though the credentials
// service is disabled — this is exactly why the SSH check itself keeps
// passing on a no-master-key server, and why a check tunneled through it must
// too.
func TestLoadConfigOpensPlaintextEnvelopeWithoutKey(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	envelope, err := credentials.SealPlaintext(map[string]any{"password": "from-plaintext-envelope"})
	r.NoError(err)

	check := sshCheck(map[string]any{
		"host": "h", "username": "u", "expected_fingerprint": "SHA256:abc",
	})
	check.ConfigPrivate = &envelope

	cfg, err := sshtunnel.LoadConfig(
		t.Context(),
		&stubLoader{check: check},
		&stubCreds{enabled: false},
		testOrgUID, testCheckUID,
	)
	r.NoError(err)
	r.Equal("from-plaintext-envelope", cfg.Password)
}

// TestLoadConfigOpensPlaintextEnvelopeWithNilCreds locks in the nil-safety of
// the plaintext branch: creds can legitimately be nil (a resolver wired
// without a credentials service), and a plaintext envelope must still open
// without ever touching creds.
func TestLoadConfigOpensPlaintextEnvelopeWithNilCreds(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	envelope, err := credentials.SealPlaintext(map[string]any{"password": "from-plaintext-envelope"})
	r.NoError(err)

	check := sshCheck(map[string]any{
		"host": "h", "username": "u", "expected_fingerprint": "SHA256:abc",
	})
	check.ConfigPrivate = &envelope

	cfg, err := sshtunnel.LoadConfig(t.Context(), &stubLoader{check: check}, nil, testOrgUID, testCheckUID)
	r.NoError(err)
	r.Equal("from-plaintext-envelope", cfg.Password)
}

func TestResolveDialsWithPassword(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	srv := sshtunneltest.Start(t)
	target := newEchoTarget(t, srv, "private.invalid:9000")

	resolver := sshtunnel.NewResolver(&stubLoader{check: sshCheck(baseConfig(srv))}, &stubCreds{})

	dialer, closer, err := resolver(t.Context(), testOrgUID, testCheckUID)
	r.NoError(err)

	defer func() { _ = closer.Close() }()

	conn, err := dialer.DialContext(t.Context(), "tcp", "private.invalid:9000")
	r.NoError(err)

	defer func() { _ = conn.Close() }()

	// The hostname crossed the tunnel verbatim: the bastion resolved it, not us.
	r.Equal([]string{"private.invalid:9000"}, srv.Requested())
	r.Equal(target, readLine(t, conn))
	r.NoError(dialer.TunnelFailure())
}

// A bastion that advertises only "keyboard-interactive" (the shape sshd's
// ChallengeResponseAuthentication presents — see the checksftp fix this test
// accompanies) must still authenticate with a configured password, exactly
// as a real OpenSSH client would answer the challenge.
func TestResolveDialsWithKeyboardInteractiveOnlyServer(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	srv := sshtunneltest.StartKeyboardInteractiveOnly(t)
	target := newEchoTarget(t, srv, "private.invalid:9000")

	resolver := sshtunnel.NewResolver(&stubLoader{check: sshCheck(baseConfig(srv))}, &stubCreds{})

	dialer, closer, err := resolver(t.Context(), testOrgUID, testCheckUID)
	r.NoError(err)

	defer func() { _ = closer.Close() }()

	conn, err := dialer.DialContext(t.Context(), "tcp", "private.invalid:9000")
	r.NoError(err)

	defer func() { _ = conn.Close() }()

	r.Equal(target, readLine(t, conn))
}

// A wrong password against a keyboard-interactive-only server must still
// fail — the challenge answer must not silently authenticate.
func TestResolveRejectsBadPasswordKeyboardInteractiveOnlyServer(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	srv := sshtunneltest.StartKeyboardInteractiveOnly(t)

	config := baseConfig(srv)
	config["password"] = "wrong"

	resolver := sshtunnel.NewResolver(&stubLoader{check: sshCheck(config)}, &stubCreds{})

	_, _, err := resolver(t.Context(), testOrgUID, testCheckUID)
	r.Error(err)
	r.True(sshtunnel.IsTunnelError(err))
}

func TestResolveDialsWithPrivateKey(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	srv := sshtunneltest.Start(t)
	target := newEchoTarget(t, srv, "private.invalid:9000")

	config := baseConfig(srv)
	delete(config, "password")
	config["private_key"] = srv.ClientKeyPEM

	resolver := sshtunnel.NewResolver(&stubLoader{check: sshCheck(config)}, &stubCreds{})

	dialer, closer, err := resolver(t.Context(), testOrgUID, testCheckUID)
	r.NoError(err)

	defer func() { _ = closer.Close() }()

	conn, err := dialer.DialContext(t.Context(), "tcp", "private.invalid:9000")
	r.NoError(err)

	defer func() { _ = conn.Close() }()

	// Reading the greeting is what proves the private-key handshake produced a
	// working tunnel rather than merely an accepted dial. Closing is not asserted
	// on: the echo target hangs up as soon as it has written, so a close that
	// loses the race to the peer's own close reports EOF — a property of the SSH
	// channel, not a tunnel fault.
	r.Equal(target, readLine(t, conn))
}

// The tunnel carries the probe's traffic, so an unverified bastion is a silent
// MITM: a key that isn't the expected fingerprint must abort the handshake.
func TestResolveRejectsFingerprintMismatch(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	srv := sshtunneltest.Start(t)

	config := baseConfig(srv)
	config["expected_fingerprint"] = "SHA256:" + "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"

	resolver := sshtunnel.NewResolver(&stubLoader{check: sshCheck(config)}, &stubCreds{})

	_, _, err := resolver(t.Context(), testOrgUID, testCheckUID)
	r.Error(err)
	r.ErrorIs(err, sshtunnel.ErrHostKeyMismatch)
	r.True(sshtunnel.IsTunnelError(err))
}

func TestResolveRejectsBadPassword(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	srv := sshtunneltest.Start(t)

	config := baseConfig(srv)
	config["password"] = "wrong"

	resolver := sshtunnel.NewResolver(&stubLoader{check: sshCheck(config)}, &stubCreds{})

	_, _, err := resolver(t.Context(), testOrgUID, testCheckUID)
	r.Error(err)
	r.True(sshtunnel.IsTunnelError(err))
}

// A forward the bastion REFUSES to open (administratively prohibited) is a
// tunnel failure; a forward it attempts and the TARGET refuses is not — that is
// the target being down, which is exactly what the check exists to detect.
func TestDialerClassifiesForwardFailures(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		rejection      sshtunneltest.Rejection
		wantTunnelFail bool
	}{
		{
			name: "administratively prohibited is a tunnel failure",
			rejection: sshtunneltest.Rejection{
				Reason: ssh.Prohibited, Message: "administratively prohibited",
			},
			wantTunnelFail: true,
		},
		{
			name: "target refused is not a tunnel failure",
			rejection: sshtunneltest.Rejection{
				Reason: ssh.ConnectionFailed, Message: "connect failed",
			},
			wantTunnelFail: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)

			srv := sshtunneltest.Start(t)
			srv.Reject("private.invalid:9000", tt.rejection)

			resolver := sshtunnel.NewResolver(&stubLoader{check: sshCheck(baseConfig(srv))}, &stubCreds{})

			dialer, closer, err := resolver(t.Context(), testOrgUID, testCheckUID)
			r.NoError(err)

			defer func() { _ = closer.Close() }()

			_, err = dialer.DialContext(t.Context(), "tcp", "private.invalid:9000")
			r.Error(err)

			if tt.wantTunnelFail {
				r.Error(dialer.TunnelFailure())
				r.ErrorIs(dialer.TunnelFailure(), sshtunnel.ErrForwardRejected)
			} else {
				r.NoError(dialer.TunnelFailure())
			}
		})
	}
}

func TestResolveFromContextOverridesGlobal(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	// Not configured at all: the caller gets a typed tunnel error rather than a
	// nil-deref.
	_, _, err := sshtunnel.Resolve(t.Context(), testOrgUID, testCheckUID)
	r.ErrorIs(err, sshtunnel.ErrNotConfigured)

	called := false
	ctx := sshtunnel.WithResolver(t.Context(), func(
		_ context.Context, _, _ string,
	) (*sshtunnel.Dialer, io.Closer, error) {
		called = true

		return nil, nil, errBoom
	})

	_, _, err = sshtunnel.Resolve(ctx, testOrgUID, testCheckUID)
	r.ErrorIs(err, errBoom)
	r.True(called)
}

// newEchoTarget starts a TCP listener that greets every connection, and teaches
// the bastion to forward `requested` to it. It returns the greeting.
func newEchoTarget(t *testing.T, srv *sshtunneltest.Server, requested string) string {
	t.Helper()

	const greeting = "hello-from-the-private-side"

	listener, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
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

				_, _ = conn.Write([]byte(greeting + "\n"))
			}()
		}
	}()

	srv.Forward(requested, listener.Addr().String())

	return greeting
}

func readLine(t *testing.T, conn net.Conn) string {
	t.Helper()

	buf := make([]byte, 128)

	n, err := conn.Read(buf)
	require.NoError(t, err)

	return string(buf[:n-1])
}
