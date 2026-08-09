// Package checkdnsbl provides DNS blocklist (DNSBL) monitoring checks.
//
// A DNSBL query inverts the usual DNS semantics: an A-record answer means the
// target IP IS listed (bad), while NXDOMAIN means it is NOT listed (good).
package checkdnsbl

import (
	"context"
	"errors"
	"fmt"
	"net"
	"slices"
	"strings"
	"time"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

const (
	defaultTimeout = 10 * time.Second
	maxTimeout     = 30 * time.Second
)

var (
	errTargetNotIPv4 = errors.New("target is not an IPv4 address")
	errTargetNoIPv4  = errors.New("target resolved to no IPv4 addresses")
	// errTargetIPv6Unsupported explains why a dnsbl check cannot be pinned to
	// IPv6: DNSBL zones are indexed by reversed IPv4 octets, and the IPv6
	// nibble form is a separate feature nobody has asked for yet.
	errTargetIPv6Unsupported = errors.New(
		"dnsbl checks query IPv4 blocklists only, so this check cannot be pinned to IPv6")
)

// hostLookuper is the minimal resolver surface the checker needs. It lets tests
// inject a map-backed fake so they never touch the network.
type hostLookuper interface {
	LookupHost(ctx context.Context, host string) ([]string, error)
}

// DNSBLChecker implements the Checker interface for DNSBL (blocklist) checks.
type DNSBLChecker struct {
	// lookuper is the resolver used for both target resolution and zone queries.
	// When nil, Execute builds one from the config (system resolver or custom
	// nameserver). Tests set it directly.
	lookuper hostLookuper
}

// Type returns the check type identifier.
func (c *DNSBLChecker) Type() checkerdef.CheckType {
	return checkerdef.CheckTypeDNSBL
}

// Validate checks if the configuration is valid. It performs no network operations.
func (c *DNSBLChecker) Validate(spec *checkerdef.CheckSpec) error {
	cfg := &DNSBLConfig{}
	if err := cfg.FromMap(spec.Config); err != nil {
		return err
	}

	if cfg.Target == "" {
		return checkerdef.NewConfigError(keyTarget, "is required")
	}

	if cfg.Nameserver != "" && !strings.Contains(cfg.Nameserver, ":") {
		return checkerdef.NewConfigErrorf("nameserver", "must be in format host:port, got %s", cfg.Nameserver)
	}

	if cfg.Timeout != 0 && (cfg.Timeout <= 0 || cfg.Timeout > maxTimeout) {
		return checkerdef.NewConfigErrorf("timeout", "must be > 0 and <= 30s, got %s", cfg.Timeout.String())
	}

	return nil
}

// Execute performs the DNSBL check and returns the result.
//
//nolint:funlen // DNSBL checking aggregates several zones with inverted DNS semantics
func (c *DNSBLChecker) Execute(ctx context.Context, config checkerdef.Config) (*checkerdef.Result, error) {
	cfg, err := checkerdef.AssertConfig[*DNSBLConfig](config)
	if err != nil {
		return nil, err
	}

	timeout := cfg.Timeout
	if timeout == 0 {
		timeout = defaultTimeout
	}

	lookuper := c.lookuper
	if lookuper == nil {
		lookuper = createResolver(cfg.Nameserver, timeout)
	}

	zones := cfg.resolveBlocklists()
	start := time.Now()

	// 1. Resolve the target to one or more IPv4 addresses.
	ips, err := resolveTargetIPs(ctx, lookuper, cfg.Target, checkerdef.IPVersionFrom(ctx))
	if err != nil {
		return resolveTargetFailure(cfg.Target, err, start), nil
	}

	// 2. Query each IP against each zone, classifying with inverted DNS semantics.
	res := newZoneResults()

	for _, ip := range ips {
		reversed := reverseIP(ip)
		if reversed == "" {
			continue
		}

		for _, zone := range zones {
			query := reversed + "." + zone

			addrs, lookupErr := lookuper.LookupHost(ctx, query)
			switch {
			case lookupErr == nil && len(addrs) > 0:
				// A non-empty answer may still be an error/status reply
				// (127.255.255.x), which must not count as a listing.
				res.recordAnswer(zone, addrs)
			case isNotFound(lookupErr):
				res.clean[zone] = true
			default:
				res.inconclusive[zone] = true
			}
		}
	}

	duration := time.Since(start)

	listedOn := sortedKeysExcept(res.listed, nil)
	clean := sortedKeysExcept(res.clean, res.listed)
	inconclusive := sortedKeysExcept(res.inconclusive, mergeSets(res.listed, res.clean))

	status := classifyStatus(len(listedOn), len(clean), len(inconclusive))

	result := &checkerdef.Result{
		Status:   status,
		Duration: duration,
		Metrics: map[string]any{
			"listings":           len(listedOn),
			"blocklists_checked": len(zones),
			"query_time_ms":      float64(duration.Milliseconds()),
		},
		Output: map[string]any{
			keyTarget:      cfg.Target,
			"target_ip":    strings.Join(ips, ","),
			"listed_on":    listedOn,
			"clean":        clean,
			"inconclusive": inconclusive,
		},
	}

	if len(res.returnCodes) > 0 {
		result.Output["return_codes"] = res.returnCodes
	}

	// Surface reserved 127.255.255.x error/status codes (e.g. .254 = query via
	// public resolver refused) so operators see why a zone was inconclusive.
	if len(res.errorCodes) > 0 {
		result.Output["error_codes"] = res.errorCodes
	}

	if cfg.Nameserver != "" {
		result.Output["nameserver"] = cfg.Nameserver
	}

	return result, nil
}

// zoneResults accumulates per-zone classification while querying DNSBL zones.
// listed/clean/inconclusive are zone sets; returnCodes holds genuine listing
// addresses and errorCodes holds reserved 127.255.255.x error/status replies.
type zoneResults struct {
	listed       map[string]bool
	clean        map[string]bool
	inconclusive map[string]bool
	returnCodes  map[string][]string
	errorCodes   map[string][]string
}

// newZoneResults returns a zoneResults with all maps initialized.
func newZoneResults() *zoneResults {
	return &zoneResults{
		listed:       map[string]bool{},
		clean:        map[string]bool{},
		inconclusive: map[string]bool{},
		returnCodes:  map[string][]string{},
		errorCodes:   map[string][]string{},
	}
}

// recordAnswer classifies a non-empty zone answer. Genuine 127.0.0.x listings
// mark the zone listed and are recorded under returnCodes; a set containing only
// reserved 127.255.255.x error codes marks the zone inconclusive instead. Any
// error codes are always surfaced under errorCodes.
func (r *zoneResults) recordAnswer(zone string, addrs []string) {
	listings, errCodes := partitionDNSBLAddrs(addrs)

	if len(listings) > 0 {
		r.listed[zone] = true
		r.returnCodes[zone] = appendUnique(r.returnCodes[zone], listings)
	} else {
		r.inconclusive[zone] = true
	}

	if len(errCodes) > 0 {
		r.errorCodes[zone] = appendUnique(r.errorCodes[zone], errCodes)
	}
}

// classifyStatus maps zone counts to a check status per the spec table:
//   - listed on ≥1 zone        → Down
//   - clean on ≥1 zone         → Up
//   - every zone inconclusive  → Timeout
func classifyStatus(listed, clean, inconclusive int) checkerdef.Status {
	switch {
	case listed > 0:
		return checkerdef.StatusDown
	case clean > 0:
		return checkerdef.StatusUp
	case inconclusive > 0:
		return checkerdef.StatusTimeout
	default:
		// No zones queried at all — treat as clean.
		return checkerdef.StatusUp
	}
}

// resolveTargetFailure renders a target-resolution failure.
//
// A hostname that does not resolve cannot be checked, which is Down — the
// historical verdict. The one exception is an `ipVersion: ipv6` dnsbl check: it
// is a configuration that cannot exist (the write-time gate rejects it) and says
// nothing about the target's reputation, so it must not be recorded as the
// target being unreachable. StatusError, like every other "this check cannot run
// as configured" case. See checkerdef/ERRORS.md.
func resolveTargetFailure(target string, err error, start time.Time) *checkerdef.Result {
	status := checkerdef.StatusDown
	if errors.Is(err, errTargetIPv6Unsupported) {
		status = checkerdef.StatusError
	}

	return &checkerdef.Result{
		Status:   status,
		Duration: time.Since(start),
		Output: map[string]any{
			keyTarget:                 target,
			checkerdef.OutputKeyError: fmt.Sprintf("failed to resolve target: %v", err),
		},
	}
}

// resolveTargetIPs returns the IPv4 addresses for the target. When the target
// is already an IPv4 literal it is returned as-is.
//
// The IPv4 restriction here is a PROTOCOL constraint, not an address-family
// preference: a DNSBL zone is queried by reversing the octets of an IPv4
// address (1.2.3.4 -> 4.3.2.1.zone), and the IPv6 nibble form is a different,
// unimplemented feature. So the family filter goes through the shared
// checkerdef predicate — there is no local preference loop — and the check
// accepts `ipVersion: auto` and `ipVersion: ipv4` as the same thing. An
// `ipVersion: ipv6` dnsbl check is rejected at write time (by the checks
// service's validateIPVersionConfig); the guard below only exists for a
// hand-edited row that predates that gate.
func resolveTargetIPs(
	ctx context.Context, lookuper hostLookuper, target string, version checkerdef.IPVersion,
) ([]string, error) {
	if version == checkerdef.IPVersionIPv6 {
		return nil, fmt.Errorf("%w: %q", errTargetIPv6Unsupported, target)
	}

	if ip := net.ParseIP(target); ip != nil {
		if !checkerdef.MatchesIPVersion(ip, checkerdef.IPVersionIPv4) {
			return nil, fmt.Errorf("%w: %q", errTargetNotIPv4, target)
		}

		return []string{ip.String()}, nil
	}

	addrs, err := lookuper.LookupHost(ctx, target)
	if err != nil {
		return nil, err
	}

	ips := make([]string, 0, len(addrs))

	for _, addr := range addrs {
		ip := net.ParseIP(addr)
		if ip != nil && checkerdef.MatchesIPVersion(ip, checkerdef.IPVersionIPv4) {
			ips = append(ips, ip.String())
		}
	}

	if len(ips) == 0 {
		return nil, fmt.Errorf("%w: %q", errTargetNoIPv4, target)
	}

	return ips, nil
}

// reverseIP returns the reversed-octet form of an IPv4 address used for DNSBL
// queries (1.2.3.4 → 4.3.2.1). It returns "" for non-IPv4 input.
func reverseIP(ipStr string) string {
	ip := net.ParseIP(ipStr)
	if ip == nil {
		return ""
	}

	ip4 := ip.To4()
	if ip4 == nil {
		return ""
	}

	return fmt.Sprintf("%d.%d.%d.%d", ip4[3], ip4[2], ip4[1], ip4[0])
}

// isDNSBLErrorCode reports whether addr is a reserved DNSBL error/status reply.
//
// DNSBLs use the 127.255.255.0/24 range for non-listing replies rather than
// genuine listings — Spamhaus, for example, returns 127.255.255.252 (typo /
// malformed zone), 127.255.255.254 (query via a public/open resolver, refused),
// and 127.255.255.255 (query volume / rate-limit exceeded). No legitimate DNSBL
// encodes a real listing in 127.255.255.x, so the whole /24 is treated as an
// error range.
func isDNSBLErrorCode(addr string) bool {
	ip := net.ParseIP(addr)
	if ip == nil {
		return false
	}

	ip4 := ip.To4()
	if ip4 == nil {
		return false
	}

	return ip4[0] == 127 && ip4[1] == 255 && ip4[2] == 255
}

// partitionDNSBLAddrs splits a zone's answer set into genuine listing addresses
// and reserved 127.255.255.x error/status codes. The error codes must not be
// counted as listings (see isDNSBLErrorCode).
func partitionDNSBLAddrs(addrs []string) ([]string, []string) {
	var listings, errCodes []string

	for _, addr := range addrs {
		if isDNSBLErrorCode(addr) {
			errCodes = append(errCodes, addr)
		} else {
			listings = append(listings, addr)
		}
	}

	return listings, errCodes
}

// isNotFound reports whether err is an NXDOMAIN / host-not-found DNS error,
// which for a DNSBL means the target is NOT listed (clean).
func isNotFound(err error) bool {
	if err == nil {
		return false
	}

	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return dnsErr.IsNotFound
	}

	return false
}

