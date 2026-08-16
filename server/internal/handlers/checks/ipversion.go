package checks

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/handlers/base"
	"github.com/fclairamb/solidping/server/internal/regions"
)

// ipVersionConfigField is the config field name reported on validation errors,
// so the dashboard can attach the message to the IP-version selector.
const ipVersionConfigField = checkerdef.IPVersionConfigKey

// validateIPVersionConfig enforces every rule that makes an `ipVersion` value
// legal, on the EFFECTIVE (post-merge, post-normalize) config. It runs on the
// create, validate and PATCH paths — PATCH matters most, since UpdateCheck never
// calls checker.Validate, so this is the only gate there.
//
// Rules:
//   - the value must be auto / ipv4 / ipv6 (or absent). A typo like "v6" is
//     rejected loudly rather than silently monitoring the wrong family.
//   - `auto` (and absence) is always legal, for every type — it is the historical
//     behavior and must never start failing a write.
//   - a pinned family requires the check's type to declare SupportsIPVersion.
//     `dns` deliberately does not: for a DNS check the option could mean either
//     "which record types to assert on" or "which transport to reach the
//     nameserver over", two different features, so it is rejected instead of
//     being silently accepted and ignored. Same for every type that dials by
//     name through a client library with no address-family seam.
//   - `ipv6` on a `dnsbl` check is rejected: DNSBL zones are queried by reversed
//     IPv4 octets, so an IPv6 DNSBL lookup is a different (unimplemented)
//     feature rather than a setting.
//   - a pinned family on a TUNNELED check is rejected. A tunneled probe is
//     resolved and dialed on the far side of the bastion — the worker never sees
//     an address — so the family is the tunnel's business and the option could
//     only ever be a lie. Rejecting is deliberate: silently ignoring it would
//     leave a user believing they monitor IPv6 when they do not.
func validateIPVersionConfig(checkType string, config map[string]any) error {
	version, err := checkerdef.IPVersionFromConfig(config)
	if err != nil {
		// checkerdef's error already reads as a field message ("must be one of
		// auto, ipv4 or ipv6, got \"v6\""); drop the sentinel prefix so the
		// ConfigError does not repeat the field name twice.
		return checkerdef.NewConfigError(
			ipVersionConfigField, strings.TrimPrefix(err.Error(), "invalid ipVersion: "),
		)
	}

	if !version.Explicit() {
		return nil
	}

	meta := checkerdef.GetCheckTypeMeta(checkerdef.CheckType(checkType))
	if meta == nil || !meta.SupportsIPVersion {
		return checkerdef.NewConfigErrorf(
			ipVersionConfigField,
			"check type %q cannot be pinned to an IP version", checkType,
		)
	}

	if checkerdef.CheckType(checkType) == checkerdef.CheckTypeDNSBL && version == checkerdef.IPVersionIPv6 {
		return checkerdef.NewConfigError(
			ipVersionConfigField,
			"dnsbl checks query IPv4 blocklists only, so they cannot be pinned to ipv6",
		)
	}

	if _, tunneled := checkerdef.TunnelCheckUIDFrom(config); tunneled {
		return checkerdef.NewConfigError(
			ipVersionConfigField,
			"a tunneled check is resolved and dialed by the SSH bastion, so its address family "+
				"cannot be pinned here — remove ipVersion, or remove the tunnel",
		)
	}

	return nil
}

// ipVersionRegionWarnings reports — as a WARNING, never a rejection — that a
// check pinned to IPv6 targets a region whose live workers currently advertise
// no IPv6 egress (spec 2026-08-15-11).
//
// Warning, not rejection, for two independent reasons, and both matter:
//
//  1. The advertised value is a hint with a lag. A region may have gained IPv6
//     since its workers last reported, and the run-time egress pre-flight is
//     the authority — refusing the write here would block a check that would
//     have run perfectly.
//  2. "unknown" is a real state. A region with no live worker, or one served
//     by an agent predating the capability report, says nothing about IPv6, and
//     must never be treated as "no".
//
// So only an explicit "no" warns, and even then the check is created and runs.
func (s *Service) ipVersionRegionWarnings(
	ctx context.Context, orgUID, checkType string, config map[string]any, checkRegions []string,
) []base.ValidationErrorField {
	version, err := checkerdef.IPVersionFromConfig(config)
	if err != nil || version != checkerdef.IPVersionIPv6 {
		return nil
	}

	meta := checkerdef.GetCheckTypeMeta(checkerdef.CheckType(checkType))
	if meta == nil || !meta.SupportsIPVersion {
		return nil
	}

	effective, err := s.regions.ResolveRegionsForCheck(ctx, checkRegions, orgUID)
	if err != nil || len(effective) == 0 {
		return nil
	}

	index, err := s.regions.CapabilityIndex(ctx, orgUID)
	if err != nil {
		return nil
	}

	var lacking []string

	for _, slug := range effective {
		def, known := index[slug]
		if !known {
			continue
		}

		if def.IPv6Capability() == regions.CapabilityNo {
			lacking = append(lacking, regionLabel(def))
		}
	}

	if len(lacking) == 0 {
		return nil
	}

	return []base.ValidationErrorField{{
		Name:    ipVersionConfigField,
		Message: ipVersionRegionWarningMessage(lacking, ipv6CapableRegions(index)),
	}}
}

// regionLabel renders a region the way the user sees it in the picker.
func regionLabel(def regions.RegionDefinition) string {
	if def.Name == "" {
		return def.Slug
	}

	return fmt.Sprintf("%s (%s)", def.Name, def.Slug)
}

// ipv6CapableRegions lists the regions that DO advertise IPv6, so the warning
// can end with something actionable rather than just bad news. Sorted for a
// stable message.
func ipv6CapableRegions(index map[string]regions.RegionDefinition) []string {
	capable := make([]string, 0, len(index))

	for slug := range index {
		def := index[slug]
		if def.IPv6Capability() == regions.CapabilityYes {
			capable = append(capable, regionLabel(def))
		}
	}

	sort.Strings(capable)

	return capable
}

// ipVersionRegionWarningMessage names the offending regions and, when one
// exists, points at a region that does advertise IPv6.
func ipVersionRegionWarningMessage(lacking, capable []string) string {
	var builder strings.Builder

	builder.WriteString("this check is pinned to IPv6, but ")

	if len(lacking) == 1 {
		builder.WriteString("region " + lacking[0] + " reports no IPv6 egress")
	} else {
		builder.WriteString("regions " + strings.Join(lacking, ", ") + " report no IPv6 egress")
	}

	builder.WriteString(" from its live workers")

	if len(capable) > 0 {
		builder.WriteString(", so runs there will fail with a worker-egress error. ")
		builder.WriteString(strings.Join(capable, ", "))

		if len(capable) == 1 {
			builder.WriteString(" advertises IPv6")
		} else {
			builder.WriteString(" advertise IPv6")
		}

		builder.WriteString(".")
	} else {
		builder.WriteString(", so runs there will fail with a worker-egress error.")
	}

	builder.WriteString(
		" The check is still saved and will still run — this is only what the region last reported.",
	)

	return builder.String()
}
