package emailpreview

// Preview fixtures of the alert mail: escalation, paging-nudge, test-email,
// region-*, usage-cap-reached and custom-domain-demoted.

import (
	"github.com/fclairamb/solidping/server/internal/i18n"
)

// escalationFixture is the fixture for the escalation-policy email
// (job_escalation_step.go's sendEscalationEmail) — a smaller view-model than
// the four incident-lifecycle templates: no ack/unsubscribe (it's an internal
// paging email, not a per-recipient incident notification).
func escalationFixture(_ i18n.Locale) map[string]any {
	incidentURL := fixtureDashboardURL + "/orgs/acme/incidents/" + fixtureIncidentUID

	return map[string]any{
		"CheckName":      fixtureCheckName,
		"CheckURL":       fixtureCheckURL,
		"IncidentNumber": fixtureIncidentNumber,
		"IncidentURL":    incidentURL,
		"StartedAt":      "Jul 5, 10:00 UTC",
		"FailureCount":   3,
		"FailureReason":  "HTTP 503 Service Unavailable (expected 200)",
		"CheckTarget":    "https://api.acme.com/health",
		"AckURL": "https://solidping.example/api/v1/orgs/acme/incidents/" +
			fixtureIncidentUID + "/ack?token=preview-token",
		"AckExpiresAt":  fixtureAckExpiresAt(),
		keyDashboardURL: fixtureDashboardURL,
		"DocsURL":       "https://solidping.example/docs",
	}
}

// testEmailFixture covers the admin and user-notification test-send emails
// (system.Service.TestEmail, usernotifications.EmailSenderAdapter.SendTestEmail),
// which share test-email.html.
func testEmailFixture(_ i18n.Locale) map[string]any {
	return map[string]any{
		keySubject: "SolidPing Test Email",
		"Heading":  "SolidPing Test Email",
		"Body": "This is a test email from SolidPing. " +
			"If you received this, your email configuration is working correctly.",
	}
}

// pagingNudgeFixture covers members.Service.SendPagingNudge's paging-nudge.html.
func pagingNudgeFixture(_ i18n.Locale) map[string]any {
	return map[string]any{
		keyOrgName:         fixtureOrgName,
		"NotificationsURL": fixtureDashboardURL + "/orgs/acme/account/notifications",
	}
}

// regionOfflineFixture covers the org-facing region outage notice
// (regionsweep, spec 2026-09-25-03). Operator mail: no unsubscribe, no ack.
func regionOfflineFixture(_ i18n.Locale) map[string]any {
	return map[string]any{
		keyOrgName:     fixtureOrgName,
		"RegionName":   "Lauterbourg (lauterbourg)",
		"Since":        "Sep 24, 13:41 UTC",
		"BlindCount":   2,
		"ReducedCount": 3,
		"MoreCount":    0,
		"Checks": []map[string]any{
			{"Name": "API", "URL": fixtureDashboardURL + "/orgs/acme/checks/api"},
			{"Name": "Website", "URL": fixtureDashboardURL + "/orgs/acme/checks/website"},
		},
		"ChecksURL": fixtureDashboardURL + "/orgs/acme/checks",
	}
}

// regionRecoveredFixture covers the matching recovery notice.
func regionRecoveredFixture(_ i18n.Locale) map[string]any {
	return map[string]any{
		keyOrgName:    fixtureOrgName,
		"RegionName":  "Lauterbourg (lauterbourg)",
		"Since":       "Sep 24, 13:41 UTC",
		"RecoveredAt": "Sep 24, 21:33 UTC",
		"Duration":    "7h52m",
		"Moved":       false,
		"ChecksURL":   fixtureDashboardURL + "/orgs/acme/checks",
	}
}

// usageCapReachedFixture covers the once-a-month notice that a paging channel
// ran out of its plan allowance (spec 2026-10-17-03). Operator mail.
func usageCapReachedFixture(_ i18n.Locale) map[string]any {
	return map[string]any{
		keyOrgName:   fixtureOrgName,
		"Channel":    "SMS",
		"Limit":      20,
		"UsageURL":   fixtureDashboardURL + "/orgs/acme/organization/usage",
		"UpgradeURL": "https://billing.acme.com/acme/plans",
	}
}

// customDomainDemotedFixture covers the status-page custom-domain demotion
// alert (customdomain.mailDemoted). Operator mail: no unsubscribe, no ack.
func customDomainDemotedFixture(_ i18n.Locale) map[string]any {
	return map[string]any{
		keyOrgName:       fixtureOrgName,
		"StatusPageName": fixtureStatusPageName,
		"Domain":         "status.acme.com",
		"Diagnostic":     "CNAME lookup for status.acme.com returned NXDOMAIN",
		"SettingsURL": fixtureDashboardURL + "/orgs/acme/status-pages/" +
			"3f1c9a2e-77b1-4f0a-9a1e-6c2f0b8d4e51",
	}
}
