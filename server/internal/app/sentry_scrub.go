package app

import (
	"strings"

	"github.com/getsentry/sentry-go"

	"github.com/fclairamb/solidping/server/internal/scrub"
)

const sentryFiltered = scrub.Filtered

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

// redactCredentialQuery and redactCredentialURL delegate to the rules shared
// with the PostHog error-tracking sink.
func redactCredentialQuery(rawQuery string) string { return scrub.Query(rawQuery) }

func redactCredentialURL(raw string) string { return scrub.URL(raw) }
