package checkerdef

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"
)

// IPVersion is the address family a check is pinned to.
//
// It exists because "the check is up" over IPv4 says nothing about whether the
// same target answers over IPv6: a dual-stack host with a broken AAAA path
// (missing firewall rule, dead v6 route, load balancer listening on v4 only)
// keeps reporting up while every IPv6 user is down. Pinning a check to a family
// is the only way to express "monitor this host over IPv6" without hardcoding a
// literal address and losing DNS coverage.
type IPVersion string

// Address families a check can be pinned to.
const (
	// IPVersionAuto is the zero value and the default: pick one address
	// exactly the way the checker did before this option existed (IPv4-first
	// for the address-picking checkers, Happy Eyeballs for HTTP). It
	// deliberately does NOT mean "probe both families" — that would need two
	// probes and a rollup status; a user wanting both coverage creates two
	// checks. This is the one place SolidPing knowingly diverges from Better
	// Stack, whose null value means "use both".
	IPVersionAuto IPVersion = "auto"

	// IPVersionIPv4 pins the check to an A record / IPv4 address.
	IPVersionIPv4 IPVersion = "ipv4"

	// IPVersionIPv6 pins the check to an AAAA record / IPv6 address.
	IPVersionIPv6 IPVersion = "ipv6"
)

const (
	// IPVersionConfigKey is the canonical (camelCase) check-config key holding
	// the requested address family. Like `timeout` and `tunnelCheckUid` it is a
	// shared, well-known key read generically off the raw config map rather
	// than a field on every checker's config struct — so a checker gaining
	// support needs no config-struct change, and address-family selection has
	// exactly one implementation.
	IPVersionConfigKey = "ipVersion"

	// IPVersionConfigKeyLegacy is the snake_case spelling, accepted on read for
	// configs written by hand or by an older client. camelCase is canonical.
	IPVersionConfigKeyLegacy = "ip_version"

	// OutputKeyIPVersion is the result-output key reporting the family a check
	// actually used. It predates the input option (tcp/udp/icmp already
	// reported it) and keeps its shape: the string "ipv4" or "ipv6".
	OutputKeyIPVersion = "ip_version"
)

// ErrInvalidIPVersion is returned for a value that is not auto/ipv4/ipv6.
var ErrInvalidIPVersion = errors.New("invalid ipVersion")

// ErrNoAddressForFamily is the catalogued failure for "the target has no
// address of the requested family" — usually a missing AAAA record. It is
// deliberately distinct from a dial failure: the target may be perfectly
// healthy, it simply is not reachable over the family this check pins.
var ErrNoAddressForFamily = errors.New("no address of the requested IP version")

// ErrWorkerNoEgress is the catalogued failure for "this worker cannot send
// traffic over the requested family at all" — the node running the check has no
// IPv6 route, not the target being down. Kept separate from
// ErrNoAddressForFamily and from any dial error so the message can point the
// user at their region/worker instead of at their own service.
var ErrWorkerNoEgress = errors.New("worker has no egress for the requested IP version")

// egressProbePort is the port the local-egress pre-flight "connects" to. A
// connected UDP socket sends no packets — the syscall only performs a route
// lookup — so the port is never contacted and its value is irrelevant. 9 is
// the discard port.
const egressProbePort = 9

// egressProbeTimeout bounds the pre-flight. A route lookup is effectively
// instantaneous; the timeout only exists so a pathological resolver-free dial
// can never stall a check.
const egressProbeTimeout = time.Second

// String renders the family for output and error messages.
func (v IPVersion) String() string {
	return string(v)
}

// Valid reports whether v is one of the three accepted values. The empty
// string is valid and means auto.
func (v IPVersion) Valid() bool {
	switch v {
	case "", IPVersionAuto, IPVersionIPv4, IPVersionIPv6:
		return true
	default:
		return false
	}
}

// Explicit reports whether the check pinned a specific family. Everything in
// this package treats "not explicit" as "behave exactly as before the option
// existed".
func (v IPVersion) Explicit() bool {
	return v == IPVersionIPv4 || v == IPVersionIPv6
}

// Label renders the family the way users write it in prose and error messages.
func (v IPVersion) Label() string {
	switch v {
	case IPVersionIPv4:
		return "IPv4"
	case IPVersionIPv6:
		return "IPv6"
	default:
		return "auto"
	}
}

// Network qualifies a base network name ("tcp", "udp", "ip") with the address
// family, yielding the "tcp4"/"tcp6" forms net.Dial understands. auto returns
// the base name unchanged, which is what keeps the default path byte-for-byte
// identical.
func (v IPVersion) Network(base string) string {
	switch v {
	case IPVersionIPv4:
		return base + "4"
	case IPVersionIPv6:
		return base + "6"
	default:
		return base
	}
}

