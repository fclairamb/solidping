package backend

import (
	"net"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/agents"
	"github.com/fclairamb/solidping/server/internal/checkers/checkssh"
	"github.com/fclairamb/solidping/server/internal/crypto/credentials"
	"github.com/fclairamb/solidping/server/internal/integrations/sshtunnel"
	"github.com/fclairamb/solidping/server/internal/integrations/sshtunnel/sshtunneltest"
)

func newTunnelTestBackend() *WSBackend {
	return NewWSBackend("", "", "agent", &Identity{}, nil)
}

func tunnelPublicConfig(srv *sshtunneltest.Server) map[string]any {
	return map[string]any{
		"host":                 srv.Host,
		"port":                 float64(srv.Port),
		"expected_fingerprint": srv.Fingerprint,
		"username":             srv.Username,
	}
}

// TestResolveTunnelUnsealsAndDials is the agent-side happy path: a tunnel block
// sealed to this agent unseals, resolves, and dials a fresh SSH session that
// forwards to a hostname only the bastion can resolve (`.invalid`).
func TestResolveTunnelUnsealsAndDials(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	srv := sshtunneltest.Start(t)
	greeting := echoTarget(t, srv, "private.invalid:9000")

	keys, err := agents.GenerateAgentKeys()
	r.NoError(err)

	// The bastion password is the SEALED secret; the public config carries only
	// host/port/fingerprint/username.
	sealed, err := credentials.SealForRecipients(
		[]string{keys.X25519Recipient}, map[string]any{"password": srv.Password},
	)
	r.NoError(err)

	tunnel := &agents.AgentJobTunnel{
		CheckUID:     "ssh-1",
		Config:       tunnelPublicConfig(srv),
		ConfigSealed: &sealed,
	}

	cfg, err := buildTunnelConfig(keys.X25519Identity, tunnel)
	r.NoError(err)
	r.Equal(srv.Password, cfg.Password, "the sealed password must merge over the public config")
	r.Equal(srv.Username, cfg.Username)

	b := newTunnelTestBackend()
	b.registerTunnel(tunnel.CheckUID, cfg)

	dialer, closer, err := b.ResolveTunnel(t.Context(), "org", tunnel.CheckUID)
	r.NoError(err)

	defer func() { _ = closer.Close() }()

	conn, err := dialer.DialContext(t.Context(), "tcp", "private.invalid:9000")
	r.NoError(err)

	defer func() { _ = conn.Close() }()

	// The hostname crossed the tunnel verbatim — the bastion resolved it.
	r.Equal([]string{"private.invalid:9000"}, srv.Requested())

	buf := make([]byte, len(greeting))
	n, readErr := conn.Read(buf)
	r.NoError(readErr)
	r.Equal(greeting, string(buf[:n]))
}

// TestBuildTunnelConfigWrongIdentity: a block sealed to a DIFFERENT agent yields
// the clear "not sealed for this agent" error, not a silent success.
func TestBuildTunnelConfigWrongIdentity(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	otherKeys, err := agents.GenerateAgentKeys()
	r.NoError(err)
	ourKeys, err := agents.GenerateAgentKeys()
	r.NoError(err)

	sealed, err := credentials.SealForRecipients(
		[]string{otherKeys.X25519Recipient}, map[string]any{"password": "hunter2"},
	)
	r.NoError(err)

	tunnel := &agents.AgentJobTunnel{
		CheckUID:     "ssh-1",
		Config:       map[string]any{"host": "bastion", "expected_fingerprint": "SHA256:abc", "username": "u"},
		ConfigSealed: &sealed,
	}

	_, err = buildTunnelConfig(ourKeys.X25519Identity, tunnel)
	r.ErrorIs(err, ErrTunnelSealedForOthers)
}

// TestBuildTunnelConfigPlaintextFallback: with no sealed blob (the self-hosted
// no-master-key mode) the credentials ride on the public config and still work.
func TestBuildTunnelConfigPlaintextFallback(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	tunnel := &agents.AgentJobTunnel{
		CheckUID: "ssh-1",
		Config: map[string]any{
			"host": "bastion", "expected_fingerprint": "SHA256:abc",
			"username": "u", "password": "plain",
		},
	}

	cfg, err := buildTunnelConfig("", tunnel)
	r.NoError(err)
	r.Equal("plain", cfg.Password)
}

// TestBuildTunnelConfigNoCredentials: an unsealed block with no auth material is
// rejected before any dial.
func TestBuildTunnelConfigNoCredentials(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	tunnel := &agents.AgentJobTunnel{
		CheckUID: "ssh-1",
		Config:   map[string]any{"host": "bastion", "expected_fingerprint": "SHA256:abc", "username": "u"},
	}

	_, err := buildTunnelConfig("", tunnel)
	r.ErrorIs(err, ErrTunnelNoCredentials)
}

// TestResolveTunnelMissingSnapshot is decision 7: a tunneled job with no
// snapshot on this agent gets the clearer "not available on this agent" error,
// classified as a tunnel failure so the worker labels it correctly.
func TestResolveTunnelMissingSnapshot(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	b := newTunnelTestBackend()

	_, _, err := b.ResolveTunnel(t.Context(), "org", "unknown-ssh")
	r.Error(err)
	r.ErrorIs(err, sshtunnel.ErrNotAvailableOnAgent)
	r.True(sshtunnel.IsTunnelError(err), "the missing-block error must classify as a tunnel failure")
}

// TestTunnelRegistryRefcount: a snapshot shared by two claimed jobs survives
// until the last releases it; unregistering an unknown key is harmless.
func TestTunnelRegistryRefcount(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	b := newTunnelTestBackend()
	cfg := &checkssh.SSHConfig{Username: "u", Password: "p"}

	b.registerTunnel("ssh-1", cfg)
	b.registerTunnel("ssh-1", cfg)

	_, ok := b.lookupTunnel("ssh-1")
	r.True(ok)

	b.unregisterTunnel("ssh-1")
	_, ok = b.lookupTunnel("ssh-1")
	r.True(ok, "still referenced by the second job")

	b.unregisterTunnel("ssh-1")
	_, ok = b.lookupTunnel("ssh-1")
	r.False(ok, "dropped once no job references it")

	b.unregisterTunnel("never-registered") // no panic, no underflow
}

// echoTarget starts a listener that greets each connection and teaches the
// bastion to forward `requested` to it. Returns the greeting.
func echoTarget(t *testing.T, srv *sshtunneltest.Server, requested string) string {
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

				_, _ = conn.Write([]byte(greeting))
			}()
		}
	}()

	srv.Forward(requested, listener.Addr().String())

	return greeting
}
