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
	maxTimeout     = 60 * time.Second
)

var (
	errTargetNotIPv4 = errors.New("target is not an IPv4 address")
	errTargetNoIPv4  = errors.New("target resolved to no IPv4 addresses")
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
		return checkerdef.NewConfigErrorf("timeout", "must be > 0 and <= 60s, got %s", cfg.Timeout.String())
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
	ips, err := resolveTargetIPs(ctx, lookuper, cfg.Target)
	if err != nil {
		// Target hostname does not resolve → cannot be checked → Down.
		return &checkerdef.Result{
			Status:   checkerdef.StatusDown,
			Duration: time.Since(start),
			Output: map[string]any{
				keyTarget:                 cfg.Target,
				checkerdef.OutputKeyError: fmt.Sprintf("failed to resolve target: %v", err),
			},
		}, nil
	}

	// 2. Query each IP against each zone, classifying with inverted DNS semantics.
	listedSet := map[string]bool{}
	cleanSet := map[string]bool{}
	inconclusiveSet := map[string]bool{}
	returnCodes := map[string][]string{}

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
				listedSet[zone] = true

				returnCodes[zone] = appendUnique(returnCodes[zone], addrs)
			case isNotFound(lookupErr):
				cleanSet[zone] = true
			default:
				inconclusiveSet[zone] = true
			}
		}
	}

	duration := time.Since(start)

	listedOn := sortedKeysExcept(listedSet, nil)
	clean := sortedKeysExcept(cleanSet, listedSet)
	inconclusive := sortedKeysExcept(inconclusiveSet, mergeSets(listedSet, cleanSet))

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

	if len(returnCodes) > 0 {
		result.Output["return_codes"] = returnCodes
	}

	if cfg.Nameserver != "" {
		result.Output["nameserver"] = cfg.Nameserver
	}

	return result, nil
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

// resolveTargetIPs returns the IPv4 addresses for the target. When the target
// is already an IPv4 literal it is returned as-is.
func resolveTargetIPs(ctx context.Context, lookuper hostLookuper, target string) ([]string, error) {
	if ip := net.ParseIP(target); ip != nil {
		if ip.To4() == nil {
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
		if ip != nil && ip.To4() != nil {
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
