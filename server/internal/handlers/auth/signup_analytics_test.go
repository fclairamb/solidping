package auth

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
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
// chokepoint itself.
func TestEverySignupPathGoesThroughTheChokepoint(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	entries, err := os.ReadDir(".")
	r.NoError(err)

	var offenders []string

	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") {
			continue
		}

		if strings.HasSuffix(name, "_test.go") || name == "signup_analytics.go" {
			continue
		}

		content, readErr := os.ReadFile(filepath.Clean(name))
		r.NoError(readErr)

		for i, line := range strings.Split(string(content), "\n") {
			// Match the DB call, not CreateUserProvider / CreateUserToken /
			// CreateUserPasskey, none of which create an account.
			if strings.Contains(line, ".CreateUser(ctx") {
				offenders = append(offenders, name+":"+itoa(i+1)+" "+strings.TrimSpace(line))
			}
		}
	}

	r.Emptyf(offenders,
		"these account-creation sites bypass createUserAndCapture, so user_signed_up "+
			"will silently under-count for those signup paths: %v", offenders)
}

// itoa avoids pulling strconv in for a single call site.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}

	digits := ""
	for n > 0 {
		digits = string(rune('0'+n%10)) + digits
		n /= 10
	}

	return digits
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
}
