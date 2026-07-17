package checkerdef

import (
	"context"
	"net"
)

// ContextDialer is the dialer seam a tunnel-capable checker consumes: the
// `DialContext(ctx, network, addr) (net.Conn, error)` shape rather than the
// concrete *net.Dialer struct, so any transport (an SSH port-forward today, a
// SOCKS proxy or an agent-side relay tomorrow) can satisfy it. It is
// deliberately compatible with golang.org/x/net/proxy.ContextDialer and with
// what *ssh.Client already exposes.
//
// `addr` is the raw `host:port` the check targets — NOT a pre-resolved IP.
// Tunneling implies remote-side name resolution: the SSH direct-tcpip request
// carries the hostname and the bastion resolves it, which is the whole point
// for private hostnames a worker's resolver can never see. Checkers must
// therefore skip their own local lookup when a tunnel dialer is present.
type ContextDialer interface {
	DialContext(ctx context.Context, network, addr string) (net.Conn, error)
}

// DialerFunc adapts a plain function to ContextDialer.
type DialerFunc func(ctx context.Context, network, addr string) (net.Conn, error)

// DialContext implements ContextDialer.
func (f DialerFunc) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	return f(ctx, network, addr)
}

// tunnelDialerCtxKeyType makes the context key unique within the binary.
type tunnelDialerCtxKeyType struct{}

//nolint:gochecknoglobals // sentinel value for a context key
var tunnelDialerCtxKey = tunnelDialerCtxKeyType{}

// WithTunnelDialer returns a context carrying the dialer every outbound
// connection of the check should be made through. The worker sets it once per
// execution after establishing the tunnel; checkers only consume it.
func WithTunnelDialer(ctx context.Context, dialer ContextDialer) context.Context {
	return context.WithValue(ctx, tunnelDialerCtxKey, dialer)
}

// TunnelDialerFrom returns the tunnel dialer carried by ctx, or nil when the
// check is not tunneled (the overwhelmingly common case — callers must keep
// their untunneled path byte-for-byte unchanged).
func TunnelDialerFrom(ctx context.Context) ContextDialer {
	if dialer, ok := ctx.Value(tunnelDialerCtxKey).(ContextDialer); ok {
		return dialer
	}

	return nil
}

// TunnelCheckUIDConfigKey is the well-known check-config key referencing the
// SSH check whose connection the probe is dialed through. It follows the
// `timeout` precedent (see checkworker.checkTimeoutConfigKey): the worker reads
// it generically off the raw config map and the per-checker `FromMap`
// implementations simply ignore the unknown key, so no checker config struct
// grows a field for it.
//
// The name is deliberately tunnel-specific rather than a generic `dependsOn`:
// the semantic is "dial through this". A generic dependency concept can arrive
// later and subsume the edge.
const TunnelCheckUIDConfigKey = "tunnelCheckUid"

// OutputKeyTunnelFailed is the machine-readable marker set on a result whose
// tunnel could not be established (resolve, dial, auth, host-key mismatch,
// forward rejected). It distinguishes "the bastion is broken" from "the target
// behind the bastion is down" — groundwork for dependency-aware suppression.
const OutputKeyTunnelFailed = "tunnel_failed"

// MetricKeyTunnelSetupMs is the result-metric key holding how long establishing
// the tunnel took. Tunnel setup is deliberately kept OUT of the check's
// measured Duration (each checker times its own probe, which starts after the
// dialer is handed to it) — otherwise every tunneled check's latency graph
// would be dominated by SSH handshakes rather than by the target's response.
const MetricKeyTunnelSetupMs = "tunnel_setup_ms"

// TunnelCheckUIDFrom extracts the tunnel check reference from a raw check
// config map. Returns ("", false) when absent, not a string, or empty.
func TunnelCheckUIDFrom(configMap map[string]any) (string, bool) {
	raw, ok := configMap[TunnelCheckUIDConfigKey].(string)
	if !ok || raw == "" {
		return "", false
	}

	return raw, true
}
