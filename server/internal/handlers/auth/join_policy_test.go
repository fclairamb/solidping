package auth

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
)

// safePattern is an auto-join regex that passes validateAutoJoinRegex.
const safePattern = `@allowed\.example$`

// unsafePattern compiles fine and *would* match the probe email below, but
// validateAutoJoinRegex rejects it (its post-@ part is wide open). The join
// policy must therefore treat it as if the org had no pattern at all.
const unsafePattern = `@.*`

// joinTestOrg creates an org plus, optionally, a seed member so the org is
// past the zero-member bootstrap rule.
func joinTestOrg(ctx context.Context, t *testing.T, dbSvc db.Service, slug string, seeded bool) *models.Organization {
	t.Helper()

	org := models.NewOrganization(slug, "")
	require.NoError(t, dbSvc.CreateOrganization(ctx, org))

	if seeded {
		seed := models.NewUser("seed-" + slug + "@allowed.example")
		require.NoError(t, dbSvc.CreateUser(ctx, seed))
		require.NoError(t, dbSvc.CreateOrganizationMember(
			ctx, models.NewOrganizationMember(org.UID, seed.UID, models.MemberRoleAdmin)))
	}

	return org
}

func joinTestUser(ctx context.Context, t *testing.T, dbSvc db.Service, email string) *models.User {
	t.Helper()

	user := models.NewUser(email)
	require.NoError(t, dbSvc.CreateUser(ctx, user))

	return user
}

// TestJoinOrgViaLogin is the table-driven proof that a federated login can no
// longer walk into an arbitrary org. Every negative case (no membership, a
// membership request instead) is paired with a positive control in the same
// table so a policy that simply refused everyone would fail too.
func TestJoinOrgViaLogin(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		// pattern is the org's registration.email_pattern ("" = unset).
		pattern string
		// email of the user completing the login.
		email string
		// seeded=false means a zero-member org (bootstrap case).
		seeded bool
		// invited, when non-empty, pre-creates an invitation for `email`
		// carrying this role.
		invited string

		wantPending bool
		wantRole    models.MemberRole
	}{
		{
			// NEGATIVE — the incident this spec exists for: an arbitrary
			// Microsoft/Google/… account initiating OAuth from an org's login
			// page must NOT become a member.
			name:        "non-matching email with no invite stays out",
			pattern:     safePattern,
			email:       "attacker@evil.example",
			seeded:      true,
			wantPending: true,
		},
		{
			// POSITIVE CONTROL for the case above — same org, same rules, an
			// address the pattern admits.
			name:     "matching email joins as user",
			pattern:  safePattern,
			email:    "alice@allowed.example",
			seeded:   true,
			wantRole: models.MemberRoleUser,
		},
		{
			// An org with no pattern at all has no rule-4 path: unknown users
			// fall through to a membership request.
			name:        "org without a pattern admits nobody",
			pattern:     "",
			email:       "alice@allowed.example",
			seeded:      true,
			wantPending: true,
		},
		{
			// A pattern that would match but fails validateAutoJoinRegex is
			// treated as absent, exactly like autoJoinMatchingOrgs does.
			name:        "unsafe pattern is treated as absent",
			pattern:     unsafePattern,
			email:       "alice@allowed.example",
			seeded:      true,
			wantPending: true,
		},
		{
			// Bootstrap preserved: the first user of an empty org still
			// becomes its admin, pattern or not.
			name:     "zero-member org bootstraps an admin",
			pattern:  "",
			email:    "founder@somewhere.example",
			seeded:   false,
			wantRole: models.MemberRoleAdmin,
		},
		{
			// An invitation admits an address the pattern would refuse, with
			// the invited role.
			name:     "invited email joins with the invited role",
			pattern:  safePattern,
			email:    "contractor@outside.example",
			seeded:   true,
			invited:  string(models.MemberRoleViewer),
			wantRole: models.MemberRoleViewer,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			svc, dbSvc, ctx := setupAuthTestService(t)
			org := joinTestOrg(ctx, t, dbSvc, "join-org", tt.seeded)
			user := joinTestUser(ctx, t, dbSvc, tt.email)

			if tt.pattern != "" {
				require.NoError(t, dbSvc.SetOrgParameter(
					ctx, org.UID, "registration.email_pattern", tt.pattern, false))
			}

			var inviteKey string
			if tt.invited != "" {
				inviteKey = inviteKeyPrefix + "tok-" + tt.name
				ttl := time.Hour
				require.NoError(t, dbSvc.SetStateEntry(ctx, &org.UID, inviteKey, &models.JSONMap{
					keyEmail: tt.email,
					"role":   tt.invited,
				}, &ttl))
			}

			member, pending, err := svc.JoinOrgViaLogin(ctx, org, user)
			require.NoError(t, err)
			require.Equal(t, tt.wantPending, pending)

			stored, memberErr := dbSvc.GetMemberByUserAndOrg(ctx, user.UID, org.UID)

			if tt.wantPending {
				require.Nil(t, member)
				require.Error(t, memberErr, "a refused login must leave no organization_members row")

				request, reqErr := dbSvc.GetMembershipRequestByOrgAndUser(ctx, org.UID, user.UID)
				require.NoError(t, reqErr)
				require.NotNil(t, request)
				require.Equal(t, models.MembershipRequestStatusPending, request.Status)

				return
			}

			require.NotNil(t, member)
			require.Equal(t, tt.wantRole, member.Role)
			require.NoError(t, memberErr)
			require.Equal(t, tt.wantRole, stored.Role)

			// No membership request is opened for an admitted user.
			request, reqErr := dbSvc.GetMembershipRequestByOrgAndUser(ctx, org.UID, user.UID)
			require.True(t, reqErr != nil || request == nil)

			if inviteKey != "" {
				// The invitation is single-use: consumed by the join.
				entry, entryErr := dbSvc.GetStateEntry(ctx, &org.UID, inviteKey)
				require.True(t, entryErr != nil || entry == nil, "invitation must be consumed")
			}
		})
	}
}