// createResolver builds a hostLookuper from an optional custom nameserver.
func createResolver(nameserver string, timeout time.Duration) hostLookuper {
	if nameserver == "" {
		return &net.Resolver{}
	}

	return &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, _, _ string) (net.Conn, error) {
			d := net.Dialer{Timeout: timeout}

			return d.DialContext(ctx, "udp", nameserver)
		},
	}
}

// appendUnique appends values from add to base, skipping duplicates.
func appendUnique(base, add []string) []string {
	seen := make(map[string]bool, len(base))
	for _, v := range base {
		seen[v] = true
	}

	for _, v := range add {
		if !seen[v] {
			base = append(base, v)
			seen[v] = true
		}
	}

	return base
}

// sortedKeysExcept returns the sorted keys of set that are not present in except.
func sortedKeysExcept(set, except map[string]bool) []string {
	keys := make([]string, 0, len(set))

	for k := range set {
		if except != nil && except[k] {
			continue
		}

		keys = append(keys, k)
	}

	slices.Sort(keys)

	return keys
}

// mergeSets returns the union of two string sets.
func mergeSets(first, second map[string]bool) map[string]bool {
	out := make(map[string]bool, len(first)+len(second))
	for k := range first {
		out[k] = true
	}

	for k := range second {
		out[k] = true
	}

	return out
}
