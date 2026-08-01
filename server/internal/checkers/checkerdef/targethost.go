package checkerdef

import "net/url"

// ExtractTargetHost derives the "target host" a check probes from its public
// (non-secret) config, independent of check type: the config's `host` field
// when present, else the hostname parsed from `url`, else `target`; nil when
// none apply (e.g. heartbeat/email passive checks, which carry only a
// `token`).
//
// This is deliberately a flat, type-agnostic fallback chain rather than a
// per-CheckType switch: across every checker's config schema (see
// server/internal/checkers/check*/config.go), the field that names "the thing
// being probed" already converges on host/url/target — tcp/ssl/smtp/ssh/…
// use `host`, http/browser/websocket use `url`, dnsbl uses `target`. A new
// checker type gets a correct targetHost for free as long as it follows the
// same naming convention; no registry entry is required. Types with neither
// field (heartbeat, email, domain, kubernetes, sleep, …) correctly resolve to
// nil — a by-host view buckets them under "no host" rather than guessing.
//
// The computation is read-time only and not persisted: renaming a host in a
// check's config changes this value on the next read, with no migration.
func ExtractTargetHost(configMap map[string]any) *string {
	if host := stringConfigField(configMap, "host"); host != "" {
		return &host
	}

	if rawURL := stringConfigField(configMap, "url"); rawURL != "" {
		if host := hostnameFromURL(rawURL); host != "" {
			return &host
		}
	}

	if target := stringConfigField(configMap, "target"); target != "" {
		return &target
	}

	return nil
}

// stringConfigField reads a string-typed key out of a check's config map,
// returning "" when absent, nil, or a non-string value.
func stringConfigField(configMap map[string]any, key string) string {
	if configMap == nil {
		return ""
	}

	value, ok := configMap[key]
	if !ok {
		return ""
	}

	s, ok := value.(string)
	if !ok {
		return ""
	}

	return s
}

// hostnameFromURL extracts the hostname component of a URL (no port), e.g.
// "https://api.example.com:8443/health" -> "api.example.com". Returns "" if
// the value does not parse as a URL or carries no host.
func hostnameFromURL(rawURL string) string {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}

	return parsed.Hostname()
}
