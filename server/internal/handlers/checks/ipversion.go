package checks

import (
	"strings"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
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
//     behaviour and must never start failing a write.
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
