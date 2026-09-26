package securityheaders

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// MaxEmbedOrigins caps statuspage.allowed_embed_origins. An allowlist that
// long is a wildcard with extra steps.
const MaxEmbedOrigins = 20

// ErrInvalidEmbedOrigin is returned for an allowlist entry that is not a plain
// scheme+host origin.
var ErrInvalidEmbedOrigin = errors.New("invalid embed origin")

// ErrTooManyEmbedOrigins is returned when the allowlist exceeds
// MaxEmbedOrigins.
var ErrTooManyEmbedOrigins = errors.New("too many embed origins")

// embedOriginRE is the only shape an embed-allowlist entry may take once
// lowercased: http(s)://host[:port], where host is a DNS name (optionally with
// a single leading "*." wildcard label, which CSP supports) or an IPv4
// address. No path, query, fragment, userinfo, whitespace, quote or `;` can
// match, so an org admin cannot smuggle a second directive into the header.
var embedOriginRE = regexp.MustCompile(
	`^https?://(\*\.)?[a-z0-9](?:[a-z0-9-]*[a-z0-9])?(?:\.[a-z0-9](?:[a-z0-9-]*[a-z0-9])?)*(?::[0-9]{1,5})?$`,
)

// ValidateEmbedOrigin checks one allowlist entry and returns its normalized
// form (lowercased, a single trailing slash dropped).
func ValidateEmbedOrigin(raw string) (string, error) {
	origin := strings.ToLower(strings.TrimSpace(raw))
	origin = strings.TrimSuffix(origin, "/")

	if !embedOriginRE.MatchString(origin) {
		return "", fmt.Errorf("%w: %q must be a scheme and host such as https://intranet.acme.com "+
			"(no path, no query, and a wildcard only as the first label)", ErrInvalidEmbedOrigin, raw)
	}

	// A wildcard must leave at least a registrable-looking domain behind:
	// "https://*.com" would let every .com site frame the page.
	_, authority, _ := strings.Cut(origin, "://")
	if rest, wildcard := strings.CutPrefix(authority, "*."); wildcard {
		host, _, _ := strings.Cut(rest, ":")
		if !strings.Contains(host, ".") {
			return "", fmt.Errorf("%w: %q: a wildcard must be followed by at least two labels "+
				"(https://*.acme.com)", ErrInvalidEmbedOrigin, raw)
		}
	}

	return origin, nil
}

// NormalizeEmbedOrigins validates every entry, drops blanks and duplicates,
// and enforces MaxEmbedOrigins. It is what the org-settings write path runs,
// so a bad entry is refused with the reason rather than stored.
func NormalizeEmbedOrigins(entries []string) ([]string, error) {
	out := make([]string, 0, len(entries))

	for _, entry := range entries {
		if strings.TrimSpace(entry) == "" {
			continue
		}

		origin, err := ValidateEmbedOrigin(entry)
		if err != nil {
			return nil, err
		}

		if !containsString(out, origin) {
			out = append(out, origin)
		}
	}

	if len(out) > MaxEmbedOrigins {
		return nil, fmt.Errorf("%w: at most %d are allowed", ErrTooManyEmbedOrigins, MaxEmbedOrigins)
	}

	return out, nil
}

// ParseEmbedOrigins reads a stored allowlist (comma- or whitespace-separated)
// for the read path. It is lenient where the write path is strict: an entry
// that does not validate is dropped rather than failing the page, because the
// page must still render — just without that origin in frame-ancestors.
func ParseEmbedOrigins(raw string) []string {
	fields := strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == ' ' || r == '\n' || r == '\t' || r == '\r'
	})

	out := make([]string, 0, len(fields))

	for _, field := range fields {
		origin, err := ValidateEmbedOrigin(field)
		if err != nil || containsString(out, origin) {
			continue
		}

		out = append(out, origin)

		if len(out) == MaxEmbedOrigins {
			break
		}
	}

	return out
}

// FormatEmbedOrigins is the stored form of an allowlist: comma-separated.
func FormatEmbedOrigins(origins []string) string {
	return strings.Join(origins, ",")
}
