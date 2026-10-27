package emailpreview

// Preview fixtures of the account mail: registration, welcome, password-*,
// email-changed, invitation and membership_*.

import (
	"time"

	"github.com/fclairamb/solidping/server/internal/handlers/auth"
	"github.com/fclairamb/solidping/server/internal/i18n"
)

// fixtureChangedAt is when the security notices say the password or address
// changed: a time, like the one production passes, so the preview shows the
// reader's date format. This year's, so the preview never shows a year.
func fixtureChangedAt() time.Time {
	return time.Date(time.Now().Year(), time.July, 5, 10, 0, 0, 0, time.UTC)
}

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
// purpose, so the preview also shows the "or"-joined label list, joined in
// the reader's language like production does.
func passwordResetSSOFixture(loc i18n.Locale) map[string]any {
	return map[string]any{
		"ResetURL":  fixtureDashboardURL + "/reset-password?token=preview-token",
		"Providers": loc.ListOr([]string{"GitHub", "Google"}),
	}
}

func passwordChangedFixture(_ i18n.Locale) map[string]any {
	return map[string]any{
		"ChangedAt": fixtureChangedAt(),
	}
}

func emailChangedFixture(_ i18n.Locale) map[string]any {
	return map[string]any{
		"ChangedAt": fixtureChangedAt(),
		"OldEmail":  "alice@acme.com",
		"NewEmail":  "alice@acme.org",
	}
}

func deviceTokenCreatedFixture(_ i18n.Locale) map[string]any {
	return map[string]any{
		"ClientName":  "sp CLI on laptop",
		keyOrgName:    fixtureOrgName,
		"RequesterIP": "203.0.113.7",
		"CreatedAt":   fixtureChangedAt(),
		"TokensURL":   fixtureDashboardURL + "/orgs/acme/account/tokens",
	}
}

func invitationFixture(loc i18n.Locale) map[string]any {
	return map[string]any{
		keyOrgName:    fixtureOrgName,
		"Role":        "admin",
		"InviterName": fixturePersonName,
		"InviteURL":   fixtureDashboardURL + "/invitations/preview-token",
		// Dynamic on purpose: the template used to hardcode "7 days" here
		// regardless of the actual invite TTL. This fixture value is
		// deliberately NOT "7 days" so the preview harness would catch a
		// regression back to the hardcoded string.
		"ExpiresIn": auth.InviteExpiresIn(loc, "24h"),
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