// TestJoinOrgViaLoginExistingMember proves a plain login by someone who is
// already a member changes nothing — no re-join, no role change, no request.
func TestJoinOrgViaLoginExistingMember(t *testing.T) {
	t.Parallel()

	svc, dbSvc, ctx := setupAuthTestService(t)
	org := joinTestOrg(ctx, t, dbSvc, "member-org", true)
	user := joinTestUser(ctx, t, dbSvc, "viewer@outside.example")

	existing := models.NewOrganizationMember(org.UID, user.UID, models.MemberRoleViewer)
	require.NoError(t, dbSvc.CreateOrganizationMember(ctx, existing))

	member, pending, err := svc.JoinOrgViaLogin(ctx, org, user)
	require.NoError(t, err)
	require.False(t, pending)
	require.NotNil(t, member)
	require.Equal(t, models.MemberRoleViewer, member.Role, "an existing member keeps their role")

	request, reqErr := dbSvc.GetMembershipRequestByOrgAndUser(ctx, org.UID, user.UID)
	require.True(t, reqErr != nil || request == nil)
}

// TestJoinOrgViaLoginRespectsMaxUsers proves the seat cap still gates the
// pattern path: a user the pattern *would* admit is refused when the org has
// no slot left, and no membership row appears.
func TestJoinOrgViaLoginRespectsMaxUsers(t *testing.T) {
	t.Parallel()

	svc, dbSvc, ctx := setupAuthTestService(t)
	svc.entitlements = &stubEntitlementsChecker{err: errLDAPSSOQuotaTest}

	org := joinTestOrg(ctx, t, dbSvc, "capped-org", true)
	user := joinTestUser(ctx, t, dbSvc, "alice@allowed.example")
	require.NoError(t, dbSvc.SetOrgParameter(ctx, org.UID, "registration.email_pattern", safePattern, false))

	_, _, err := svc.JoinOrgViaLogin(ctx, org, user)
	require.ErrorIs(t, err, errLDAPSSOQuotaTest)

	_, memberErr := dbSvc.GetMemberByUserAndOrg(ctx, user.UID, org.UID)
	require.Error(t, memberErr, "no membership may be created once the org is at its cap")
}

