package config

import (
	"fmt"
	"net/netip"
)

// discoveryRefusedPrefixes are the IPv4 ranges an agent's discovery allow-list
// may never overlap: "this network", loopback, link-local (cloud metadata),
// multicast and reserved. The egress package's nonPublicPrefixes table cannot
// be reused: it also lists RFC 1918, which is exactly what discovery is for.
//
//nolint:gochecknoglobals // immutable lookup table
var discoveryRefusedPrefixes = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("127.0.0.0/8"),
	netip.MustParsePrefix("169.254.0.0/16"),
	netip.MustParsePrefix("224.0.0.0/4"),
	netip.MustParsePrefix("240.0.0.0/4"),
}

// DiscoveryPrefixes parses and checks SP_AGENT_DISCOVERY_CIDRS (spec
// 2026-10-22-04): discovery needs a non-empty list; every entry is an IPv4
// prefix without host bits that overlaps none of the refused ranges.
func (a *AgentConfig) DiscoveryPrefixes() ([]netip.Prefix, error) {
	if len(a.DiscoveryCIDRs) == 0 {
		return nil, fmt.Errorf("%w: the list is empty", ErrInvalidDiscoveryCIDRs)
	}

	out := make([]netip.Prefix, 0, len(a.DiscoveryCIDRs))

	for _, entry := range a.DiscoveryCIDRs {
		prefix, err := netip.ParsePrefix(entry)
		if err != nil {
			return nil, fmt.Errorf("%w: %q: %w", ErrInvalidDiscoveryCIDRs, entry, err)
		}

		if !prefix.Addr().Is4() {
			return nil, fmt.Errorf("%w: %q is not IPv4", ErrInvalidDiscoveryCIDRs, entry)
		}

		if prefix != prefix.Masked() {
			return nil, fmt.Errorf("%w: %q has host bits set (use %s)", ErrInvalidDiscoveryCIDRs, entry, prefix.Masked())
		}

		for _, refused := range discoveryRefusedPrefixes {
			if refused.Overlaps(prefix) {
				return nil, fmt.Errorf("%w: %q overlaps the refused range %s", ErrInvalidDiscoveryCIDRs, entry, refused)
			}
		}

		out = append(out, prefix)
	}

	return out, nil
}
