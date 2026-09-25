// Package egress is the outbound-connection policy for connections whose
// destination a user chose: check targets today, notification senders,
// redirects and importers next.
//
// On a self-hosted instance, monitoring the internal network is the product.
// On SaaS shared workers the same capability is a cross-tenant SSRF with a full
// read primitive (a check stores the response it got), so the shared workers
// refuse every non-public destination: loopback, RFC 1918, CGNAT, link-local
// (including the 169.254.169.254 cloud metadata endpoint), IPv6 ULA and
// link-local, the unspecified address, multicast and the reserved ranges.
//
// The guard lives at the DIAL layer, not on the URL string. It resolves the
// hostname once, refuses the non-public answers, and connects to the pinned IP
// it just checked — never to the hostname again — so a DNS-rebinding answer
// that flips between a public and a private address between "validate" and
// "connect" cannot slip through. A Control hook re-checks the address the
// socket actually connects to, as a last line of defense.
package egress

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"syscall"
	"time"
)

// EnvAllowPrivate is the environment variable an operator flips to let a
// worker reach non-public addresses. It is also the env override of the
// ParamAllowPrivate system parameter.
const EnvAllowPrivate = "SP_EGRESS_ALLOW_PRIVATE"

// ParamAllowPrivate is the system parameter (and config key) holding the same
// switch as EnvAllowPrivate.
const ParamAllowPrivate = "egress.allow_private_targets"

// OutputKeyDenied is the machine-readable marker a check result carries when
// its execution was refused by the egress policy.
const OutputKeyDenied = "egress_denied"

const (
	defaultDialTimeout   = 30 * time.Second
	defaultDialKeepAlive = 30 * time.Second
)

// ErrDenied is the sentinel every egress refusal matches with errors.Is.
var ErrDenied = errors.New("target resolves to a non-public address, denied by egress policy")

// DeniedError is a refusal naming what was refused and how an operator can
// allow it. It matches ErrDenied.
type DeniedError struct {
	// Host is the name (or literal) the caller asked for.
	Host string
	// IP is the non-public address it resolved to.
	IP net.IP
}

// Error tells the user what happened and exactly which knob an operator can
// flip — the one question every support ticket about it would ask.
func (e *DeniedError) Error() string {
	target := e.Host
	if ip := e.IP.String(); e.IP != nil && ip != e.Host {
		target = fmt.Sprintf("%s (%s)", e.Host, ip)
	}

	return fmt.Sprintf(
		"%s: %s; an operator can allow private targets on this worker with %s=true "+
			"(system parameter %s)",
		ErrDenied.Error(), target, EnvAllowPrivate, ParamAllowPrivate,
	)
}

// Is makes errors.Is(err, ErrDenied) true for every DeniedError.
func (e *DeniedError) Is(target error) bool {
	return target == ErrDenied
}

// nonPublicPrefixes is every range a shared worker must never connect to.
//
//nolint:gochecknoglobals // immutable lookup table
var nonPublicPrefixes = mustPrefixes(
	// IPv4
	"0.0.0.0/8",      // "this network" — 0.x connects to the local host on Linux
	"10.0.0.0/8",     // RFC 1918
	"100.64.0.0/10",  // CGNAT / cloud-internal (e.g. 100.100.100.200 metadata)
	"127.0.0.0/8",    // loopback
	"169.254.0.0/16", // link-local, incl. 169.254.169.254 cloud metadata
	"172.16.0.0/12",  // RFC 1918
	"192.0.0.0/24",   // IETF protocol assignments
	"192.168.0.0/16", // RFC 1918
	"198.18.0.0/15",  // benchmarking, used as internal space
	"224.0.0.0/4",    // multicast
	"240.0.0.0/4",    // reserved, incl. 255.255.255.255 broadcast
	// IPv6
	"::/128",    // unspecified
	"::1/128",   // loopback
	"100::/64",  // discard-only
	"fc00::/7",  // unique local (ULA)
	"fe80::/10", // link-local
	"fec0::/10", // deprecated site-local
	"ff00::/8",  // multicast
)

// nat64Prefixes embed an IPv4 address in their low 32 bits; on a NAT64
// network they reach that IPv4 address, so they are judged by it.
//
//nolint:gochecknoglobals // immutable lookup table
var nat64Prefixes = mustPrefixes("64:ff9b::/96", "64:ff9b:1::/48")