// TestPendingMembershipRedirect pins the redirect a refused login gets: the
// no-org request-access surface, carrying an org-less session and the
// explicit flag — never the org dashboard, and never an `org` handoff param
// (which would make the SPA treat this as an org-scoped sign-in).
func TestPendingMembershipRedirect(t *testing.T) {
	t.Parallel()

	redirect := pendingMembershipRedirect("acme", "tok-123", 3600)

	parsed, err := url.Parse(redirect)
	require.NoError(t, err)
	require.Equal(t, noOrgPath, parsed.Path)

	query := parsed.Query()
	require.Equal(t, "acme", query.Get(pendingMembershipParam))
	require.Equal(t, "tok-123", query.Get("access_token"))
	require.Equal(t, "3600", query.Get("expires_in"))
	require.Empty(t, query.Get("org"))
	require.Empty(t, query.Get("refresh_token"))
}

// TestMicrosoftCallbackRefusesNonMatchingEmail is the end-to-end reproduction
// of the 2026-08-07 incident: a Microsoft account whose (UPN-derived) address
// does not match the org's registration.email_pattern completes the OAuth
// callback and must come out with no membership, no org-scoped session, and a
// pending membership request. The matching address in the second half is the
// positive control.
func TestMicrosoftCallbackRefusesNonMatchingEmail(t *testing.T) {
	t.Parallel()

	run := func(t *testing.T, mail, upn string) (
		*MicrosoftOAuthResult, *MicrosoftOAuthService, *models.Organization, context.Context,
	) {
		t.Helper()

		svc, ctx := setupMicrosoftTestService(t)
		org := setupMicrosoftTestOrg(ctx, t, svc)

		// Seed a member so the org is past bootstrap, and set the pattern.
		seed := models.NewUser("seed@stonal.example")
		require.NoError(t, svc.db.CreateUser(ctx, seed))
		require.NoError(t, svc.db.CreateOrganizationMember(
			ctx, models.NewOrganizationMember(org.UID, seed.UID, models.MemberRoleAdmin)))
		require.NoError(t, svc.db.SetOrgParameter(
			ctx, org.UID, "registration.email_pattern", `@stonal\.example$`, false))

		tokenServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(MicrosoftTokenResponse{
				AccessToken: "mock-access-token", TokenType: "Bearer", ExpiresIn: 3600,
			})
		}))
		t.Cleanup(tokenServer.Close)

		userServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(MicrosoftUserInfo{
				ID: "ms-" + upn, DisplayName: "Callback User", Mail: mail, UserPrincipalName: upn,
			})
		}))
		t.Cleanup(userServer.Close)

		svc.httpClient = tokenServer.Client()

		result := testMicrosoftCallbackWithMockServers(ctx, t, svc, org.Slug, tokenServer.URL, userServer.URL)

		return result, svc, org, ctx
	}

	t.Run("outsider is left pending", func(t *testing.T) {
		t.Parallel()

		// Graph returned an empty `mail`, so the flow falls back to the
		// tenant UPN — the exact shape of the reported incident.
		result, svc, org, ctx := run(t, "", "patrice@stonal.onmicrosoft.example")

		require.True(t, result.Pending, "a non-matching account must not be admitted")
		require.Empty(t, result.RefreshToken, "no org-scoped session may be minted")
		require.NotEmpty(t, result.AccessToken, "the user is authenticated, just not a member")

		user, err := svc.db.GetUserByEmail(ctx, "patrice@stonal.onmicrosoft.example")
		require.NoError(t, err)

		_, memberErr := svc.db.GetMemberByUserAndOrg(ctx, user.UID, org.UID)
		require.Error(t, memberErr, "no organization_members row may exist")

		request, reqErr := svc.db.GetMembershipRequestByOrgAndUser(ctx, org.UID, user.UID)
		require.NoError(t, reqErr)
		require.Equal(t, models.MembershipRequestStatusPending, request.Status)

		// And the handler sends them to the request-access surface.
		redirect := pendingMembershipRedirect(result.OrgSlug, result.AccessToken, result.ExpiresIn)
		require.Contains(t, redirect, noOrgPath)
		require.Contains(t, redirect, pendingMembershipParam+"=")
	})

	t.Run("matching email joins", func(t *testing.T) {
		t.Parallel()

		result, svc, org, ctx := run(t, "alice@stonal.example", "alice@stonal.onmicrosoft.example")

		require.False(t, result.Pending)
		require.NotEmpty(t, result.RefreshToken, "an admitted user gets a full org-scoped session")

		user, err := svc.db.GetUserByEmail(ctx, "alice@stonal.example")
		require.NoError(t, err)

		member, memberErr := svc.db.GetMemberByUserAndOrg(ctx, user.UID, org.UID)
		require.NoError(t, memberErr)
		require.Equal(t, models.MemberRoleUser, member.Role)
	})
}

