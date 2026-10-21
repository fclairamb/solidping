package emailpreview

// Preview fixtures of the incident-* templates: the incident lifecycle, SLO
// burn and degraded alerts.

import (
	"github.com/fclairamb/solidping/server/internal/i18n"
)

// incidentFixture is shared by created/escalated/reopened — they read the
// same field set (ResolvedAt/Duration are simply ignored by templates that
// don't reference them).
func incidentFixture(_ i18n.Locale) map[string]any {
	incidentURL := fixtureDashboardURL + "/orgs/acme/incidents/" + fixtureIncidentUID
	ackURL := "https://solidping.example/api/v1/orgs/acme/incidents/" +
		fixtureIncidentUID + "/ack?token=preview-token"

	return map[string]any{
		"CheckName":            fixtureCheckName,
		"CheckType":            "http",
		"CheckURL":             fixtureCheckURL,
		"StartedAt":            "Jul 5, 10:00 UTC",
		"IncidentUID":          fixtureIncidentUID,
		"IncidentNumber":       fixtureIncidentNumber,
		"IncidentURL":          incidentURL,
		"AckURL":               ackURL,
		"AckExpiresAt":         fixtureAckExpiresAt(),
		"FailureCount":         3,
		"RelapseCount":         1,
		"FailureReason":        "HTTP 503 Service Unavailable (expected 200)",
		"CheckTarget":          "https://api.acme.com/health",
		keyDashboardURL:        fixtureDashboardURL,
		"DocsURL":              "https://solidping.example/docs",
		"UnsubscribeURL":       "https://solidping.example/unsubscribe?token=preview-unsub-token",
		"UnsubscribeCheckName": fixtureCheckName,
	}
}

// burnIncidentFixture is the SLO burn-rate alert view-model. It extends the
// incident fixture rather than replacing it because a burn incident IS an
// incident — same ack link, same deep links — with the three deciding numbers
// added: burn rate, budget remaining, projected exhaustion.
func burnIncidentFixture(loc i18n.Locale) map[string]any {
	fixture := incidentFixture(loc)
	fixture["SLOName"] = "Acme API availability"
	fixture["BurnPolicyLabel"] = "Fast burn"
	fixture["BurnSeverity"] = "critical"
	fixture["BurnRate"] = "31.0x"
	fixture["BurnShortRate"] = "44.5x"
	fixture["BurnPeakRate"] = "52.0x"
	fixture["BurnThreshold"] = "14.4x"
	fixture["BurnLongWindow"] = "1h"
	fixture["BurnShortWindow"] = "5m"
	fixture["BurnBudgetRemaining"] = "1h30m"
	fixture["BurnProjectedExhaustion"] = "Jul 5, 14:30 UTC"
	fixture["BurnTarget"] = "99.9%"

	return fixture
}

// degradedIncidentFixture is the degraded-detection notice (spec 2026-09-22-03),
// carrying the motivating episode's own numbers: 7 failures in 60 probes,
// 93.8% availability, and a target that is up at this very moment.
func degradedIncidentFixture(loc i18n.Locale) map[string]any {
	fixture := incidentFixture(loc)
	fixture["DegradedReason"] = "7 failures in the last 60 probes (93.8% available)"
	fixture["DegradedStatus"] = "Currently up."
	fixture["DegradedWindow"] = "14:35 – 15:28 UTC"
	fixture["DegradedWindowURL"] = "https://solidping.example/d/orgs/acme/checks/acme-api" +
		"?graphFrom=1790778900000&graphTo=1790782080000"
	fixture["DegradedFailureLine"] = "7 failures in the last 60 probes (93.8% available) (rule: 5 of 60 probes)"
	fixture["DegradedSlowLine"] = "3 of the last 6 probes were slower than 1000ms (rule: 3 of 6 probes over 1000ms)"

	return fixture
}

// resolvedDegradedIncidentFixture is the cleared half: the pattern stopped, so
// there is nothing left to acknowledge.
func resolvedDegradedIncidentFixture(loc i18n.Locale) map[string]any {
	fixture := degradedIncidentFixture(loc)
	fixture["AckURL"] = ""
	fixture["ResolvedAt"] = fixtureResolvedAt
	fixture["Duration"] = "53m"

	return fixture
}

// resolvedBurnIncidentFixture is the cleared-alert half: no ack (there is
// nothing left to acknowledge) and a rate back under the threshold.
func resolvedBurnIncidentFixture(loc i18n.Locale) map[string]any {
	fixture := burnIncidentFixture(loc)
	fixture["AckURL"] = ""
	fixture["ResolvedAt"] = fixtureResolvedAt
	fixture["Duration"] = "15m"
	fixture["BurnRate"] = "1.2x"

	return fixture
}

// resolvedIncidentFixture extends incidentFixture with the resolved-only
// fields (ResolvedAt, Duration) and has no AckURL — resolved has nothing to
// ack.
func resolvedIncidentFixture(loc i18n.Locale) map[string]any {
	fx := incidentFixture(loc)
	fx["AckURL"] = ""
	fx["ResolvedAt"] = fixtureResolvedAt
	fx["Duration"] = "15m"

	return fx
}

// acknowledgedIncidentFixture is the "somebody took it" half of the incident
// lifecycle: the incident is still open, so the ack fields are set but there is
// no AckURL left to click.
func acknowledgedIncidentFixture(loc i18n.Locale) map[string]any {
	fixture := incidentFixture(loc)
	fixture["AckURL"] = ""
	fixture["AckActor"] = fixturePersonName
	fixture["AckVia"] = "from the dashboard"
	fixture["AcknowledgedAt"] = "Jul 5, 10:04 UTC"

	return fixture
}

// unacknowledgedIncidentFixture is the retraction: the incident is open and
// unowned again, so — unlike the acknowledged fixture — the ack magic link IS
// present. Taking the incident from this very email is the call to action.
func unacknowledgedIncidentFixture(loc i18n.Locale) map[string]any {
	fixture := incidentFixture(loc)
	fixture["AckActor"] = fixturePersonName
	fixture["AckVia"] = "from the dashboard"

	return fixture
}

// reminderIncidentFixture is the third reminder of an incident open for
// 1 h 15 min and still unacknowledged (spec 2026-10-12-16).
func reminderIncidentFixture(loc i18n.Locale) map[string]any {
	fixture := incidentFixture(loc)
	fixture["ReminderText"] = "Still down after 1 h 15 min, not acknowledged (reminder 3)"

	return fixture
}

// commentIncidentFixture carries a deliberately multi-line comment body so the
// preview exercises the quote block's wrapping rather than a single short line.
func commentIncidentFixture(loc i18n.Locale) map[string]any {
	fixture := incidentFixture(loc)
	fixture["CommentAuthor"] = "Bob Builder"
	fixture["CommentSource"] = "from Slack"
	fixture["CommentText"] = "Upstream provider confirms a regional outage.\n" +
		"Failing over to the secondary region now — next update in 15 minutes."

	return fixture
}
