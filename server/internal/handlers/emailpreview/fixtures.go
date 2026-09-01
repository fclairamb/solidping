package emailpreview

import "sort"

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
	// fixtureCheckURL is a UID-shaped check dashboard link — links are built
	// from the check's UID, not its slug, so a check rename never breaks the
	// link and the URL never falls back to unlinked text.
	fixtureCheckURL       = "https://solidping.example/dash0/orgs/acme/checks/3f7a9c2e-6b1d-4e0a-9c8f-1a2b3c4d5e6f"
	fixtureStatusPageName = "Acme Status"
	// fixturePersonName is the one human the previews name, so an org member
	// reads as the same person whether they invited someone or acked an alert.
	fixturePersonName = "Alice Admin"

	keyOrgName      = "OrgName"
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
	"incident-acknowledged.html":       acknowledgedIncidentFixture,
	"incident-unacknowledged.html":     unacknowledgedIncidentFixture,
	"incident-comment.html":            commentIncidentFixture,
	"custom-domain-demoted.html":       customDomainDemotedFixture,
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
		"CheckURL":             fixtureCheckURL,
		"StartedAt":            "2026-07-05 10:00:00 UTC",
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
	fixture := incidentFixture()
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
	fixture["BurnProjectedExhaustion"] = "2026-07-05 14:30:00 UTC"
	fixture["BurnTarget"] = "99.9%"

	return fixture
}

// resolvedBurnIncidentFixture is the cleared-alert half: no ack (there is
// nothing left to acknowledge) and a rate back under the threshold.
func resolvedBurnIncidentFixture() map[string]any {
	fixture := burnIncidentFixture()
	fixture["AckURL"] = ""
	fixture["ResolvedAt"] = "2026-07-05 10:15:00 UTC"
	fixture["Duration"] = "15m"
	fixture["BurnRate"] = "1.2x"

	return fixture
}

// resolvedIncidentFixture extends incidentFixture with the resolved-only
// fields (ResolvedAt, Duration) and has no AckURL — resolved has nothing to
// ack.
func resolvedIncidentFixture() map[string]any {
	fx := incidentFixture()
	fx["AckURL"] = ""
	fx["ResolvedAt"] = "2026-07-05 10:15:00 UTC"
	fx["Duration"] = "15m"

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
		"CheckURL":       fixtureCheckURL,
		"IncidentNumber": fixtureIncidentNumber,
		"IncidentURL":    incidentURL,
		"StartedAt":      "2026-07-05 10:00:00 UTC",
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
		keySubject:   "Confirm your subscription to " + fixtureStatusPageName,
		"PageName":   fixtureStatusPageName,
		"ConfirmURL": "https://solidping.example/api/v1/public/status-subscribers/confirm?token=preview-token",
	}
}

