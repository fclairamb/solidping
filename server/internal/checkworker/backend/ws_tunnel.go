package backend

import (
	"context"
	"fmt"
	"io"

	"github.com/fclairamb/solidping/server/internal/agents"
	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/checkers/checkssh"
	"github.com/fclairamb/solidping/server/internal/crypto/credentials"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/integrations/sshtunnel"
)

// buildTunnelConfig turns a dispatched tunnel block into a ready-to-dial SSH
// config: unseal the region-sealed envelope with the agent's identity, merge the
// secrets over the SSH check's public config, and validate credential presence.
// It is the agent-side twin of the server's sshtunnel.LoadConfig, minus the DB
// lookup (the server already re-asserted eligibility at claim time) — the agent
// only ever sees the sealed blob, never config_private.
func buildTunnelConfig(identity string, tunnel *agents.AgentJobTunnel) (*checkssh.SSHConfig, error) {
	merged := tunnel.Config

	if tunnel.ConfigSealed != nil && *tunnel.ConfigSealed != "" {
		secrets, err := credentials.UnsealWithIdentity(identity, *tunnel.ConfigSealed)
		if err != nil {
			return nil, fmt.Errorf("%w: %w", ErrTunnelSealedForOthers, err)
		}

		merged = credentials.MergeConfig(tunnel.Config, secrets)
	}

	cfg := &checkssh.SSHConfig{}
	if err := cfg.FromMap(merged); err != nil {
		return nil, err
	}

	if cfg.Username == "" || (cfg.Password == "" && cfg.PrivateKey == "") {
		return nil, ErrTunnelNoCredentials
	}

	return cfg, nil
}

// submitTunnelError reports an unbuildable tunnel block as an error result so
// the check's history shows the actionable message (and the tunnel_failed
// marker) instead of the check silently never running. Mirrors submitSealError.
func (b *WSBackend) submitTunnelError(ctx context.Context, job *models.CheckJob, cause error) {
	_, _ = b.request(ctx, &agents.ClientFrame{
		Type:     agents.MsgTypeResult,
		JobUID:   job.UID,
		Status:   int(models.ResultStatusError),
		Duration: 0,
		Output: map[string]any{
			checkerdef.OutputKeyError:        "tunnel failed: " + cause.Error(),
			checkerdef.OutputKeyTunnelFailed: true,
		},
	})
}

// registerTunnel stores (or refreshes) the unsealed snapshot for a tunnel check,
// bumping its reference count. Multiple claimed jobs may share one SSH check.
func (b *WSBackend) registerTunnel(checkUID string, cfg *checkssh.SSHConfig) {
	b.tunnelMu.Lock()
	defer b.tunnelMu.Unlock()

	if entry, ok := b.tunnels[checkUID]; ok {
		entry.cfg = cfg
		entry.refs++

		return
	}

	b.tunnels[checkUID] = &tunnelSnapshot{cfg: cfg, refs: 1}
}

// unregisterTunnel drops one reference to a tunnel snapshot, deleting the
// plaintext once no claimed job references it. A no-op for an unknown key (a
// job dropped for a missing block never registered anything).
func (b *WSBackend) unregisterTunnel(checkUID string) {
	b.tunnelMu.Lock()
	defer b.tunnelMu.Unlock()

	entry, ok := b.tunnels[checkUID]
	if !ok {
		return
	}

	entry.refs--
	if entry.refs <= 0 {
		delete(b.tunnels, checkUID)
	}
}

// lookupTunnel returns the unsealed snapshot for a tunnel check, if present.
func (b *WSBackend) lookupTunnel(checkUID string) (*checkssh.SSHConfig, bool) {
	b.tunnelMu.Lock()
	defer b.tunnelMu.Unlock()

	entry, ok := b.tunnels[checkUID]
	if !ok {
		return nil, false
	}

	return entry.cfg, true
}

// ResolveTunnel is the agent-side sshtunnel.Resolver (wired into
// sshtunnel.ResolverFunc at agent startup). It looks up the unsealed snapshot
// for the tunnel check and dials a fresh SSH session through it — the exact
// dial/handshake/host-key/classification machinery the server path uses. A
// missing snapshot yields decision 7's clearer error, wrapped as a tunnel
// *Error so the worker still classifies the execution as a tunnel failure.
//
// orgUID is ignored: an agent is single-org and single-region, and the tunnel
// check UID is unique within it.
func (b *WSBackend) ResolveTunnel(
	ctx context.Context, _, tunnelCheckUID string,
) (*sshtunnel.Dialer, io.Closer, error) {
	cfg, ok := b.lookupTunnel(tunnelCheckUID)
	if !ok {
		return nil, nil, &sshtunnel.Error{Op: "resolve", Err: sshtunnel.ErrNotAvailableOnAgent}
	}

	return sshtunnel.Dial(ctx, cfg)
}
