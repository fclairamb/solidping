package auth

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
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

	// With auth.auto_personal_org (the SaaS default) the new user gets a
	// personal org and an org-scoped session (spec 2026-10-30-04). Without it
	// (the self-hosted default) the session is org-less, for /no-org.
	for _, tc := range []struct {
		mode        string
		personalOrg bool
	}{
		{mode: config.DeploymentModeSaaS, personalOrg: true},
		{mode: config.DeploymentModeSelfHosted, personalOrg: false},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			t.Parallel()

			r := require.New(t)
			f, ctx := newLoginAuditFixture(t)

			f.svc.fullCfg.Deployment.Mode = tc.mode
			// Allow registration globally, but deliberately do NOT set the org's
			// registration.email_pattern — so autoJoinMatchingOrgs admits the new
			// user into no org, reproducing the common "brand new email" case.
			f.svc.fullCfg.Auth.RegistrationEmailPattern = ".*"

			const email = "no-org-registrant@acme.com"

			_, err := f.svc.Register(ctx, RegisterRequest{
				Name: "No Org User", Email: email, Password: "supersecret123",
			})
			r.NoError(err)

			token := extractRegistrationConfirmToken(t, ctx, f.db, email)
			r.NotEmpty(token, "precondition: the registration token must have been stored")

			resp, err := f.svc.ConfirmRegistration(ctx, token)
			r.NoError(err)

			user, err := f.db.GetUserByEmail(ctx, email)
			r.NoError(err)

			members, err := f.db.ListMembersByUser(ctx, user.UID)
			r.NoError(err)

			// The actual assertion: a usable session was minted.
			r.NotEmpty(resp.AccessToken, "confirming registration with no matching org must still mint an access token")
			r.Positive(resp.ExpiresIn)
			r.Equal(email, resp.User.Email)

			// And it must actually authenticate, exactly as the landing page's
			// first authenticated call would exercise it.
			claims, err := f.svc.ValidateToken(ctx, resp.AccessToken)
			r.NoError(err, "the minted access token must validate")

			meResp, err := f.svc.GetUserInfo(ctx, claims)
			r.NoError(err, "/auth/me must succeed with the confirm-registration access token")
			r.Equal(email, meResp.User.Email)

			if tc.personalOrg {
				r.Len(members, 1, "the new user is settled into a personal org")
				r.NotEmpty(resp.RefreshToken, "the personal org gets a regular org-scoped session")
				r.Equal(LoginActionDefault, resp.LoginAction)
				r.NotEmpty(claims.OrgSlug, "the token must carry the personal org")
				r.NotNil(meResp.Organization)

				return
			}

			r.Empty(members, "without auth.auto_personal_org the new user joins no org")
			r.Empty(resp.RefreshToken,
				"the no-org branch issues no refresh token, consistent with Login's resolvedOrg==nil branch")
			r.Equal(LoginActionNoOrg, resp.LoginAction)
			r.Empty(claims.OrgSlug, "the token must carry no org")
		})
	}
}
