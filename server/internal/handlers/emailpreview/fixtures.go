package emailpreview

// fixtureFor returns the preview fixture data for a shipped template, and
// whether the name was recognized. Kept as one table so the supported list
// is discoverable by reading the source rather than probing at runtime — and
// so a template that ships without a fixture here is a build-time-obvious
// omission (caught by TestAllShippedTemplatesHaveFixtures in the server
// package, which enumerates the embedded templates/ directory).
//
//nolint:mnd // fixture data literals, not magic numbers
func fixtureFor(templateName string) (map[string]any, bool) {
	switch templateName {
	case "incident-created.html", "incident-escalated.html", "incident-reopened.html":
		return incidentFixture(), true
	case "incident-resolved.html":
		return resolvedIncidentFixture(), true
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
			"OrgName":     "Acme Corp",
			"Role":        "admin",
			"InviterName": "Alice Admin",
			"InviteURL":   "https://solidping.example/dash0/invitations/preview-token",
		}, true
	case "welcome.html":
		return map[string]any{
			"DashboardURL": "https://solidping.example/dash0",
		}, true
	case "password-changed.html":
		return map[string]any{
			"ChangedAt": "Sunday, July 5, 2026 at 10:00 UTC",
		}, true
	case "membership_request_new.html":
		return map[string]any{
			"OrgName":        "Acme Corp",
			"RequesterName":  "Bob Builder",
			"RequesterEmail": "bob@example.com",
			"Message":        "I'd like to help monitor our new services.",
			"RequestsURL":    "https://solidping.example/dash0/orgs/acme/organization/membership-requests",
		}, true
	case "membership_request_decision.html":
		return map[string]any{
			"OrgName":      "Acme Corp",
			"Decision":     "approved",
			"Role":         "viewer",
			"DashboardURL": "https://solidping.example/dash0/orgs/acme",
		}, true
	default:
		return nil, false
	}
}

// incidentFixture is shared by created/escalated/reopened — they read the
// same field set (ResolvedAt/Duration are simply ignored by templates that
// don't reference them).
func incidentFixture() map[string]any {
	return map[string]any{
		"CheckName":            "Production API",
		"CheckType":            "http",
		"CheckURL":             "https://solidping.example/dash0/orgs/acme/checks/prod-api",
		"StartedAt":            "2026-07-05 10:00:00",
		"IncidentUID":          "8f14e45f-ceea-467e-adde-3f4edd1a5b22",
		"IncidentURL":          "https://solidping.example/dash0/orgs/acme/incidents/8f14e45f-ceea-467e-adde-3f4edd1a5b22",
		"AckURL":               "https://solidping.example/api/v1/orgs/acme/incidents/8f14e45f-ceea-467e-adde-3f4edd1a5b22/ack?token=preview-token",
		"FailureCount":         3,
		"RelapseCount":         1,
		"DashboardURL":         "https://solidping.example/dash0",
		"DocsURL":              "https://solidping.example/docs",
		"UnsubscribeURL":       "https://solidping.example/unsubscribe?token=preview-unsub-token",
		"UnsubscribeCheckName": "Production API",
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