// ParseIPVersion normalizes a user-supplied value. Empty, "auto" and unset all
// yield IPVersionAuto. Comparison is case-insensitive and surrounding
// whitespace is tolerated, but no aliases are invented: "v4", "4", "inet" and
// friends are rejected so a typo fails loudly at write time rather than
// silently monitoring the wrong family.
func ParseIPVersion(raw string) (IPVersion, error) {
	normalized := IPVersion(strings.ToLower(strings.TrimSpace(raw)))

	switch normalized {
	case "", IPVersionAuto:
		return IPVersionAuto, nil
	case IPVersionIPv4, IPVersionIPv6:
		return normalized, nil
	default:
		return IPVersionAuto, fmt.Errorf(
			"%w: must be one of auto, ipv4 or ipv6, got %q", ErrInvalidIPVersion, raw)
	}
}

// IPVersionFromConfig reads the requested family off a raw check-config map,
// accepting the canonical camelCase key and the snake_case fallback. A missing
// key yields IPVersionAuto; a present-but-wrong value is an error, never a
// silent fallback to auto.
func IPVersionFromConfig(configMap map[string]any) (IPVersion, error) {
	for _, key := range []string{IPVersionConfigKey, IPVersionConfigKeyLegacy} {
		raw, ok := configMap[key]
		if !ok || raw == nil {
			continue
		}

		str, ok := raw.(string)
		if !ok {
			return IPVersionAuto, fmt.Errorf(
				"%w: must be a string (auto, ipv4 or ipv6), got %T", ErrInvalidIPVersion, raw)
		}

		return ParseIPVersion(str)
	}

	return IPVersionAuto, nil
}

// ipVersionCtxKeyType makes the context key unique within the binary.
type ipVersionCtxKeyType struct{}

//nolint:gochecknoglobals // sentinel value for a context key
var ipVersionCtxKey = ipVersionCtxKeyType{}

// WithIPVersion returns a context carrying the address family every outbound
// connection of the check must use. The worker sets it once per execution from
// the check's config; checkers only consume it. Mirrors WithTunnelDialer.
func WithIPVersion(ctx context.Context, version IPVersion) context.Context {
	return context.WithValue(ctx, ipVersionCtxKey, version)
}

// IPVersionFrom returns the family carried by ctx, or IPVersionAuto when the
// check did not pin one (the overwhelmingly common case — callers must keep
// their auto path byte-for-byte unchanged).
func IPVersionFrom(ctx context.Context) IPVersion {
	if version, ok := ctx.Value(ipVersionCtxKey).(IPVersion); ok {
		return version
	}

	return IPVersionAuto
}

// MatchesIPVersion reports whether ip belongs to the requested family. auto
// matches everything.
func MatchesIPVersion(ip net.IP, version IPVersion) bool {
	switch version {
	case IPVersionIPv4:
		return ip.To4() != nil
	case IPVersionIPv6:
		return ip.To4() == nil && ip.To16() != nil
	default:
		return true
	}
}

// IPVersionOf reports the family an address belongs to, for the `ip_version`
// result-output field. Every checker that reports the family goes through this
// rather than rolling its own `To4()` test, so the reported strings can never
// drift apart between check types.
func IPVersionOf(ip net.IP) IPVersion {
	if ip.To4() != nil {
		return IPVersionIPv4
	}

	return IPVersionIPv6
}

// IPVersionFailureStatus maps an address-selection failure to the status the
// check should report.
//
// "The target has no AAAA record" is a genuine failure of the thing being
// monitored — an IPv6 user cannot reach it — so it is StatusDown. "This worker
// has no IPv6 egress" is our own infrastructure gap and must not be recorded as
// the target being down, so it is StatusError.
func IPVersionFailureStatus(err error) Status {
	if errors.Is(err, ErrWorkerNoEgress) {
		return StatusError
	}

	return StatusDown
}

// EgressProbe reports whether this worker can originate traffic of the given
// family towards ip. It is the seam SelectIPAddr uses so tests can drive the
// "worker has no IPv6" branch without an actual v6-less host.
type EgressProbe func(version IPVersion, ip net.IP) error

