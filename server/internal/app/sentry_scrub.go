package app

import (
	"net/url"
	"strings"

	"github.com/getsentry/sentry-go"
)

// sentryFiltered replaces a scrubbed header or credential value in an event.
const sentryFiltered = "[FILTERED]"

// sentryCredentialParams are query parameter names whose value is a
// credential. Matched case-insensitively. Mirrors CREDENTIAL_QUERY_PARAMS in
// web/dash0/src/lib/analytics-redaction.ts.
//
// `code` is the federated-login handoff code (/d/auth/complete?code=…, spec
// 2026-09-25-12); access_token / refresh_token are what the legacy callback
// redirect carried, and what a pod on the previous release still sends during
// a rolling deploy.
//
//nolint:gochecknoglobals // Effectively a constant set; Go has no const maps.
var sentryCredentialParams = map[string]struct{}{
	"access_token":  {},
	"refresh_token": {},
	"token":         {},
	"code":          {},
	"state":         {},
	"temptoken":     {},
	"id_token":      {},
}

// sentrySensitiveHeaders are headers dropped from every event, compared
// case-insensitively.
//
//nolint:gochecknoglobals // Effectively a constant set; Go has no const maps.
var sentrySensitiveHeaders = map[string]struct{}{
	"authorization": {},
	"cookie":        {},
}

// scrubSentryEvent is the BeforeSend hook: it keeps credentials carried by the
// request out of Sentry. The request's own query string and the Referer header
// both carry the URL, so both get their credential params redacted; the
// Authorization and Cookie headers are filtered whole.
func scrubSentryEvent(event *sentry.Event, _ *sentry.EventHint) *sentry.Event {
	if event == nil || event.Request == nil {
		return event
	}

	event.Request.QueryString = redactCredentialQuery(event.Request.QueryString)
	event.Request.URL = redactCredentialURL(event.Request.URL)

	for key, value := range event.Request.Headers {
		lower := strings.ToLower(key)
		if _, sensitive := sentrySensitiveHeaders[lower]; sensitive {
			event.Request.Headers[key] = sentryFiltered

			continue
		}

		if lower == "referer" {
			event.Request.Headers[key] = redactCredentialURL(value)
		}
	}

	return event
}

// redactCredentialQuery replaces the value of every credential parameter in a
// raw query string, keeping every other parameter, their order and encoding
// untouched.
func redactCredentialQuery(rawQuery string) string {
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

		if _, credential := sentryCredentialParams[strings.ToLower(name)]; credential && hasValue {
			pairs[i] = key + "=" + sentryFiltered
		}
	}

	return strings.Join(pairs, "&")
}

// redactCredentialURL applies redactCredentialQuery to a full URL's query and
// to a param-shaped fragment (`#access_token=…`). A value that does not parse
// is dropped entirely rather than sent unexamined.
func redactCredentialURL(raw string) string {
	if raw == "" {
		return raw
	}

	parsed, err := url.Parse(raw)
	if err != nil {
		return sentryFiltered
	}

	parsed.RawQuery = redactCredentialQuery(parsed.RawQuery)

	if parsed.Fragment != "" && strings.Contains(parsed.Fragment, "=") {
		parsed.RawFragment = ""
		parsed.Fragment = redactCredentialQuery(parsed.Fragment)
	}

	return parsed.String()
}
