package emailpreview

import (
	"sort"
	"time"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/i18n"
)

// Fixture constants shared across multiple templates below — pulled out
// once a literal repeats within this file (goconst). keyOrgName/keyDashboardURL
// are the map KEYS (not just the values), which repeat across several of the
// view-models below since multiple templates share field names.
const (
	fixtureOrgName        = "Acme Corp"
	fixtureDashboardURL   = "https://solidping.example" + config.DashboardBasePath
	fixtureIncidentUID    = "8f14e45f-ceea-467e-adde-3f4edd1a5b22"
	fixtureIncidentNumber = 42
	fixtureCheckName      = "Production API"
	// fixtureCheckURL is a UID-shaped check dashboard link — links are built
	// from the check's UID, not its slug, so a check rename never breaks the
	// link and the URL never falls back to unlinked text.
	fixtureCheckURL       = fixtureDashboardURL + "/orgs/acme/checks/3f7a9c2e-6b1d-4e0a-9c8f-1a2b3c4d5e6f"
	fixtureStatusPageName = "Acme Status"
	// fixturePersonName is the one human the previews name, so an org member
	// reads as the same person whether they invited someone or acked an alert.
	fixturePersonName = "Alice Admin"

	keyOrgName      = "OrgName"
	keySecurityURL  = "SecurityURL"
	keyDashboardURL = "DashboardURL"
	keySubject      = "Subject"
	keyName         = "Name"
	// keyColor / colorReportGood keep the uptime-report day strip readable
	// without repeating literals the linter counts.
	keyColor        = "Color"
	keySpan         = "Span"
	keyWide         = "Wide"
	colorReportGood = "#15803d"
	keyHasData      = "HasData"
	// fixtureResolvedAt is the one resolution timestamp every resolved-incident
	// fixture shows, so the previews of the resolved templates line up.
	fixtureResolvedAt = "Jul 5, 10:15 UTC"
	keyAvailability   = "AvailabilityPct"
)

// fixtureAckExpiresAt is when the fixtures' ack link stops working: the
// incident started Jul 5, 10:00 UTC and the link lives 7 days
// (incidentlinks.AckTokenTTL). This year's, so the preview never shows a year.
func fixtureAckExpiresAt() time.Time {
	return time.Date(time.Now().Year(), time.July, 12, 10, 0, 0, 0, time.UTC)
}

// fixtureBuilders maps a shipped template name to the function that returns
// its preview fixture data. Kept as one table (a map, not a switch — the
// switch form tripped cyclop once enough templates existed) so the supported
// list is discoverable by reading the source rather than probing at runtime —
// and so a template that ships without a fixture here is a build-time-obvious
// omission (caught by TestPreview_AllShippedTemplatesRender in this
// package's handler_test.go, which enumerates the shipped template list).
//
//nolint:gochecknoglobals // read-only dispatch table, not mutable state.
var fixtureBuilders = map[string]func(i18n.Locale) map[string]any{
	"incident-created.html":            incidentFixture,
	"incident-escalated.html":          incidentFixture,
	"incident-reopened.html":           incidentFixture,
	"incident-resolved.html":           resolvedIncidentFixture,
	"incident-burn-created.html":       burnIncidentFixture,
	"incident-burn-resolved.html":      resolvedBurnIncidentFixture,
	"incident-degraded-created.html":   degradedIncidentFixture,
	"incident-degraded-resolved.html":  resolvedDegradedIncidentFixture,
	"escalation.html":                  escalationFixture,
	"test-email.html":                  testEmailFixture,
	"paging-nudge.html":                pagingNudgeFixture,
	"status-subscriber-confirm.html":   statusSubscriberConfirmFixture,
	"status-subscriber-update.html":    statusSubscriberUpdateFixture,
	"registration.html":                registrationFixture,
	"password-reset.html":              passwordResetFixture,
	"password-reset-sso.html":          passwordResetSSOFixture,
	"invitation.html":                  invitationFixture,
	"welcome.html":                     welcomeFixture,
	"password-changed.html":            passwordChangedFixture,
	"email-changed.html":               emailChangedFixture,
	"device-token-created.html":        deviceTokenCreatedFixture,
	"passkey-added.html":               passkeyAddedFixture,
	"totp-enabled.html":                totpEnabledFixture,
	"discord-linked.html":              discordLinkedFixture,
	"membership-request-new.html":      membershipRequestNewFixture,
	"membership-request-decision.html": membershipRequestDecisionFixture,
	"uptime-report.html":               uptimeReportFixture,
	"incident-acknowledged.html":       acknowledgedIncidentFixture,
	"incident-unacknowledged.html":     unacknowledgedIncidentFixture,
	"incident-reminder.html":           reminderIncidentFixture,
	"incident-comment.html":            commentIncidentFixture,
	"custom-domain-demoted.html":       customDomainDemotedFixture,
	"region-offline.html":              regionOfflineFixture,
	"region-recovered.html":            regionRecoveredFixture,
	"usage-cap-reached.html":           usageCapReachedFixture,
}

// FixtureTemplateNames returns, sorted, every template name this package can
// build fixture data for. Exported so the index endpoint and
// TestEveryShippedTemplateHasFixture read the same table the preview route
// reads, rather than a second hand-maintained list that can drift from it.
func FixtureTemplateNames() []string {
	names := make([]string, 0, len(fixtureBuilders))
	for name := range fixtureBuilders {
		names = append(names, name)
	}

	sort.Strings(names)

	return names
}

// fixtureFor returns the preview fixture data for a shipped template in loc,
// and whether the name was recognized. A builder takes the locale because the
// production helpers it should call to produce Go-built phrases (an ack
// attribution, a reminder sentence) take one (spec 2026-10-21-01).
func fixtureFor(templateName string, loc i18n.Locale) (map[string]any, bool) {
	builder, ok := fixtureBuilders[templateName]
	if !ok {
		return nil, false
	}

	return builder(loc), true
}