// SelectIPAddr picks the single address a check dials, out of the addresses the
// resolver returned. It is THE address-family decision for every checker that
// resolves before dialing — no checker keeps its own preference loop, because
// nine copies of that loop is exactly how this option would rot back into
// per-type inconsistency.
//
// Behaviour:
//   - auto reproduces the historical pick byte-for-byte: the first IPv4
//     address, normalized to its 4-byte form (checkicmp relied on that), and
//     otherwise the first address the resolver returned. No egress probe runs,
//     so an auto check performs exactly the syscalls it always did.
//   - ipv4/ipv6 filter to that family and never fall back — falling back is
//     what makes an "IPv6 check" silently an IPv4 check. With no address of the
//     family, the check fails with ErrNoAddressForFamily naming the host and
//     the family, not a generic dial error.
//   - ipv4/ipv6 additionally pre-flight local egress, so a worker with no IPv6
//     route reports ErrWorkerNoEgress ("look at your region") instead of a
//     network-unreachable dial error that reads as "your target is down".
func SelectIPAddr(host string, addrs []net.IPAddr, version IPVersion) (net.IP, error) {
	return SelectIPAddrWithProbe(host, addrs, version, DialEgressProbe)
}

// SelectIPAddrWithProbe is SelectIPAddr with the local-egress pre-flight
// injectable. Production code calls SelectIPAddr; tests use this to exercise
// the worker-has-no-IPv6 branch deterministically. A nil probe disables the
// pre-flight.
func SelectIPAddrWithProbe(
	host string, addrs []net.IPAddr, version IPVersion, probe EgressProbe,
) (net.IP, error) {
	if len(addrs) == 0 {
		return nil, fmt.Errorf("%w: %q resolved to no addresses", ErrNoAddressForFamily, host)
	}

	if !version.Explicit() {
		return autoIPAddr(addrs), nil
	}

	for i := range addrs {
		if !MatchesIPVersion(addrs[i].IP, version) {
			continue
		}

		ip := normalizeIP(addrs[i].IP)

		if probe != nil {
			if err := probe(version, ip); err != nil {
				return nil, err
			}
		}

		return ip, nil
	}

	return nil, fmt.Errorf(
		"%w: %s has no %s address (this check is pinned to %s; remove the %s option or publish "+
			"an %s record for it)",
		ErrNoAddressForFamily, host, version.Label(), version.Label(),
		IPVersionConfigKey, recordTypeFor(version),
	)
}

// autoIPAddr is the historical pick, kept in one place: first IPv4, else the
// first address returned. Changing this changes every existing check on every
// dual-stack target in every install, so it is deliberately dumb and stable.
func autoIPAddr(addrs []net.IPAddr) net.IP {
	for i := range addrs {
		if v4 := addrs[i].IP.To4(); v4 != nil {
			return v4
		}
	}

	return addrs[0].IP
}

// normalizeIP renders an IPv4 address in its canonical 4-byte form (what
// To4() returns) and leaves IPv6 untouched, so every checker sees the same
// representation the IPv4-first loops used to produce.
func normalizeIP(ip net.IP) net.IP {
	if v4 := ip.To4(); v4 != nil {
		return v4
	}

	return ip
}

// recordTypeFor names the DNS record a user would have to publish to satisfy
// the pinned family — the actionable half of the error message.
func recordTypeFor(version IPVersion) string {
	if version == IPVersionIPv6 {
		return "AAAA"
	}

	return "A"
}

// DialEgressProbe is the production EgressProbe: it opens a *connected* UDP
// socket towards ip. No packet is ever sent — connect(2) on a datagram socket
// only performs a route lookup — so this costs microseconds and never touches
// the target. A missing route (ENETUNREACH) or an unsupported family
// (EAFNOSUPPORT) surfaces here, which is precisely the "this worker has no IPv6"
// case we must not report as the target being down.
//
// It is best-effort by construction: a host with a default v6 route into a
// blackhole passes the probe and the check then fails on the real dial. That is
// acceptable — the probe only ever upgrades an error message, never downgrades
// one.
func DialEgressProbe(version IPVersion, ip net.IP) error {
	if !version.Explicit() {
		return nil
	}

	network := version.Network("udp")
	address := net.JoinHostPort(ip.String(), fmt.Sprint(egressProbePort))

	dialer := net.Dialer{Timeout: egressProbeTimeout}

	conn, err := dialer.Dial(network, address)
	if err != nil {
		return fmt.Errorf(
			"%w: this worker has no %s connectivity, so the check could not even be sent — "+
				"the target is probably fine; run this check from a region with %s egress, "+
				"or set %s back to auto (%v)",
			ErrWorkerNoEgress, version.Label(), version.Label(), IPVersionConfigKey, err,
		)
	}

	_ = conn.Close()

	return nil
}
