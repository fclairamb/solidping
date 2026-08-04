package checkerdef

import (
	"net/url"
	"strings"
)

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
// One documented exception: checkdocker's `host` is a Docker daemon
// connection URI (`unix:///var/run/docker.sock` by default, or
// `tcp://host:port` when customized), not a bare hostname — resolveHostField
// normalizes it before it's treated as a hostname. Every other checker's
// `host` field was verified (as of this writing) to already be a bare
// hostname/IP validated alongside a separate `port` field, so this is not a
// general "host may be a URI" rule, just a targeted fix for the one type
// that doesn't follow the convention.
//
// The computation is read-time only and not persisted: renaming a host in a
// check's config changes this value on the next read, with no migration.
func ExtractTargetHost(configMap map[string]any) *string {
	if host := stringConfigField(configMap, "host"); host != "" {
		resolved := resolveHostField(host)
		if resolved != "" {
			return &resolved
		}
		// host was present but resolved to no meaningful network host (e.g. a
		// unix:// socket) — respect that rather than falling back to url/target
		// (which checkdocker's config doesn't carry anyway).
		return nil
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

// resolveHostField normalizes a config `host` field value that may itself be
// a connection URI rather than a bare hostname/IP — currently only
// checkdocker's `host`. A unix:// socket has no network host at all, so it
// resolves to "" (ExtractTargetHost turns that into nil rather than
// surfacing a filesystem path as if it were a target host); a
// tcp://host:port form resolves to just the hostname, exactly like the `url`
// branch below. A value with no "://" is already a bare hostname/IP and
// passes through unchanged.
func resolveHostField(host string) string {
	if !strings.Contains(host, "://") {
		return host
	}

	if strings.HasPrefix(host, "unix://") {
		return ""
	}

	return hostnameFromURL(host)
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