// statusSubscriberUpdateFixture covers the incident-opened/update/resolved
// fan-out email (statussubscribers.Notifier.sendOne) — one template for all
// three kinds, distinguished by Label/Title/Subject.
func statusSubscriberUpdateFixture() map[string]any {
	return map[string]any{
		keySubject:     "[" + fixtureStatusPageName + "] New incident: Elevated error rates",
		"Label":        "New incident",
		"Title":        "Elevated error rates",
		"BodyMarkdown": "We are investigating elevated error rates on the API.",
		"LinkURL":      "https://solidping.example/status0/acme/acme-status",
		"PageName":     fixtureStatusPageName,
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
		"InviterName": fixturePersonName,
		"InviteURL":   "https://solidping.example/dash0/invitations/preview-token",
		// Dynamic on purpose: the template used to hardcode "7 days" here
		// regardless of the actual invite TTL. This fixture value is
		// deliberately NOT "7 days" so the preview harness would catch a
		// regression back to the hardcoded string.
		"ExpiresIn": "24 hours",
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
		"LongestIncident": "42m",
		"AverageIncident": "21m 40s",
		"TotalDowntime":   "1h 5m",

		// Period-over-period trend. Present in the fixture so the preview
		// exercises the delta spans rather than only their absence.
		"PreviousPeriodLabel":     "June 2026",
		"HasPreviousData":         true,
		"PreviousAvailabilityPct": "99.870",
		"PreviousIncidentCount":   5,
		"PreviousAvgResponseTime": "158 ms",
		"ShowAvailabilityDelta":   true,
		"AvailabilityDeltaText":   "+0.080 pts",
		"AvailabilityDeltaColor":  colorReportGood,
		"ShowIncidentDelta":       true,
		"IncidentDeltaText":       "-2",
		"IncidentDeltaColor":      colorReportGood,
		"ShowResponseDelta":       true,
		"ResponseDeltaText":       "+8.4%",
		"ResponseDeltaColor":      "#b91c1c",

		// Response-time block.
		"HasLatency":      true,
		"AvgResponseTime": "171 ms",
		"MinResponseTime": "42 ms",
		"MaxResponseTime": "3.20 s",
		"SlowLine":        "4 samples and 2 peaks above 1 s",
		"SlowNote":        "A peak is a rolled-up period whose slowest sample exceeded 1 s.",
		"LatencyNote":     "Response times include failed samples.",

		"DayStripLabel": "Daily availability, 1 Jul – 31 Jul (UTC)",
		"Checks": []map[string]any{
			{
				keyName: fixtureCheckName, keyHasData: true, keyAvailability: "99.980",
				"Days": []map[string]any{
					{keyColor: colorReportGood, keySpan: 20, keyWide: true},
					{keyColor: "#b45309", keySpan: 1},
					{keyColor: colorReportGood, keySpan: 10, keyWide: true},
				},
			},
			{
				keyName: "Marketing site", keyHasData: false, keyAvailability: "",
				"Days": []map[string]any{{keyColor: "#d1d5db", keySpan: 31, keyWide: true}},
			},
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

// acknowledgedIncidentFixture is the "somebody took it" half of the incident
// lifecycle: the incident is still open, so the ack fields are set but there is
// no AckURL left to click.
func acknowledgedIncidentFixture() map[string]any {
	fixture := incidentFixture()
	fixture["AckURL"] = ""
	fixture["AckActor"] = fixturePersonName
	fixture["AckVia"] = "from the dashboard"
	fixture["AcknowledgedAt"] = "2026-07-05 10:04:00 UTC"

	return fixture
}

// unacknowledgedIncidentFixture is the retraction: the incident is open and
// unowned again, so — unlike the acknowledged fixture — the ack magic link IS
// present. Taking the incident from this very email is the call to action.
func unacknowledgedIncidentFixture() map[string]any {
	fixture := incidentFixture()
	fixture["AckActor"] = fixturePersonName
	fixture["AckVia"] = "from the dashboard"

	return fixture
}

// commentIncidentFixture carries a deliberately multi-line comment body so the
// preview exercises the quote block's wrapping rather than a single short line.
func commentIncidentFixture() map[string]any {
	fixture := incidentFixture()
	fixture["CommentAuthor"] = "Bob Builder"
	fixture["CommentSource"] = "from Slack"
	fixture["CommentText"] = "Upstream provider confirms a regional outage.\n" +
		"Failing over to the secondary region now — next update in 15 minutes."

	return fixture
}

// customDomainDemotedFixture covers the status-page custom-domain demotion
// alert (customdomain.mailDemoted). Operator mail: no unsubscribe, no ack.
func customDomainDemotedFixture() map[string]any {
	return map[string]any{
		keyOrgName:       fixtureOrgName,
		"StatusPageName": fixtureStatusPageName,
		"Domain":         "status.acme.com",
		"Diagnostic":     "CNAME lookup for status.acme.com returned NXDOMAIN",
		"SettingsURL": "https://solidping.example/dash0/orgs/acme/status-pages/" +
			"3f1c9a2e-77b1-4f0a-9a1e-6c2f0b8d4e51",
	}
}
