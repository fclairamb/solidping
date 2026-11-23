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

	checkconfig "github.com/fclairamb/solidping/server/internal/checkers/checkdnsbl/config"
	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/checkers/dnsblquery"
)

const (
	defaultTimeout = 10 * time.Second
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

// Validate checks if the configuration is valid. Every rule lives in the light
// `config` sub-package so an offline validator (`sp checks validate`) can run it
// without linking this checker's execution client.
func (c *DNSBLChecker) Validate(spec *checkerdef.CheckSpec) error {
	return checkconfig.ValidateSpec(spec)
}

// Execute performs the DNSBL check and returns the result.
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

	zones := cfg.ResolveBlocklists()
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

	refused := refusedZones(inconclusive, res.errorCodes)
	status := classifyStatus(len(listedOn), len(clean), len(inconclusive), len(refused))

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

	addOptionalOutput(result.Output, res, refused, cfg.Nameserver, status)

	return result, nil
}

// addOptionalOutput adds the output keys that only appear when relevant: the
// listing and error codes, the refusal message of a degraded result, and the
// custom nameserver.
func addOptionalOutput(
	output map[string]any, res *zoneResults, refused []string, nameserver string, status checkerdef.Status,
) {
	if len(res.returnCodes) > 0 {
		output["return_codes"] = res.returnCodes
	}

	// Surface reserved 127.255.255.x error/status codes (e.g. .254 = query via
	// public resolver refused) so operators see why a zone was inconclusive.
	if len(res.errorCodes) > 0 {
		output["error_codes"] = res.errorCodes
	}

	// A zone that refused to answer means "we could not check", not "listed":
	// say which zone, what its code means and how to fix it.
	if status == checkerdef.StatusWarning {
		output[checkerdef.OutputKeyMessage] = refusalMessage(refused, res.errorCodes)
	}

	if nameserver != "" {
		output["nameserver"] = nameserver
	}
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

// classifyStatus maps zone counts to a check status:
//   - listed on ≥1 zone                                  → Down
//   - ≥1 inconclusive zone refused with a 127.255.255.x  → Warning (degraded:
//     the blocklist would not answer, the target is not known to be listed)
//   - clean on ≥1 zone                                   → Up (plain timeouts or
//     SERVFAILs on other zones are transient and not reported)
//   - every zone plainly inconclusive (timeout/SERVFAIL) → Timeout
//
// refused counts the inconclusive zones that answered only with reserved
// 127.255.255.x codes; it is a subset of inconclusive.
func classifyStatus(listed, clean, inconclusive, refused int) checkerdef.Status {
	switch {
	case listed > 0:
		return checkerdef.StatusDown
	case refused > 0:
		return checkerdef.StatusWarning
	case clean > 0:
		return checkerdef.StatusUp
	case inconclusive > 0:
		return checkerdef.StatusTimeout
	default:
		// No zones queried at all — treat as clean.
		return checkerdef.StatusUp
	}
}

// refusedZones returns the inconclusive zones that answered with a reserved
// 127.255.255.x error/status code, in the (sorted) order of inconclusive.
func refusedZones(inconclusive []string, errorCodes map[string][]string) []string {
	var refused []string

	for _, zone := range inconclusive {
		if len(errorCodes[zone]) > 0 {
			refused = append(refused, zone)
		}
	}

	return refused
}

// refusalMessage renders one readable sentence per refused zone and code:
// the code, what it means and how to fix it.
func refusalMessage(refused []string, errorCodes map[string][]string) string {
	parts := make([]string, 0, len(refused))

	for _, zone := range refused {
		for _, code := range errorCodes[zone] {
			parts = append(parts, fmt.Sprintf("%s returned %s: %s", zone, code, describeErrorCode(code)))
		}
	}

	return "blocklist could not be checked, the target is not known to be listed: " + strings.Join(parts, "; ")
}

// describeErrorCode explains a reserved 127.255.255.x DNSBL reply (Spamhaus
// semantics, the only zone known to use them) and its fix.
func describeErrorCode(code string) string {
	switch code {
	case "127.255.255.254":
		return "query via a public resolver refused, use a private resolver (nameserver option) or a Spamhaus DQS key"
	case "127.255.255.255":
		return "query volume limit exceeded (rate limited), use a private resolver (nameserver option) " +
			"or a Spamhaus DQS key"
	case "127.255.255.252":
		return "typo or malformed query, check the blocklist zone name"
	default:
		return "the blocklist refused to answer (reserved error code)"
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
// queries (1.2.3.4 → 4.3.2.1). It returns "" for non-IPv4 input. The dnsbl
// checker is IPv4-only; the shared primitives live in dnsblquery.
func reverseIP(ipStr string) string { return dnsblquery.ReverseIPv4(ipStr) }

// isDNSBLErrorCode reports whether addr is a reserved DNSBL error/status reply
// (see dnsblquery.IsErrorCode).
func isDNSBLErrorCode(addr string) bool { return dnsblquery.IsErrorCode(addr) }

// partitionDNSBLAddrs splits a zone's answer set into genuine listing addresses
// and reserved 127.255.255.x error/status codes (see dnsblquery.PartitionAddrs).
func partitionDNSBLAddrs(addrs []string) ([]string, []string) {
	return dnsblquery.PartitionAddrs(addrs)
}

// isNotFound reports whether err is an NXDOMAIN / host-not-found DNS error,
// which for a DNSBL means the target is NOT listed (clean).
func isNotFound(err error) bool { return dnsblquery.IsNotFound(err) }

// createResolver builds a hostLookuper from an optional custom nameserver.
func createResolver(nameserver string, timeout time.Duration) hostLookuper {
	if nameserver == "" {
		return &net.Resolver{}
	}

	return &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, _, _ string) (net.Conn, error) {
			// A custom nameserver answers to the egress guard (spec
			// 2026-09-25-19); the system resolver is the worker's own.
			d := checkerdef.GuardDialerOr(ctx, &net.Dialer{Timeout: timeout})

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
