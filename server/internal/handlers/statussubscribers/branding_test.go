package statussubscribers_test

import (
	"regexp"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/email"
	"github.com/fclairamb/solidping/server/internal/handlers/statuspageassets"
	"github.com/fclairamb/solidping/server/internal/handlers/statussubscribers"
	"github.com/fclairamb/solidping/server/internal/handlers/statusupdates"
)

const brandingBaseURL = "https://status.example.com"

func brandingFormatter(t *testing.T) email.Formatter {
	t.Helper()

	formatter, err := email.NewFormatter(email.WithBaseURL(brandingBaseURL))
	require.NoError(t, err)

	return formatter
}

// setPageBranding stores a logo file UID and the white-label flag on the page,
// so the real producer path (statusupdates → notifier) carries them.
func setPageBranding(t *testing.T, setup *subSetup, logoFileUID string, hideBranding bool) {
	t.Helper()

	branding := &models.BrandingSettings{HideBranding: hideBranding}
	if logoFileUID != "" {
		branding.LogoFileUID = &logoFileUID
	}

	settings := setup.page.Settings
	settings.Branding = branding

	require.NoError(t, setup.dbSvc.UpdateStatusPage(t.Context(), setup.page.UID,
		&models.StatusPageUpdate{Settings: &settings}))

	setup.page.Settings = settings
}

// brandedEvent builds the fan-out event the way the real producers do —
// statusupdates.Service and incidentpublications.Service both read the page's
// branding off page.Settings.
func brandedEvent(setup *subSetup) *statusupdates.SubscriberUpdateEvent {
	return &statusupdates.SubscriberUpdateEvent{
		StatusPageUID:    setup.page.UID,
		Kind:             string(models.StatusUpdateKindInfo),
		Title:            "Elevated error rates",
		BodyMarkdown:     "We are investigating.",
		PageName:         setup.page.Name,
		PageLogoURL:      statuspageassets.PublicURL(setup.page.Settings.LogoFileUID()),
		PageHideBranding: setup.page.Settings.HideBranding(),
	}
}

// TestSubscriberMailWearsStatusPageBrandingNotTheOrgs is the resolved-question
// requirement: status-subscriber mail takes its logo from the STATUS PAGE, and
// the organization behind the page must not appear at all — a subscriber opted
// into the page, not into the org.
func TestSubscriberMailWearsStatusPageBrandingNotTheOrgs(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	setup := newSubSetup(t)

	// The org has its own logo and name; neither may reach a subscriber.
	orgLogo := "/pub/assets/org-logo-uid"
	r.NoError(setup.dbSvc.UpdateOrganization(t.Context(), setup.org.UID,
		models.OrganizationUpdate{LogoURL: &orgLogo}))

	setPageBranding(t, setup, "page-logo-uid", false)
	confirmedSubscriber(t, setup, "subscriber@example.com")

	sender := &fakeSender{}
	notifier := statussubscribers.NewNotifier(
		setup.dbSvc, sender, brandingFormatter(t), brandingBaseURL, nil, nil, nil)

	notifier.NotifyStatusUpdate(t.Context(), brandedEvent(setup))

	msgs := sender.sent()
	r.Len(msgs, 1)

	html := msgs[0].HTML
	r.Contains(html, brandingBaseURL+statuspageassets.PublicPathPrefix+"page-logo-uid")
	r.Contains(html, `alt="Acme Status"`)
	// No org branding: neither the org logo nor the "<org> — sent by SolidPing"
	// footer attribution.
	r.NotContains(html, "org-logo-uid")
	r.NotContains(html, "Acme — sent by SolidPing")
	// And the SolidPing logo is not shown either — the page's own mark wins.
	r.NotContains(html, "/d/logo.png")
}

// TestSubscriberMailHonorsHideBranding: with the page's white-label opt-in set,
// the mail carries no logo and no SolidPing attribution at all. The test above
// is its positive control — the same path DOES render a logo without the flag.
func TestSubscriberMailHonorsHideBranding(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	setup := newSubSetup(t)

	setPageBranding(t, setup, "page-logo-uid", true)
	confirmedSubscriber(t, setup, "subscriber@example.com")

	sender := &fakeSender{}
	notifier := statussubscribers.NewNotifier(
		setup.dbSvc, sender, brandingFormatter(t), brandingBaseURL, nil, nil, nil)

	notifier.NotifyStatusUpdate(t.Context(), brandedEvent(setup))

	msgs := sender.sent()
	r.Len(msgs, 1)

	html := msgs[0].HTML
	r.NotContains(html, "<img")
	r.NotContains(html, "page-logo-uid")
	r.NotContains(html, "/d/logo.png")
	r.NotContains(html, "sent by SolidPing")
	// The page still identifies itself in the header.
	r.Contains(html, "Acme Status")
}

// TestSubscriberMailWithoutALogoNamesThePage — a page with no logo of its own
// and no white-label opt-in is named as text in the header, with a "sent by
// SolidPing" attribution: the reader subscribed to the page, not to SolidPing.
func TestSubscriberMailWithoutALogoNamesThePage(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	setup := newSubSetup(t)

	confirmedSubscriber(t, setup, "subscriber@example.com")

	sender := &fakeSender{}
	notifier := statussubscribers.NewNotifier(
		setup.dbSvc, sender, brandingFormatter(t), brandingBaseURL, nil, nil, nil)

	notifier.NotifyStatusUpdate(t.Context(), brandedEvent(setup))

	msgs := sender.sent()
	r.Len(msgs, 1)

	html := msgs[0].HTML
	r.Regexp(`<span class="header-logo"[^>]*>`+regexp.QuoteMeta(setup.page.Name)+`</span>`, html)
	r.Contains(html, "sent by SolidPing")
	r.NotContains(html, brandingBaseURL+"/d/logo.png")
	r.NotContains(html, "<img")
}
