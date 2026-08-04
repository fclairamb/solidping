package auth

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/analytics/signupguard"
)

// TestEverySignupPathGoesThroughTheChokepoint is a source-level regression
// guard for spec 2026-08-02-08.
//
// The user_signed_up product event under-counts silently the moment a new
// account-creation path calls db.CreateUser directly instead of
// createUserAndCapture — and it under-counts invisibly, because nothing fails.
// That is exactly how the first cut of this feature ended up firing only on the
// email/password confirmation path while every SSO provider (GitHub, GitLab,
// Google, Microsoft, Discord, Slack, OIDC, SAML, LDAP) and invite acceptance
// created accounts silently.
//
// So: no non-test file in this package may call CreateUser except the
// chokepoint itself. The scan is wrapping- and rename-proof; see
// internal/analytics/signupguard.
func TestEverySignupPathGoesThroughTheChokepoint(t *testing.T) {
	t.Parallel()

	offenders := signupguard.ScanDir(t, ".", "signup_analytics.go")

	require.Emptyf(t, offenders,
		"these account-creation sites bypass createUserAndCapture, so user_signed_up "+
			"will silently under-count for those signup paths: %v", offenders)
}

// TestSignupMethodLabelsAreNonIdentifying pins the label set. Every value must
// be a provider family name — never a tenant, issuer URL or email domain.
func TestSignupMethodLabelsAreNonIdentifying(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	all := []string{
		signupMethodPassword, signupMethodInvite, signupMethodGoogle, signupMethodGitHub,
		signupMethodGitLab, signupMethodMicrosoft, signupMethodDiscord, signupMethodSlack,
		signupMethodOIDC, signupMethodSAML, signupMethodLDAP,
	}

	seen := make(map[string]bool, len(all))
	for _, label := range all {
		r.NotEmpty(label)
		r.NotContains(label, "@", "a signup method label must never carry an email domain")
		r.NotContains(label, "://", "a signup method label must never carry a URL")
		r.False(seen[label], "duplicate signup method label %q", label)
		seen[label] = true
	}

	r.Len(seen, 11)

	// The Slack integration package builds its event from the exported alias;
	// both must stay the same string or the metric silently splits in two.
	r.Equal(signupMethodSlack, SignupMethodSlack)
}
