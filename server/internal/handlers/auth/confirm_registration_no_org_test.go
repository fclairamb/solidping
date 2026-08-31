package auth

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestConfirmRegistrationNoOrgMintsSession is the regression test for spec
// 2026-08-29-06: confirming a brand-new email+password registration that
// matches no org's auto-join pattern (the common case — a first-time
// sign-up) used to return a LoginResponse with no AccessToken at all. The
// frontend then persisted the literal string "undefined" as the session
// token and bounced the freshly-created user straight to the login page,
// making a successful signup look like a failure.
//
// This proves the negative directly: no org membership after confirmation
// must still yield a usable session (non-empty AccessToken, no
// RefreshToken — same as Login's resolvedOrg==nil branch — and
// LoginActionNoOrg), and that session must actually authenticate against
// GetUserInfo, exactly as /no-org's first call would.
func TestConfirmRegistrationNoOrgMintsSession(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	f, ctx := newLoginAuditFixture(t)

	// Allow registration globally, but deliberately do NOT set the org's
	// registration.email_pattern — so autoJoinMatchingOrgs admits the new
	// user into no org, reproducing the common "brand new email" case.
	f.svc.fullCfg.Auth.RegistrationEmailPattern = ".*"

	const email = "no-org-registrant@acme.com"

	_, err := f.svc.Register(ctx, RegisterRequest{
		Name: "No Org User", Email: email, Password: "supersecret123",
	})
	r.NoError(err)

	entries, err := f.db.ListStateEntries(ctx, nil, registrationKeyPrefix)
	r.NoError(err)

	var token string

	for _, entry := range entries {
		if entry.Value == nil {
			continue
		}

		if got, ok := (*entry.Value)[keyEmail].(string); ok && got == email {
			token, _ = (*entry.Value)[keyToken].(string)
		}
	}

	r.NotEmpty(token, "precondition: the registration token must have been stored")

	resp, err := f.svc.ConfirmRegistration(ctx, token)
	r.NoError(err)

	// Precondition: this test is only meaningful for the zero-org branch.
	user, err := f.db.GetUserByEmail(ctx, email)
	r.NoError(err)

	members, err := f.db.ListMembersByUser(ctx, user.UID)
	r.NoError(err)
	r.Empty(members, "precondition: the new user must have joined no org")

	// The actual assertion: a usable, org-less session was minted.
	r.NotEmpty(resp.AccessToken, "confirming registration with no matching org must still mint an access token")
	r.Empty(resp.RefreshToken,
		"the no-org branch issues no refresh token, consistent with Login's resolvedOrg==nil branch")
	r.Positive(resp.ExpiresIn)
	r.Equal(LoginActionNoOrg, resp.LoginAction)
	r.Equal(email, resp.User.Email)

	// And it must actually authenticate, exactly as /no-org's first
	// authenticated call would exercise it.
	claims, err := f.svc.ValidateToken(ctx, resp.AccessToken)
	r.NoError(err, "the minted access token must validate")
	r.Empty(claims.OrgSlug, "the token must carry no org")

	meResp, err := f.svc.GetUserInfo(ctx, claims)
	r.NoError(err, "/auth/me must succeed with the confirm-registration access token")
	r.Equal(email, meResp.User.Email)
}
