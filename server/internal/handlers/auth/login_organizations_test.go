package auth

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

// slugsOf projects a login response's organization list down to slugs.
func slugsOf(summaries []OrganizationSummary) []string {
	slugs := make([]string, 0, len(summaries))
	for _, summary := range summaries {
		slugs = append(slugs, summary.Slug)
	}

	return slugs
}

// TestCompleteLoginAfter2FAReturnsOrganizations pins the login response SHAPE
// for the 2FA path.
//
// Every other completed login hands the caller its full organization list —
// completeLogin gets it from resolveOrgPreference (password, LDAP, OAuth,
// passkey), and the invitation accept re-reads it. 2FA returned none: the
// summaries resolveOrgPreference computed before the challenge were dropped
// with everything else the temp token does not carry.
//
// That is not cosmetic. The dashboard picks which org a fresh session may land
// on from this list, so an empty one reads as "this user belongs to no
// organization at all" — a TOTP user was bounced through the no-organization
// screen until a follow-up /auth/me landed and corrected it.
func TestCompleteLoginAfter2FAReturnsOrganizations(t *testing.T) {
	t.Parallel()

	fixture, ctx := newLoginAuditFixture(t)
	user := fixture.user(ctx, t, "totp-orgs@unknown.example")

	// A SECOND membership, so the assertion proves the response carries the
	// user's whole list rather than merely echoing the org being signed into —
	// which a response built from `orgSlug` alone would also satisfy.
	second := models.NewOrganization("acmesecond", "Acme Second")
	require.NoError(t, fixture.db.CreateOrganization(ctx, second))
	require.NoError(t, fixture.db.CreateOrganizationMember(
		ctx, models.NewOrganizationMember(second.UID, user.UID, models.MemberRoleUser)))

	want := []string{fixture.org.Slug, second.Slug}

	t.Run("signing into a resolved org", func(t *testing.T) {
		t.Parallel()

		resp, err := fixture.svc.completeLoginAfter2FA(ctx, user, fixture.org.Slug,
			string(models.MemberRoleAdmin),
			withSecondFactor(AuthMethodPassword, SecondFactorTOTP), Context{})
		require.NoError(t, err)
		require.ElementsMatch(t, want, slugsOf(resp.Organizations))
	})

	t.Run("recovery code, no org on the temp token", func(t *testing.T) {
		t.Parallel()

		// The no-org branch mints an access token with an empty org claim. It
		// must still describe the memberships, otherwise the client has
		// nothing to offer the user but the org-less screen.
		resp, err := fixture.svc.completeLoginAfter2FA(ctx, user, "",
			string(models.MemberRoleAdmin),
			withSecondFactor(AuthMethodPassword, SecondFactorRecoveryCode), Context{})
		require.NoError(t, err)
		require.ElementsMatch(t, want, slugsOf(resp.Organizations))
	})
}
