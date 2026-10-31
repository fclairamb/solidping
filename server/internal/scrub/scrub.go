// Package scrub holds the credential-redaction rules shared by every error
// sink (Sentry and PostHog error tracking), so the two can never drift apart.
package scrub

import (
	"net/url"
	"regexp"
	"strings"
)

// Filtered replaces a scrubbed header or credential value.
const Filtered = "[FILTERED]"

// credentialParams are query parameter names whose value is a credential.
// Matched case-insensitively. Mirrors CREDENTIAL_QUERY_PARAMS in
// web/dash0/src/lib/analytics-redaction.ts.
//
// `code` is the federated-login handoff code (/d/auth/complete?code=…, spec
// 2026-09-25-12); access_token / refresh_token are what the legacy callback
// redirect carried, and what a pod on the previous release still sends during
// a rolling deploy.
//
//nolint:gochecknoglobals // Effectively a constant set; Go has no const maps.
var credentialParams = map[string]struct{}{
	"access_token":  {},
	"refresh_token": {},
	"token":         {},
	"code":          {},
	"state":         {},
	"temptoken":     {},
	"id_token":      {},
}

var urlPattern = regexp.MustCompile(`https?://[^\s"'<>]+`)

// Query replaces the value of every credential parameter in a raw query
// string, keeping every other parameter, their order and encoding untouched.
func Query(rawQuery string) string {
	if rawQuery == "" {
		return rawQuery
	}

	pairs := strings.Split(rawQuery, "&")
	for i, pair := range pairs {
		key, _, hasValue := strings.Cut(pair, "=")

		name, err := url.QueryUnescape(key)
		if err != nil {
			name = key
		}

		if _, credential := credentialParams[strings.ToLower(name)]; credential && hasValue {
			pairs[i] = key + "=" + Filtered
		}
	}

	return strings.Join(pairs, "&")
}

// URL applies Query to a full URL's query and to a param-shaped fragment
// (`#access_token=…`). A value that does not parse is dropped entirely rather
// than sent unexamined.
func URL(raw string) string {
	if raw == "" {
		return raw
	}

	parsed, err := url.Parse(raw)
	if err != nil {
		return Filtered
	}

	parsed.RawQuery = Query(parsed.RawQuery)

	if parsed.Fragment != "" && strings.Contains(parsed.Fragment, "=") {
		parsed.RawFragment = ""
		parsed.Fragment = Query(parsed.Fragment)
	}

	return parsed.String()
}

// Text redacts the credential params of every URL embedded in free text (an
// error message, a stack trace).
func Text(text string) string {
	return urlPattern.ReplaceAllStringFunc(text, URL)
}
