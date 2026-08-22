package emailpreview

// Fixture constants shared across multiple templates below — pulled out
// once a literal repeats within this file (goconst). keyOrgName/keyDashboardURL
// are the map KEYS (not just the values), which repeat across several of the
// view-models below since multiple templates share field names.
const (
	fixtureOrgName        = "Acme Corp"
	fixtureDashboardURL   = "https://solidping.example/dash0"
	fixtureIncidentUID    = "8f14e45f-ceea-467e-adde-3f4edd1a5b22"
	fixtureIncidentNumber = 42
	fixtureCheckName      = "Production API"

	keyOrgName      = "OrgName"
	keyDashboardURL = "DashboardURL"
	keySubject      = "Subject"
	keyName         = "Name"
	keyHasData      = "HasData"
	keyAvailability = "AvailabilityPct"
)

// fixtureBuilders maps a shipped template name to the function that returns
// its preview fixture data. Kept as one table (a map, not a switch — the
// switch form tripped cyclop once enough templates existed) so the supported
// list is discoverable by reading the source rather than probing at runtime —
// and so a template that ships without a fixture here is a build-time-obvious
// omission (caught by TestPreview_AllShippedTemplatesRender in this
// package's handler_test.go, which enumerates the shipped template list).
//
//nolint:gochecknoglobals // read-only dispatch table, not mutable state.
var fixtureBuilders = map[string]func() map[string]any{
	"incident-created.html":            incidentFixture,
	"incident-escalated.html":          incidentFixture,
	"incident-reopened.html":           incidentFixture,
	"incident-resolved.html":           resolvedIncidentFixture,
	"incident-burn-created.html":       burnIncidentFixture,
	"incident-burn-resolved.html":      resolvedBurnIncidentFixture,
	"escalation.html":                  escalationFixture,
	"test-email.html":                  testEmailFixture,
	"paging-nudge.html":                pagingNudgeFixture,
	"status-subscriber-confirm.html":   statusSubscriberConfirmFixture,
	"status-subscriber-update.html":    statusSubscriberUpdateFixture,
	"registration.html":                registrationFixture,
	"password-reset.html":              passwordResetFixture,
	"invitation.html":                  invitationFixture,
	"welcome.html":                     welcomeFixture,
	"password-changed.html":            passwordChangedFixture,
	"membership_request_new.html":      membershipRequestNewFixture,
	"membership_request_decision.html": membershipRequestDecisionFixture,
	"uptime-report.html":               uptimeReportFixture,
}

// fixtureFor returns the preview fixture data for a shipped template, and
// whether the name was recognized.
func fixtureFor(templateName string) (map[string]any, bool) {
	builder, ok := fixtureBuilders[templateName]
	if !ok {
		return nil, false
	}

	return builder(), true
}