func mustPrefixes(cidrs ...string) []netip.Prefix {
	out := make([]netip.Prefix, 0, len(cidrs))
	for _, cidr := range cidrs {
		out = append(out, netip.MustParsePrefix(cidr))
	}

	return out
}

// NonPublicRanges returns the CIDR ranges IsNonPublic refuses, for docs and
// diagnostics.
func NonPublicRanges() []string {
	out := make([]string, 0, len(nonPublicPrefixes))
	for i := range nonPublicPrefixes {
		out = append(out, nonPublicPrefixes[i].String())
	}

	return out
}

// IsNonPublic reports whether ip is an address a shared worker must not
// connect to. A nil or unparseable IP is non-public: failing closed is the
// only safe answer for "I cannot tell where this goes".
func IsNonPublic(ip net.IP) bool {
	addr, ok := netip.AddrFromSlice(ip)
	if !ok {
		return true
	}

	return isNonPublicAddr(addr)
}

func isNonPublicAddr(addr netip.Addr) bool {
	addr = addr.WithZone("")

	// ::ffff:a.b.c.d is a.b.c.d on the wire.
	if addr.Is4In6() {
		addr = addr.Unmap()
	}

	if addr.Is6() {
		for i := range nat64Prefixes {
			if nat64Prefixes[i].Contains(addr) {
				raw := addr.As16()

				return isNonPublicAddr(netip.AddrFrom4([4]byte{raw[12], raw[13], raw[14], raw[15]}))
			}
		}
	}

	for i := range nonPublicPrefixes {
		if nonPublicPrefixes[i].Contains(addr) {
			return true
		}
	}

	return false
}

// LookupFunc resolves a hostname, with net.Resolver.LookupIPAddr's shape.
type LookupFunc func(ctx context.Context, host string) ([]net.IPAddr, error)

// Option configures a Guard.
type Option func(*Guard)

// WithLookup replaces the resolver the guard uses (tests, or a caller's
// retrying resolver).
func WithLookup(lookup LookupFunc) Option {
	return func(g *Guard) { g.lookup = lookup }
}

// Guard enforces the egress policy on one process's outbound connections.
//
// A nil *Guard is valid and allows everything, so callers that were never
// handed one keep their historical behavior byte-for-byte.
type Guard struct {
	allowPrivate bool
	lookup       LookupFunc
	base         net.Dialer
	// classify is IsNonPublic in production; the package's own tests swap it
	// to exercise the pinned dial against a loopback listener.
	classify func(net.IP) bool

	transportOnce sync.Once
	transport     *http.Transport
}

// New builds a guard. allowPrivate=true is the self-hosted / private-agent
// posture (no filtering at all); false refuses every non-public destination.
func New(allowPrivate bool, opts ...Option) *Guard {
	guard := &Guard{
		allowPrivate: allowPrivate,
		lookup:       net.DefaultResolver.LookupIPAddr,
		classify:     IsNonPublic,
		base:         net.Dialer{Timeout: defaultDialTimeout, KeepAlive: defaultDialKeepAlive},
	}

	for _, opt := range opts {
		opt(guard)
	}

	return guard
}

// Enforcing reports whether the guard refuses non-public destinations. False
// for a nil guard.
func (g *Guard) Enforcing() bool {
	return g != nil && !g.allowPrivate
}

// AllowsPrivate is the inverse of Enforcing.
func (g *Guard) AllowsPrivate() bool {
	return !g.Enforcing()
}

func (g *Guard) isNonPublic(ip net.IP) bool {
	if g == nil || g.classify == nil {
		return IsNonPublic(ip)
	}

	return g.classify(ip)
}

// CheckIP returns a *DeniedError when the guard is enforcing and ip is
// non-public, nil otherwise.
func (g *Guard) CheckIP(ip net.IP) error {
	return g.CheckHostIP(ip.String(), ip)
}

// CheckHostIP is CheckIP naming the host the address was resolved from, so
// the error tells the user which name led to the refusal.
func (g *Guard) CheckHostIP(host string, ip net.IP) error {
	if !g.Enforcing() || !g.isNonPublic(ip) {
		return nil
	}

	return &DeniedError{Host: host, IP: ip}
}

// CheckAddr is CheckHostIP recording the refusal on ctx's Recorder, for
// callers that validate an address themselves before handing it to a library.
func (g *Guard) CheckAddr(ctx context.Context, host string, ip net.IP) error {
	if !g.Enforcing() || !g.isNonPublic(ip) {
		return nil
	}

	return g.denied(ctx, host, ip)
}

