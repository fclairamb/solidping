package checkerdef

import (
	"context"
	"net"
	"net/http"
	"syscall"

	"github.com/fclairamb/solidping/server/internal/egress"
)

// This file is the checkers' single seam onto the egress policy (spec
// 2026-09-25-19). The worker puts an *egress.Guard on the execution context;
// checkers never read it directly, they ask for the dialer to use here.
//
// A tunnel dialer always wins over the guard: a tunneled probe connects from
// the BASTION's network, not the worker's (the SSH connection to the bastion
// itself went through the guard when the worker established the tunnel), and
// its name resolution is remote by design.

// OutboundDialer returns the dialer every outbound TCP connection of the check
// must go through, or nil when the library default is fine:
//
//   - the tunnel dialer when the check is tunneled;
//   - the egress guard when it is enforcing (a SaaS shared worker);
//   - nil otherwise, so an untunneled check on a permissive worker keeps its
//     library's own dialer byte-for-byte.
func OutboundDialer(ctx context.Context) ContextDialer {
	if dialer := TunnelDialerFrom(ctx); dialer != nil {
		return dialer
	}

	return guardDialer(ctx, nil)
}

// OutboundDialerOr is OutboundDialer with the dialer to use when neither a
// tunnel nor an enforcing guard applies. The guard reuses base's settings
// (timeout, keep-alive, local address) when it takes over.
func OutboundDialerOr(ctx context.Context, base *net.Dialer) ContextDialer {
	if dialer := TunnelDialerFrom(ctx); dialer != nil {
		return dialer
	}

	return GuardDialerOr(ctx, base)
}

// GuardDialerOr ignores tunnels: it is for the paths that can never be
// tunneled (UDP) or that dial something other than the tunneled target. It
// returns the guard (with base's settings) when enforcing, base otherwise.
func GuardDialerOr(ctx context.Context, base *net.Dialer) ContextDialer {
	if dialer := guardDialer(ctx, base); dialer != nil {
		return dialer
	}

	if base == nil {
		return nil
	}

	return base
}

func guardDialer(ctx context.Context, base *net.Dialer) ContextDialer {
	guard := egress.FromContext(ctx)
	if !guard.Enforcing() {
		return nil
	}

	return DialerFunc(func(dialCtx context.Context, network, addr string) (net.Conn, error) {
		conn, err := guard.DialContextWith(dialCtx, base, network, addr)
		if err != nil {
			// A library may dial with a context of its own; the execution's
			// recorder lives on ctx.
			egress.RecordDenial(ctx, err)
		}

		return conn, err
	})
}

// GuardedNetDialer returns a copy of base whose Control hook refuses a
// non-public connect under an enforcing guard, or base itself otherwise. It is
// for libraries that take a concrete *net.Dialer and resolve by themselves:
// the hook judges the literal address each connect(2) targets, after the
// library's own resolution, so DNS rebinding cannot get past it either.
func GuardedNetDialer(ctx context.Context, base *net.Dialer) *net.Dialer {
	if base == nil {
		base = &net.Dialer{}
	}

	guard := egress.FromContext(ctx)
	if !guard.Enforcing() {
		return base
	}

	guarded := *base
	control := guard.ControlContext(ctx)

	if next := base.Control; next != nil {
		guarded.Control = func(network, address string, c syscall.RawConn) error {
			if err := control(network, address, c); err != nil {
				return err
			}

			return next(network, address, c)
		}
	} else {
		guarded.Control = control
	}

	return &guarded
}

// GuardedHTTPClient is http.DefaultClient for the paths that used it directly,
// except under an enforcing egress policy, where it is a client on the guard's
// pooled transport. It is NOT tunnel-aware, exactly like the DefaultClient it
// replaces: use HTTPTransportFor for a check's main probe.
func GuardedHTTPClient(ctx context.Context) *http.Client {
	guard := egress.FromContext(ctx)
	if !guard.Enforcing() {
		return http.DefaultClient
	}

	return &http.Client{Transport: guard.HTTPTransport()}
}

// EgressEnforcing reports whether the execution's egress guard refuses
// non-public destinations.
func EgressEnforcing(ctx context.Context) bool {
	return egress.FromContext(ctx).Enforcing()
}

// PinTargetHost is for libraries that take a host string and dial on their
// own (no dialer hook). Under an enforcing guard it resolves host once,
// refuses a non-public answer and returns the pinned IP literal to hand to the
// library instead of the name — so the library cannot re-resolve to a
// different (rebound) address. Otherwise it returns host unchanged.
//
// Only use it where the name is not needed after the dial (no TLS SNI, no
// Host header): a TLS library must get a dialer instead.
func PinTargetHost(ctx context.Context, host string) (string, error) {
	guard := egress.FromContext(ctx)
	if !guard.Enforcing() {
		return host, nil
	}

	ip, err := guard.ResolveOne(ctx, host)
	if err != nil {
		return "", err
	}

	return ip.String(), nil
}

// CheckEgressIP refuses ip under an enforcing guard, naming host. It is for
// checkers that resolve the target themselves and then send packets with a
// primitive the guard cannot wrap (raw ICMP sockets).
func CheckEgressIP(ctx context.Context, host string, ip net.IP) error {
	return egress.FromContext(ctx).CheckAddr(ctx, host, ip)
}
