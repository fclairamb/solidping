package emailpreview

// Preview fixtures of the status-subscriber-* templates.

import (
	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/email"
	"github.com/fclairamb/solidping/server/internal/i18n"
)

// statusSubscriberConfirmFixture covers the double opt-in confirmation email
// (statussubscribers.Handler.sendConfirmMail).
func statusSubscriberConfirmFixture(_ i18n.Locale) map[string]any {
	fixture := map[string]any{
		keySubject:   "Confirm your subscription to " + fixtureStatusPageName,
		"PageName":   fixtureStatusPageName,
		"ConfirmURL": "https://solidping.example/api/v1/public/status-subscribers/confirm?token=preview-token",
	}
	email.ApplyStatusPageBranding(fixture, fixtureStatusPageName, nil, false)

	return fixture
}

// statusSubscriberUpdateFixture covers the incident-opened/update/resolved
// fan-out email (statussubscribers.Notifier.sendOne) — one template for all
// three kinds, distinguished by Label/Title/Subject.
func statusSubscriberUpdateFixture(_ i18n.Locale) map[string]any {
	fixture := map[string]any{
		keySubject:     fixtureStatusPageName + ": Elevated error rates",
		"Label":        "New incident",
		"Title":        "Elevated error rates",
		"BodyMarkdown": "We are investigating elevated error rates on the API.",
		"LinkURL":      "https://solidping.example" + config.StatusBasePath + "/acme/acme-status",
		"PageName":     fixtureStatusPageName,
		"SubscriberUnsubscribeURL": "https://solidping.example/api/v1/public/status-subscribers/" +
			"unsubscribe?token=preview-token",
	}
	email.ApplyStatusPageBranding(fixture, fixtureStatusPageName, nil, false)

	return fixture
}