// TestGoogleCallbackRefusesNonMatchingEmail is the second provider required by
// the spec: the gate lives in the shared policy, so a different connector must
// behave identically.
func TestGoogleCallbackRefusesNonMatchingEmail(t *testing.T) {
	t.Parallel()

	svc, ctx := setupGoogleTestService(t)
	org := setupTestOrg(ctx, t, svc)

	seed := models.NewUser("seed@stonal.example")
	require.NoError(t, svc.db.CreateUser(ctx, seed))
	require.NoError(t, svc.db.CreateOrganizationMember(
		ctx, models.NewOrganizationMember(org.UID, seed.UID, models.MemberRoleAdmin)))
	require.NoError(t, svc.db.SetOrgParameter(
		ctx, org.UID, "registration.email_pattern", `@stonal\.example$`, false))

	tokenServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(GoogleTokenResponse{
			AccessToken: "mock-token", TokenType: "Bearer", ExpiresIn: 3600,
		})
	}))
	defer tokenServer.Close()

	userInfoServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(GoogleUserInfo{
			Sub: "google-outsider", Email: "outsider@gmail.example",
			EmailVerified: true, Name: "Outsider",
		})
	}))
	defer userInfoServer.Close()

	svc.httpClient = tokenServer.Client()

	result := testGoogleCallbackWithMockServers(ctx, t, svc, org.Slug, tokenServer.URL, userInfoServer.URL)

	require.True(t, result.Pending)
	require.Empty(t, result.RefreshToken)

	user, err := svc.db.GetUserByEmail(ctx, "outsider@gmail.example")
	require.NoError(t, err)

	_, memberErr := svc.db.GetMemberByUserAndOrg(ctx, user.UID, org.UID)
	require.Error(t, memberErr)

	request, reqErr := svc.db.GetMembershipRequestByOrgAndUser(ctx, org.UID, user.UID)
	require.NoError(t, reqErr)
	require.Equal(t, models.MembershipRequestStatusPending, request.Status)
}

// TestFinishProviderCallbackPendingRedirect exercises the handler tail every
// provider callback now shares: a pending result must send the browser to the
// no-org request-access surface, NOT to the success redirect the login flow
// originally asked for (which is the org dashboard).
func TestFinishProviderCallbackPendingRedirect(t *testing.T) {
	t.Parallel()

	t.Run("pending goes to the no-org surface", func(t *testing.T) {
		t.Parallel()

		recorder := httptest.NewRecorder()
		req := httptest.NewRequestWithContext(
			t.Context(), http.MethodGet, "/api/v1/auth/microsoft/callback", nil)

		require.NoError(t, finishProviderCallback(
			recorder, req, "/dash0/orgs/acme?access_token=at", "acme", "at", 3600, true))

		location := recorder.Header().Get("Location")
		require.Contains(t, location, noOrgPath)
		require.NotContains(t, location, "/dash0/orgs/acme")
		require.Contains(t, location, pendingMembershipParam+"=acme")
	})

	t.Run("admitted keeps the success redirect", func(t *testing.T) {
		t.Parallel()

		recorder := httptest.NewRecorder()
		req := httptest.NewRequestWithContext(
			t.Context(), http.MethodGet, "/api/v1/auth/microsoft/callback", nil)

		require.NoError(t, finishProviderCallback(
			recorder, req, "/dash0/orgs/acme?access_token=at", "acme", "at", 3600, false))

		require.Equal(t, "/dash0/orgs/acme?access_token=at", recorder.Header().Get("Location"))
	})
}
