package emailpreview

// Preview fixtures of the status-subscriber-* templates.

import (
	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/email"
	"github.com/fclairamb/solidping/server/internal/handlers/statussubscribers"
	"github.com/fclairamb/solidping/server/internal/i18n"
)

// statusSubscriberConfirmFixture covers the double opt-in confirmation email
// (statussubscribers.Handler.sendConfirmMail). Its copy is all in the
// template, so the locale only picks the catalog.
func statusSubscriberConfirmFixture(_ i18n.Locale) map[string]any {
	fixture := map[string]any{
		"PageName":   fixtureStatusPageName,
		"ConfirmURL": "https://solidping.example/api/v1/public/status-subscribers/confirm?token=preview-token",
	}
	email.ApplyStatusPageBranding(fixture, fixtureStatusPageName, nil, false)

	return fixture
}

// statusSubscriberUpdateSample is the status update the preview shows, as its
// author would write it on a page in that language (a subscriber reads a
// page's updates in the page's language).
//
//nolint:gochecknoglobals // read-only sample content per language.
var statusSubscriberUpdateSample = map[string]struct{ title, body string }{
	i18n.LangEnglish: {"Elevated error rates", "We are investigating elevated error rates on the API."},
	i18n.LangFrench:  {"Taux d'erreur élevé", "Nous analysons un taux d'erreur élevé sur l'API."},
	i18n.LangGerman:  {"Erhöhte Fehlerraten", "Wir untersuchen erhöhte Fehlerraten bei der API."},
	i18n.LangSpanish: {"Tasas de error elevadas", "Estamos investigando tasas de error elevadas en la API."},
}

// statusSubscriberUpdateFixture covers the incident-opened/update/resolved
// fan-out email (statussubscribers.Notifier.sendOne) — one template for all
// three kinds, distinguished by Label and Title. The label comes from the
// production helper, in the preview's locale.
func statusSubscriberUpdateFixture(loc i18n.Locale) map[string]any {
	sample := statusSubscriberUpdateSample[loc.Lang()]

	fixture := map[string]any{
		"Label":        statussubscribers.MailKindIncidentOpened.LabelIn(loc),
		"Title":        sample.title,
		"BodyMarkdown": sample.body,
		"LinkURL":      "https://solidping.example" + config.StatusBasePath + "/acme/acme-status",
		"PageName":     fixtureStatusPageName,
		"SubscriberUnsubscribeURL": "https://solidping.example/api/v1/public/status-subscribers/" +
			"unsubscribe?token=preview-token",
	}
	email.ApplyStatusPageBranding(fixture, fixtureStatusPageName, nil, false)

	return fixture
}
