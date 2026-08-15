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

	keyOrgName      = "OrgName"
	keyDashboardURL = "DashboardURL"
)

// fixtureFor returns the preview fixture data for a shipped template, and
// whether the name was recognized. Kept as one table so the supported list
// is discoverable by reading the source rather than probing at runtime — and
// so a template that ships without a fixture here is a build-time-obvious
// omission (caught by TestPreview_AllShippedTemplatesRender in this
// package's handler_test.go, which enumerates the shipped template list).
func fixtureFor(templateName string) (map[string]any, bool) {
	switch templateName {
	case "incident-created.html", "incident-escalated.html", "incident-reopened.html":
		return incidentFixture(), true
	case "incident-resolved.html":
		return resolvedIncidentFixture(), true
	case "escalation.html":
		return escalationFixture(), true
	case "test-email.html":
		return map[string]any{
			"Subject": "SolidPing Test Email",
			"Heading": "SolidPing Test Email",
			"Body":    "This is a test email from SolidPing. If you received this, your email configuration is working correctly.",
		}, true
	case "paging-nudge.html":
		return map[string]any{
			keyOrgName:         fixtureOrgName,
			"NotificationsURL": "https://solidping.example/dash0/orgs/acme/account/notifications",
		}, true
	case "registration.html":
		return map[string]any{
			"ConfirmURL": "https://solidping.example/api/v1/auth/confirm?token=preview-token",
		}, true
	case "password-reset.html":
		return map[string]any{
			"ResetURL": "https://solidping.example/dash0/reset-password?token=preview-token",
		}, true
	case "invitation.html":
		return map[string]any{
			keyOrgName:    fixtureOrgName,
			"Role":        "admin",
			"InviterName": "Alice Admin",
			"InviteURL":   "https://solidping.example/dash0/invitations/preview-token",
		}, true
	case "welcome.html":
		return map[string]any{
			keyDashboardURL: fixtureDashboardURL,
		}, true
	case "password-changed.html":
		return map[string]any{
			"ChangedAt": "Sunday, July 5, 2026 at 10:00 UTC",
		}, true
	case "membership_request_new.html":
		return map[string]any{
			keyOrgName:       fixtureOrgName,
			"RequesterName":  "Bob Builder",
			"RequesterEmail": "bob@example.com",
			"Message":        "I'd like to help monitor our new services.",
			"RequestsURL":    "https://solidping.example/dash0/orgs/acme/organization/membership-requests",
		}, true
	case "membership_request_decision.html":
		return map[string]any{
			keyOrgName:      fixtureOrgName,
			"Decision":      "approved",
			"Role":          "viewer",
			keyDashboardURL: fixtureDashboardURL + "/orgs/acme",
		}, true
	default:
		return nil, false
	}
}

// incidentFixture is shared by created/escalated/reopened — they read the
// same field set (ResolvedAt/Duration are simply ignored by templates that
// don't reference them).
func incidentFixture() map[string]any {
	incidentURL := "https://solidping.example/dash0/orgs/acme/incidents/" + fixtureIncidentUID
	ackURL := "https://solidping.example/api/v1/orgs/acme/incidents/" +
		fixtureIncidentUID + "/ack?token=preview-token"

	return map[string]any{
		"CheckName":            "Production API",
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
		"UnsubscribeCheckName": "Production API",
	}
}

// escalationFixture is the fixture for the escalation-policy email
// (job_escalation_step.go's sendEscalationEmail) — a smaller view-model than
// the four incident-lifecycle templates: no ack/unsubscribe (it's an internal
// paging email, not a per-recipient incident notification).
func escalationFixture() map[string]any {
	incidentURL := "https://solidping.example/dash0/orgs/acme/incidents/" + fixtureIncidentUID

	return map[string]any{
		"CheckName":      "Production API",
		"CheckURL":       "https://solidping.example/dash0/orgs/acme/checks/prod-api",
		"IncidentNumber": fixtureIncidentNumber,
		"IncidentURL":    incidentURL,
		"StartedAt":      "2026-07-05 10:00:00",
		"FailureCount":   3,
		keyDashboardURL:  fixtureDashboardURL,
		"DocsURL":        "https://solidping.example/docs",
	}
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