// denied records the refusal on ctx's recorder (if any) and returns it.
func (g *Guard) denied(ctx context.Context, host string, ip net.IP) error {
	err := &DeniedError{Host: host, IP: ip}
	record(ctx, err)

	return err
}

// Resolve resolves host and returns the addresses the guard allows, in
// resolver order. With a non-enforcing guard every address is returned. When
// enforcing and nothing public remains, the error is a *DeniedError naming
// the first refused address. An IP literal resolves to itself.
//
// Callers must connect to one of the returned IPs, never re-resolve the name:
// that is what makes the check immune to DNS rebinding.
func (g *Guard) Resolve(ctx context.Context, host string) ([]net.IP, error) {
	host = strings.TrimSuffix(strings.TrimPrefix(host, "["), "]")

	if ip := net.ParseIP(host); ip != nil {
		if g.Enforcing() && g.isNonPublic(ip) {
			return nil, g.denied(ctx, host, ip)
		}

		return []net.IP{ip}, nil
	}

	lookup := net.DefaultResolver.LookupIPAddr
	if g != nil && g.lookup != nil {
		lookup = g.lookup
	}

	addrs, err := lookup(ctx, host)
	if err != nil {
		return nil, err
	}

	out := make([]net.IP, 0, len(addrs))

	var refused net.IP

	for i := range addrs {
		ip := addrs[i].IP

		if g.Enforcing() && g.isNonPublic(ip) {
			if refused == nil {
				refused = ip
			}

			continue
		}

		out = append(out, ip)
	}

	if len(out) == 0 && refused != nil {
		return nil, g.denied(ctx, host, refused)
	}

	if len(out) == 0 {
		return nil, &net.DNSError{Err: "no such host", Name: host, IsNotFound: true}
	}

	return out, nil
}

// ResolveOne is Resolve returning the first allowed address, for callers that
// hand a single pinned literal to a library that dials by itself.
func (g *Guard) ResolveOne(ctx context.Context, host string) (net.IP, error) {
	ips, err := g.Resolve(ctx, host)
	if err != nil {
		return nil, err
	}

	return ips[0], nil
}

// DialContext dials addr under the policy with the guard's default dialer
// settings (30s connect timeout, 30s keep-alive, like http.DefaultTransport).
func (g *Guard) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	var base *net.Dialer
	if g != nil {
		base = &g.base
	}

	return g.DialContextWith(ctx, base, network, addr)
}

// DialContextWith dials addr with the given dialer's settings (timeout,
// keep-alive, local address) under the policy.
//
// Non-enforcing: exactly base.DialContext(ctx, network, addr).
//
// Enforcing: the host is resolved ONCE, non-public answers are dropped, and
// the remaining pinned IPs are dialed in order until one connects. The
// dialer's Control hook re-checks the address the socket connects to. The
// network's family is honored (tcp4 / udp6 only consider that family).
func (g *Guard) DialContextWith(
	ctx context.Context, base *net.Dialer, network, addr string,
) (net.Conn, error) {
	if base == nil {
		base = &net.Dialer{Timeout: defaultDialTimeout, KeepAlive: defaultDialKeepAlive}
	}

	if !g.Enforcing() {
		return base.DialContext(ctx, network, addr)
	}

	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, fmt.Errorf("invalid address %q: %w", addr, err)
	}

	ips, err := g.Resolve(ctx, host)
	if err != nil {
		return nil, err
	}

	ips = filterFamily(ips, network)
	if len(ips) == 0 {
		return nil, &net.AddrError{Err: "no suitable address found", Addr: host}
	}

	dialer := *base
	dialer.Control = g.controlFor(recorderFrom(ctx), host, base.Control)

	var lastErr error

	for _, ip := range ips {
		conn, dialErr := dialer.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
		if dialErr == nil {
			return conn, nil
		}

		lastErr = dialErr

		if errors.Is(dialErr, ErrDenied) || ctx.Err() != nil {
			break
		}
	}

	return nil, lastErr
}

// ControlContext is Control recording refusals on ctx's Recorder. Hand it to
// libraries that accept a *net.Dialer but resolve names themselves: the hook
// sees the literal address of every connect(2), after any resolution.
func (g *Guard) ControlContext(ctx context.Context) func(string, string, syscall.RawConn) error {
	return g.controlFor(recorderFrom(ctx), "", nil)
}

