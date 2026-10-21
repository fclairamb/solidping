package emailpreview

// Preview fixtures of the alert mail: escalation, paging-nudge, test-email,
// region-*, usage-cap-reached and custom-domain-demoted. Each one is built by
// the production helper of its sender, so a renamed key or a value that
// changes shape shows up here too.

import (
	"net"
	"time"

	"github.com/fclairamb/solidping/server/internal/customdomain"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/domainverify"
	"github.com/fclairamb/solidping/server/internal/handlers/system"
	"github.com/fclairamb/solidping/server/internal/i18n"
	"github.com/fclairamb/solidping/server/internal/jobs/jobtypes"
	"github.com/fclairamb/solidping/server/internal/regionsweep"
)

const fixtureRegionName = "Lauterbourg (lauterbourg)"

// fixtureAlertTime is a fixed instant of the current year (so the preview
// never shows a year), hour and minute of day given in UTC.
func fixtureAlertTime(month time.Month, day, hour, minute int) time.Time {
	return time.Date(time.Now().Year(), month, day, hour, minute, 0, 0, time.UTC)
}

// escalationFixture is the fixture for the escalation-policy email
// (job_escalation_step.go's sendEscalationEmail): the incident facts, the
// recipient's own ack link and the check it is about.
func escalationFixture(_ i18n.Locale) map[string]any {
	view := jobtypes.EscalationEmail{
		CheckName:      fixtureCheckName,
		CheckURL:       fixtureCheckURL,
		CheckTarget:    "https://api.acme.com/health",
		IncidentNumber: fixtureIncidentNumber,
		IncidentURL:    fixtureDashboardURL + "/orgs/acme/incidents/" + fixtureIncidentUID,
		StartedAt:      fixtureAlertTime(time.July, 5, 10, 0),
		FailureCount:   3,
		FailureReason:  "HTTP 503 Service Unavailable (expected 200)",
		AckURL: "https://solidping.example/api/v1/orgs/acme/incidents/" +
			fixtureIncidentUID + "/ack?token=preview-token",
		AckExpiresAt: fixtureAckExpiresAt(),
		DashboardURL: fixtureDashboardURL,
		DocsURL:      "https://solidping.example/docs",
	}

	return view.Data()
}

// testEmailFixture covers the SMTP test send (system.Service.TestEmail); the
// notification-route test (usernotifications) fills the same template with
// its own sentences.
func testEmailFixture(loc i18n.Locale) map[string]any {
	return system.TestEmailData(loc)
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
	data := regionsweep.OfflineEmailData(fixtureRegionName, fixtureAlertTime(time.September, 24, 13, 41), 2, 3,
		[]regionsweep.NoticeCheck{
			{Name: "API", URL: fixtureDashboardURL + "/orgs/acme/checks/api"},
			{Name: "Website", URL: fixtureDashboardURL + "/orgs/acme/checks/website"},
		},
		fixtureDashboardURL+"/orgs/acme/checks")
	data[keyOrgName] = fixtureOrgName

	return data
}

// regionRecoveredFixture covers the matching recovery notice.
func regionRecoveredFixture(_ i18n.Locale) map[string]any {
	data := regionsweep.RecoveredEmailData(fixtureRegionName,
		fixtureAlertTime(time.September, 24, 13, 41), fixtureAlertTime(time.September, 24, 21, 33), false,
		fixtureDashboardURL+"/orgs/acme/checks")
	data[keyOrgName] = fixtureOrgName

	return data
}

// usageCapReachedFixture covers the once-a-month notice that a paging channel
// ran out of its plan allowance (spec 2026-10-17-03). Operator mail.
func usageCapReachedFixture(_ i18n.Locale) map[string]any {
	data := jobtypes.UsageCapEmailData(models.UsageCounterKindSMS, 20, fixtureAlertTime(time.November, 1, 0, 0),
		fixtureDashboardURL+"/orgs/acme/organization/usage", "https://billing.acme.com/acme/plans")
	data[keyOrgName] = fixtureOrgName

	return data
}

// customDomainDemotedFixture covers the status-page custom-domain demotion
// alert (customdomain.mailDemoted). The diagnostic is what the verifier
// really records: Diagnosis.String() of a lookup that found no such host.
func customDomainDemotedFixture(_ i18n.Locale) map[string]any {
	diagnosis := domainverify.Diagnosis{
		Domain:   "status.acme.com",
		Mode:     domainverify.ModeShared,
		Expected: "status.solidping.example",
		Err:      &net.DNSError{Err: "no such host", Name: "status.acme.com", IsNotFound: true},
	}

	data := customdomain.DemotedEmailData(fixtureStatusPageName, diagnosis.Domain, diagnosis.String(),
		fixtureDashboardURL+"/orgs/acme/status-pages/3f1c9a2e-77b1-4f0a-9a1e-6c2f0b8d4e51")
	data[keyOrgName] = fixtureOrgName

	return data
}