// incidentFixture is shared by created/escalated/reopened — they read the
// same field set (ResolvedAt/Duration are simply ignored by templates that
// don't reference them).
func incidentFixture() map[string]any {
	incidentURL := "https://solidping.example/dash0/orgs/acme/incidents/" + fixtureIncidentUID
	ackURL := "https://solidping.example/api/v1/orgs/acme/incidents/" +
		fixtureIncidentUID + "/ack?token=preview-token"

	return map[string]any{
		"CheckName":            fixtureCheckName,
		"CheckType":            "http",
		"CheckURL":             "https://solidping.example/dash0/orgs/acme/checks/prod-api",
		"StartedAt":            "2026-07-05 10:00:00",
		"IncidentUID":          fixtureIncidentUID,
		"IncidentNumber":       fixtureIncidentNumber,
		"IncidentURL":          incidentURL,
		"AckURL":               ackURL,
		"FailureCount":         3,
		"RelapseCount":         1,
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
func burnIncidentFixture() map[string]any {
	fx := incidentFixture()
	fx["SLOName"] = "Acme API availability"
	fx["BurnPolicyLabel"] = "Fast burn"
	fx["BurnSeverity"] = "critical"
	fx["BurnRate"] = "31.0x"
	fx["BurnShortRate"] = "44.5x"
	fx["BurnPeakRate"] = "52.0x"
	fx["BurnThreshold"] = "14.4x"
	fx["BurnLongWindow"] = "1h"
	fx["BurnShortWindow"] = "5m"
	fx["BurnBudgetRemaining"] = "1h30m"
	fx["BurnProjectedExhaustion"] = "2026-07-05 14:30:00 UTC"
	fx["BurnTarget"] = "99.9%"

	return fx
}

// resolvedBurnIncidentFixture is the cleared-alert half: no ack (there is
// nothing left to acknowledge) and a rate back under the threshold.
func resolvedBurnIncidentFixture() map[string]any {
	fx := burnIncidentFixture()
	fx["AckURL"] = ""
	fx["ResolvedAt"] = "2026-07-05 10:15:00"
	fx["Duration"] = "15m0s"
	fx["BurnRate"] = "1.2x"

	return fx
}

// resolvedIncidentFixture extends incidentFixture with the resolved-only
// fields (ResolvedAt, Duration) and has no AckURL — resolved has nothing to
// ack.
func resolvedIncidentFixture() map[string]any {
	fx := incidentFixture()
	fx["AckURL"] = ""
	fx["ResolvedAt"] = "2026-07-05 10:15:00"
	fx["Duration"] = "15m0s"

	return fx
}

// escalationFixture is the fixture for the escalation-policy email
// (job_escalation_step.go's sendEscalationEmail) — a smaller view-model than
// the four incident-lifecycle templates: no ack/unsubscribe (it's an internal
// paging email, not a per-recipient incident notification).
func escalationFixture() map[string]any {
	incidentURL := "https://solidping.example/dash0/orgs/acme/incidents/" + fixtureIncidentUID

	return map[string]any{
		"CheckName":      fixtureCheckName,
		"CheckURL":       "https://solidping.example/dash0/orgs/acme/checks/prod-api",
		"IncidentNumber": fixtureIncidentNumber,
		"IncidentURL":    incidentURL,
		"StartedAt":      "2026-07-05 10:00:00",
		"FailureCount":   3,
		keyDashboardURL:  fixtureDashboardURL,
		"DocsURL":        "https://solidping.example/docs",
	}
}

// testEmailFixture covers the admin and user-notification test-send emails
// (system.Service.TestEmail, usernotifications.EmailSenderAdapter.SendTestEmail),
// which share test-email.html.
func testEmailFixture() map[string]any {
	return map[string]any{
		keySubject: "SolidPing Test Email",
		"Heading":  "SolidPing Test Email",
		"Body": "This is a test email from SolidPing. " +
			"If you received this, your email configuration is working correctly.",
	}
}

// pagingNudgeFixture covers members.Service.SendPagingNudge's paging-nudge.html.
func pagingNudgeFixture() map[string]any {
	return map[string]any{
		keyOrgName:         fixtureOrgName,
		"NotificationsURL": "https://solidping.example/dash0/orgs/acme/account/notifications",
	}
}

// statusSubscriberConfirmFixture covers the double opt-in confirmation email
// (statussubscribers.Handler.sendConfirmMail).
func statusSubscriberConfirmFixture() map[string]any {
	return map[string]any{
		keySubject:   "Confirm your subscription to Acme Status",
		"PageName":   "Acme Status",
		"ConfirmURL": "https://solidping.example/api/v1/public/status-subscribers/confirm?token=preview-token",
	}
}

// statusSubscriberUpdateFixture covers the incident-opened/update/resolved
// fan-out email (statussubscribers.Notifier.sendOne) — one template for all
// three kinds, distinguished by Label/Title/Subject.
func statusSubscriberUpdateFixture() map[string]any {
	return map[string]any{
		keySubject:     "[Acme Status] New incident: Elevated error rates",
		"Label":        "New incident",
		"Title":        "Elevated error rates",
		"BodyMarkdown": "We are investigating elevated error rates on the API.",
		"LinkURL":      "https://solidping.example/status0/acme/acme-status",
		"PageName":     "Acme Status",
		"SubscriberUnsubscribeURL": "https://solidping.example/api/v1/public/status-subscribers/" +
			"unsubscribe?token=preview-token",
	}
}

func registrationFixture() map[string]any {
	return map[string]any{
		"ConfirmURL": "https://solidping.example/api/v1/auth/confirm?token=preview-token",
	}
}

func passwordResetFixture() map[string]any {
	return map[string]any{
		"ResetURL": "https://solidping.example/dash0/reset-password?token=preview-token",
	}
}

func invitationFixture() map[string]any {
	return map[string]any{
		keyOrgName:    fixtureOrgName,
		"Role":        "admin",
		"InviterName": "Alice Admin",
		"InviteURL":   "https://solidping.example/dash0/invitations/preview-token",
	}
}

func welcomeFixture() map[string]any {
	return map[string]any{
		keyDashboardURL: fixtureDashboardURL,
	}
}

func passwordChangedFixture() map[string]any {
	return map[string]any{
		"ChangedAt": "Sunday, July 5, 2026 at 10:00 UTC",
	}
}

func membershipRequestNewFixture() map[string]any {
	return map[string]any{
		keyOrgName:       fixtureOrgName,
		"RequesterName":  "Bob Builder",
		"RequesterEmail": "bob@example.com",
		"Message":        "I'd like to help monitor our new services.",
		"RequestsURL":    "https://solidping.example/dash0/orgs/acme/organization/membership-requests",
	}
}

func membershipRequestDecisionFixture() map[string]any {
	return map[string]any{
		keyOrgName:      fixtureOrgName,
		"Decision":      "approved",
		"Role":          "viewer",
		keyDashboardURL: fixtureDashboardURL + "/orgs/acme",
	}
}

// uptimeReportFixture is the fixture for the scheduled uptime-report digest
// (spec 2026-08-20-01). The keys are PascalCase because that is what the
// template reads and what uptimereport.Data marshals to — see the tag comment
// on that struct for why the two must agree.
//
// It deliberately carries a per-check row, a per-objective row and an
// unsubscribe link, so TestPreview_AllShippedTemplatesRender exercises the
// range blocks and the bulk-mail footer rather than only the header.
func uptimeReportFixture() map[string]any {
	return map[string]any{
		keyOrgName:        fixtureOrgName,
		"PeriodLabel":     "July 2026",
		"ScopeLabel":      "All checks (2)",
		"Timezone":        "Europe/Paris",
		keyHasData:        true,
		keyAvailability:   "99.950",
		"CheckCount":      2,
		"IncidentCount":   3,
		"LongestIncident": "42m 0s",
		"TotalDowntime":   "1h 5m",
		"Checks": []map[string]any{
			{keyName: fixtureCheckName, keyHasData: true, keyAvailability: "99.980"},
			{keyName: "Marketing site", keyHasData: false, keyAvailability: ""},
		},
		"SLOs": []map[string]any{
			{
				keyName:           "API availability",
				keyHasData:        true,
				"AttainmentPct":   "99.950",
				"TargetPct":       "99.900",
				"StateLabel":      "Healthy",
				"BudgetRemaining": "21m 30s",
			},
		},
		keyDashboardURL:  fixtureDashboardURL,
		"UnsubscribeURL": "https://solidping.example/unsubscribe?token=preview-unsub-token",
	}
}
