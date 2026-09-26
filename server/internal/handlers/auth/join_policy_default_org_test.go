package auth

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/defaults"
)

// TestJoinOrgViaLoginDefaultOrgNewAccount is the admission table for the rule-6
// carve-out added by spec 2026-09-05-01: on SaaS, a brand-new account that only
// ever met the platform `default` org because /login's fallback sent it there
// must NOT open a membership request against the operator's own organization.
//
// Every one of the three conditions is flipped individually as a positive
// control, so a guard that over-matched (suppressing requests for a returning
// user, for a real org, or on a self-hosted install) fails here.
func TestJoinOrgViaLoginDefaultOrgNewAccount(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		// mode is SP_DEPLOYMENT_MODE.
		mode string
		// orgSlug is the org whose login page the callback came from.
		orgSlug string
		// newlyCreated says whether the connector minted the account in this
		// very callback.
		newlyCreated bool

		// wantRequest is whether a membership_requests row must exist after.
		wantRequest bool
	}{
		{
			// THE CASE THE SPEC EXISTS FOR — all three conditions hold.
			name:         "saas + default org + brand-new account opens no request",
			mode:         config.DeploymentModeSaaS,
			orgSlug:      defaults.Organization,
			newlyCreated: true,
			wantRequest:  false,
		},
		{
			// POSITIVE CONTROL — self-hosted: `default` is typically the one
			// real org, and a colleague's first Google sign-in SHOULD reach
			// its admins.
			name:         "self-hosted + default org + brand-new account still opens a request",
			mode:         config.DeploymentModeSelfHosted,
			orgSlug:      defaults.Organization,
			newlyCreated: true,
			wantRequest:  true,
		},
		{
			// POSITIVE CONTROL — a real org the user deliberately named in the
			// URL is a request they meant to make.
			name:         "saas + non-default org + brand-new account still opens a request",
			mode:         config.DeploymentModeSaaS,
			orgSlug:      "acme",
			newlyCreated: true,
			wantRequest:  true,
		},
		{
			// POSITIVE CONTROL — a returning account signing in on
			// /orgs/default/login is asking for that org on purpose.
			name:         "saas + default org + existing account still opens a request",
			mode:         config.DeploymentModeSaaS,
			orgSlug:      defaults.Organization,
			newlyCreated: false,
			wantRequest:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			svc, dbSvc, ctx := setupAuthTestService(t)
			svc.fullCfg.Deployment.Mode = tt.mode

			// Seeded so the org is past the zero-member bootstrap rule (2) and
			// really reaches rule 6.
			org := joinTestOrg(ctx, t, dbSvc, tt.orgSlug, true)
			user := joinTestUser(ctx, t, dbSvc, "newcomer@unknown.example")

			opts := []LoginOption{
				WithLoginMethod(signupMethodGoogle),
				newlyCreatedUserOption(tt.newlyCreated),
			}

			member, pending, err := svc.JoinOrgViaLogin(ctx, org, user, opts...)
			require.NoError(t, err)

			// Either way the user is NOT admitted and the session stays
			// org-less — the carve-out only removes the request, never grants
			// access.
			require.Nil(t, member)
			require.True(t, pending)

			_, memberErr := dbSvc.GetMemberByUserAndOrg(ctx, user.UID, org.UID)
			require.Error(t, memberErr, "a refused login must leave no organization_members row")

			request, reqErr := dbSvc.GetMembershipRequestByOrgAndUser(ctx, org.UID, user.UID)

			if tt.wantRequest {
				require.NoError(t, reqErr)
				require.NotNil(t, request)
				require.Equal(t, models.MembershipRequestStatusPending, request.Status)

				return
			}

			require.True(t, reqErr != nil || request == nil,
				"a brand-new SaaS account must not queue a join request against the platform default org")
		})
	}
}

// TestCompleteOrgLoginPendingOrgSlug pins the *naming* half of the change: the
// suppressed case must hand the dashboard a pending session with NO org to
// name, while every other pending case still names the org whose admins were
// asked.
func TestCompleteOrgLoginPendingOrgSlug(t *testing.T) {
	t.Parallel()

	t.Run("suppressed pending names no org", func(t *testing.T) {
		t.Parallel()

		svc, dbSvc, ctx := setupAuthTestService(t)
		svc.fullCfg.Deployment.Mode = config.DeploymentModeSaaS

		org := joinTestOrg(ctx, t, dbSvc, defaults.Organization, true)
		user := joinTestUser(ctx, t, dbSvc, "fresh@unknown.example")

		result, err := svc.CompleteOrgLogin(ctx, org, user,
			WithLoginMethod(signupMethodGoogle), WithNewlyCreatedUser())
		require.NoError(t, err)
		require.True(t, result.Pending)
		require.Empty(t, result.PendingOrgSlug,
			"the no-org screen must not name an org nobody asked to join")
	})

	t.Run("ordinary pending still names the org", func(t *testing.T) {
		t.Parallel()

		svc, dbSvc, ctx := setupAuthTestService(t)
		svc.fullCfg.Deployment.Mode = config.DeploymentModeSaaS

		org := joinTestOrg(ctx, t, dbSvc, "acme", true)
		user := joinTestUser(ctx, t, dbSvc, "fresh2@unknown.example")

		result, err := svc.CompleteOrgLogin(ctx, org, user,
			WithLoginMethod(signupMethodGoogle), WithNewlyCreatedUser())
		require.NoError(t, err)
		require.True(t, result.Pending)
		require.Equal(t, "acme", result.PendingOrgSlug)
	})
}
