package emailpreview

// Preview fixtures of the account mail: registration, welcome, password-*,
// email-changed, invitation and membership_*.

import (
	"github.com/fclairamb/solidping/server/internal/i18n"
)

func registrationFixture(_ i18n.Locale) map[string]any {
	return map[string]any{
		"ConfirmURL": "https://solidping.example/api/v1/auth/confirm?token=preview-token",
	}
}

func welcomeFixture(_ i18n.Locale) map[string]any {
	return map[string]any{
		keyDashboardURL: fixtureDashboardURL,
	}
}

func passwordResetFixture(_ i18n.Locale) map[string]any {
	return map[string]any{
		"ResetURL": fixtureDashboardURL + "/reset-password?token=preview-token",
	}
}

// passwordResetSSOFixture previews the reset email sent to a user with no
// password who signed up through SSO (spec 2026-09-23-02). Two providers on
// purpose, so the preview also shows the "or"-joined label list.
func passwordResetSSOFixture(_ i18n.Locale) map[string]any {
	return map[string]any{
		"ResetURL":  fixtureDashboardURL + "/reset-password?token=preview-token",
		"Providers": "GitHub or Google",
	}
}

func passwordChangedFixture(_ i18n.Locale) map[string]any {
	return map[string]any{
		"ChangedAt": "Sunday, July 5, 2026 at 10:00 UTC",
	}
}

func emailChangedFixture(_ i18n.Locale) map[string]any {
	return map[string]any{
		"ChangedAt": "Sunday, July 5, 2026 at 10:00 UTC",
		"OldEmail":  "alice@acme.com",
		"NewEmail":  "alice@acme.org",
	}
}

func invitationFixture(_ i18n.Locale) map[string]any {
	return map[string]any{
		keyOrgName:    fixtureOrgName,
		"Role":        "admin",
		"InviterName": fixturePersonName,
		"InviteURL":   fixtureDashboardURL + "/invitations/preview-token",
		// Dynamic on purpose: the template used to hardcode "7 days" here
		// regardless of the actual invite TTL. This fixture value is
		// deliberately NOT "7 days" so the preview harness would catch a
		// regression back to the hardcoded string.
		"ExpiresIn": "24 hours",
	}
}

func membershipRequestNewFixture(_ i18n.Locale) map[string]any {
	return map[string]any{
		keyOrgName:       fixtureOrgName,
		"RequesterName":  "Bob Builder",
		"RequesterEmail": "bob@example.com",
		"Message":        "I'd like to help monitor our new services.",
		"RequestsURL":    fixtureDashboardURL + "/orgs/acme/organization/requests",
	}
}

func membershipRequestDecisionFixture(_ i18n.Locale) map[string]any {
	return map[string]any{
		keyOrgName:      fixtureOrgName,
		"Decision":      "approved",
		"Role":          "viewer",
		keyDashboardURL: fixtureDashboardURL + "/orgs/acme",
	}
}