// Control is a net.Dialer.Control hook refusing a connect(2) towards a
// non-public address when the guard is enforcing. It sees the literal address
// the kernel is about to connect to, after every resolution — the backstop for
// any path that resolved on its own.
func (g *Guard) Control(network, address string, rawConn syscall.RawConn) error {
	return g.controlFor(nil, "", nil)(network, address, rawConn)
}

func (g *Guard) controlFor(
	rec *Recorder, host string,
	next func(string, string, syscall.RawConn) error,
) func(string, string, syscall.RawConn) error {
	return func(network, address string, rawConn syscall.RawConn) error {
		if g.Enforcing() {
			ipStr, _, err := net.SplitHostPort(address)
			if err != nil {
				ipStr = address
			}

			ip := net.ParseIP(ipStr)
			if g.isNonPublic(ip) {
				name := host
				if name == "" {
					name = ipStr
				}

				denied := &DeniedError{Host: name, IP: ip}
				rec.record(denied)

				return denied
			}
		}

		if next != nil {
			return next(network, address, rawConn)
		}

		return nil
	}
}

// HTTPTransport returns one shared, pooled *http.Transport whose every dial
// goes through the guard — the enforcing counterpart of http.DefaultTransport,
// so an HTTP check keeps its connection reuse under the policy.
//
// The environment proxy is deliberately disabled: a proxy would perform the
// real connection on our behalf, from where we cannot see the destination.
func (g *Guard) HTTPTransport() *http.Transport {
	g.transportOnce.Do(func() {
		base, ok := http.DefaultTransport.(*http.Transport)
		if ok {
			g.transport = base.Clone()
		} else {
			g.transport = &http.Transport{}
		}

		g.transport.Proxy = nil
		g.transport.DialContext = g.DialContext
	})

	return g.transport
}

func filterFamily(ips []net.IP, network string) []net.IP {
	want4 := strings.HasSuffix(network, "4")
	want6 := strings.HasSuffix(network, "6")

	if !want4 && !want6 {
		return ips
	}

	out := make([]net.IP, 0, len(ips))

	for _, ip := range ips {
		is4 := ip.To4() != nil
		if (want4 && is4) || (want6 && !is4) {
			out = append(out, ip)
		}
	}

	return out
}

type guardCtxKey struct{}

type recorderCtxKey struct{}

// WithGuard returns a context carrying the guard every outbound connection of
// the current execution must go through.
func WithGuard(ctx context.Context, g *Guard) context.Context {
	return context.WithValue(ctx, guardCtxKey{}, g)
}

// FromContext returns the guard carried by ctx, or nil (allow everything).
func FromContext(ctx context.Context) *Guard {
	if ctx == nil {
		return nil
	}

	g, _ := ctx.Value(guardCtxKey{}).(*Guard)

	return g
}

// Recorder remembers the first refusal of one execution, so the caller can
// report it uniformly whatever the code path that hit it did with the error.
type Recorder struct {
	mu    sync.Mutex
	first *DeniedError
}

// WithRecorder returns a context whose refusals are recorded on the returned
// Recorder.
func WithRecorder(ctx context.Context) (context.Context, *Recorder) {
	rec := &Recorder{}

	return context.WithValue(ctx, recorderCtxKey{}, rec), rec
}

// Denied returns the first refusal recorded, or nil.
func (r *Recorder) Denied() *DeniedError {
	if r == nil {
		return nil
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	return r.first
}

// RecordDenial records err on ctx's Recorder when it is (or wraps) a refusal.
// For dialers invoked by a library with a context of its own (gRPC, database
// drivers): the refusal happened on that context, the recorder lives on the
// execution's.
func RecordDenial(ctx context.Context, err error) {
	var denied *DeniedError
	if errors.As(err, &denied) {
		record(ctx, denied)
	}
}

func record(ctx context.Context, err *DeniedError) {
	recorderFrom(ctx).record(err)
}

func recorderFrom(ctx context.Context) *Recorder {
	if ctx == nil {
		return nil
	}

	rec, _ := ctx.Value(recorderCtxKey{}).(*Recorder)

	return rec
}

func (r *Recorder) record(err *DeniedError) {
	if r == nil {
		return
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if r.first == nil {
		r.first = err
	}
}
