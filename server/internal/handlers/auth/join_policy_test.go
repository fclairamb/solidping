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
			// An org with no pattern at all has no rule-5 path: unknown users
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
			// Bootstrap preserved, but the minted role changed deliberately
			// with spec 2026-08-08-11: whoever brings an org into existence
			// OWNS it, so the first user of an empty org becomes its owner
			// (owner outranks admin, so every admin gate still passes).
			name:     "zero-member org bootstraps an owner",
			pattern:  "",
			email:    "founder@somewhere.example",
			seeded:   false,
			wantRole: models.MemberRoleOwner,
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

// linkSlackWorkspace links org to a Slack workspace (team ID) through
// organization_providers — the same row the Slack sign-in and install flows
// create, and the only thing the attestation rule trusts.
func linkSlackWorkspace(
	ctx context.Context, t *testing.T, dbSvc db.Service, orgUID, teamID string,
) *models.OrganizationProvider {
	t.Helper()

	provider := models.NewOrganizationProvider(orgUID, models.ProviderTypeSlack, teamID)
	require.NoError(t, dbSvc.CreateOrganizationProvider(ctx, provider))

	return provider
}

// TestJoinOrgViaLoginSlackWorkspace is the admission-control table for the
// Slack workspace attestation rule. Every negative is paired with a positive
// control in the same table, so a rule that admitted nobody (or everybody)
// fails here.
func TestJoinOrgViaLoginSlackWorkspace(t *testing.T) {
	t.Parallel()

	const (
		ourTeam   = "T-OURS"
		otherTeam = "T-THEIRS"
	)

	tests := []struct {
		name string
		// linkTeam is the workspace this org is linked to ("" = the org has
		// no Slack link at all).
		linkTeam string
		// revokeLink soft-deletes the org↔workspace link before the login.
		revokeLink bool
		// otherOrgTeam, when set, links a DIFFERENT org to that workspace —
		// the cross-tenant setup.
		otherOrgTeam string
		// attestTeam is the workspace Slack attested for the user ("" = a
		// non-Slack connector: no attestation at all).
		attestTeam string
		// optOut is the raw value written to
		// registration.slack_workspace_auto_join when writeOptOut is set.
		// Leaving writeOptOut false means the parameter is ABSENT.
		optOut      any
		writeOptOut bool

		wantPending bool
		wantRole    models.MemberRole
	}{
		{
			// POSITIVE CONTROL — the core fix: a fellow workspace member
			// signing in to the org that workspace is linked to gets in. Also
			// the control for the opt-out cases below: the parameter is
			// ABSENT here, which must mean enabled.
			name:       "member of the linked workspace joins as user",
			linkTeam:   ourTeam,
			attestTeam: ourTeam,
			wantRole:   models.MemberRoleUser,
		},
		{
			// NEGATIVE — attestation for a workspace that is not this org's.
			// The org slug in the login URL is attacker-controlled, so the
			// decision must follow the verified team ID.
			name:        "attestation for another workspace is refused",
			linkTeam:    ourTeam,
			attestTeam:  otherTeam,
			wantPending: true,
		},
		{
			// NEGATIVE (cross-tenant) — the attested workspace really exists
			// and really is linked... to somebody else's org.
			name:         "workspace linked to another org does not admit here",
			linkTeam:     ourTeam,
			otherOrgTeam: otherTeam,
			attestTeam:   otherTeam,
			wantPending:  true,
		},
		{
			// NEGATIVE — no Slack link on this org: nothing to attest against.
			name:        "org with no slack link admits nobody",
			linkTeam:    "",
			attestTeam:  ourTeam,
			wantPending: true,
		},
		{
			// NEGATIVE — a revoked (soft-deleted) link is not a link.
			name:        "revoked workspace link admits nobody",
			linkTeam:    ourTeam,
			revokeLink:  true,
			attestTeam:  ourTeam,
			wantPending: true,
		},
		{
			// NEGATIVE — a connector with nothing to attest (Google, GitHub,
			// SAML…) sees exactly today's behavior in a Slack-linked org.
			name:        "federated login without attestation is unaffected",
			linkTeam:    ourTeam,
			attestTeam:  "",
			wantPending: true,
		},
		{
			// Opt-out honored: back to the pre-attestation behavior.
			name:        "opted-out org falls back to a membership request",
			linkTeam:    ourTeam,
			attestTeam:  ourTeam,
			optOut:      false,
			writeOptOut: true,
			wantPending: true,
		},
		{
			// POSITIVE CONTROL for the opt-out: an explicit true behaves like
			// the default.
			name:        "explicit opt-in joins as user",
			linkTeam:    ourTeam,
			attestTeam:  ourTeam,
			optOut:      true,
			writeOptOut: true,
			wantRole:    models.MemberRoleUser,
		},
		{
			// A deny switch must fail CLOSED: an admin who typed something
			// strconv.ParseBool cannot read ("off", "no", "disabled") meant to
			// turn admission off, and silently granting access instead would
			// be the wrong direction.
			name:        "unreadable opt-out value denies rather than admits",
			linkTeam:    ourTeam,
			attestTeam:  ourTeam,
			optOut:      "off",
			writeOptOut: true,
			wantPending: true,
		},
		{
			// Same, for a parameter row whose value is not a scalar at all.
			name:        "null opt-out value denies rather than admits",
			linkTeam:    ourTeam,
			attestTeam:  ourTeam,
			optOut:      nil,
			writeOptOut: true,
			wantPending: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			svc, dbSvc, ctx := setupAuthTestService(t)
			org := joinTestOrg(ctx, t, dbSvc, "slack-org", true)
			user := joinTestUser(ctx, t, dbSvc, "member@workspace.example")

			if tt.linkTeam != "" {
				provider := linkSlackWorkspace(ctx, t, dbSvc, org.UID, tt.linkTeam)
				if tt.revokeLink {
					require.NoError(t, dbSvc.DeleteOrganizationProvider(ctx, provider.UID))
				}
			}

			if tt.otherOrgTeam != "" {
				otherOrg := joinTestOrg(ctx, t, dbSvc, "other-org", true)
				linkSlackWorkspace(ctx, t, dbSvc, otherOrg.UID, tt.otherOrgTeam)
			}

			if tt.writeOptOut {
				require.NoError(t, dbSvc.SetOrgParameter(
					ctx, org.UID, registrationSlackAutoJoinKey, tt.optOut, false))
			}

			var opts []LoginOption
			if tt.attestTeam != "" {
				opts = append(opts, WithSlackWorkspace(tt.attestTeam))
			}

			member, pending, err := svc.JoinOrgViaLogin(ctx, org, user, opts...)
			require.NoError(t, err)
			require.Equal(t, tt.wantPending, pending)

			if tt.wantPending {
				require.Nil(t, member)

				_, memberErr := dbSvc.GetMemberByUserAndOrg(ctx, user.UID, org.UID)
				require.Error(t, memberErr, "a refused login must leave no organization_members row")

				request, reqErr := dbSvc.GetMembershipRequestByOrgAndUser(ctx, org.UID, user.UID)
				require.NoError(t, reqErr)
				require.NotNil(t, request)
				require.Equal(t, models.MembershipRequestStatusPending, request.Status)

				return
			}

			require.NotNil(t, member)
			require.Equal(t, tt.wantRole, member.Role)

			stored, memberErr := dbSvc.GetMemberByUserAndOrg(ctx, user.UID, org.UID)
			require.NoError(t, memberErr)
			require.Equal(t, tt.wantRole, stored.Role)
		})
	}
}

// TestJoinOrgViaLoginSlackWorkspaceCrossTenantIsolation is the cross-tenant
// negative stated as the property it protects: the SAME attested user, the
// SAME two orgs, admitted to their own workspace's org and refused by the
// other one — proving the rule follows the verified team ID rather than the
// org slug the login URL asked for.
func TestJoinOrgViaLoginSlackWorkspaceCrossTenantIsolation(t *testing.T) {
	t.Parallel()

	svc, dbSvc, ctx := setupAuthTestService(t)

	orgA := joinTestOrg(ctx, t, dbSvc, "tenant-a", true)
	orgB := joinTestOrg(ctx, t, dbSvc, "tenant-b", true)
	linkSlackWorkspace(ctx, t, dbSvc, orgA.UID, "T-A")
	linkSlackWorkspace(ctx, t, dbSvc, orgB.UID, "T-B")

	user := joinTestUser(ctx, t, dbSvc, "alice@tenant-a.example")

	// Asking for org B while Slack attested workspace A: refused.
	member, pending, err := svc.JoinOrgViaLogin(ctx, orgB, user, WithSlackWorkspace("T-A"))
	require.NoError(t, err)
	require.True(t, pending, "workspace A must not open org B")
	require.Nil(t, member)

	_, memberErr := dbSvc.GetMemberByUserAndOrg(ctx, user.UID, orgB.UID)
	require.Error(t, memberErr)

	// Same user, same attestation, their own org: admitted.
	member, pending, err = svc.JoinOrgViaLogin(ctx, orgA, user, WithSlackWorkspace("T-A"))
	require.NoError(t, err)
	require.False(t, pending)
	require.NotNil(t, member)
	require.Equal(t, models.MemberRoleUser, member.Role)
}

// TestJoinOrgViaLoginSlackWorkspaceRespectsMaxUsers proves the seat cap is not
// bypassed by the attestation rule: a workspace member the rule would admit
// gets a membership request instead when the org has no slot left, and no
// membership row appears.
func TestJoinOrgViaLoginSlackWorkspaceRespectsMaxUsers(t *testing.T) {
	t.Parallel()

	svc, dbSvc, ctx := setupAuthTestService(t)
	svc.entitlements = &stubEntitlementsChecker{err: errLDAPSSOQuotaTest}

	org := joinTestOrg(ctx, t, dbSvc, "capped-slack-org", true)
	linkSlackWorkspace(ctx, t, dbSvc, org.UID, "T-CAPPED")
	user := joinTestUser(ctx, t, dbSvc, "member@workspace.example")

	member, pending, err := svc.JoinOrgViaLogin(ctx, org, user, WithSlackWorkspace("T-CAPPED"))
	require.NoError(t, err, "a capped org must not fail the login, only refuse the membership")
	require.True(t, pending)
	require.Nil(t, member)

	_, memberErr := dbSvc.GetMemberByUserAndOrg(ctx, user.UID, org.UID)
	require.Error(t, memberErr, "no membership may be created once the org is at its cap")

	request, reqErr := dbSvc.GetMembershipRequestByOrgAndUser(ctx, org.UID, user.UID)
	require.NoError(t, reqErr)
	require.Equal(t, models.MembershipRequestStatusPending, request.Status)
}

// TestJoinOrgViaLoginSlackWorkspaceBootstrapStaysOwner pins that the Slack
// workspace path does not short-circuit the bootstrap rule: the very first
// member of a freshly linked workspace org is its OWNER, not the plain `user`
// the workspace-attestation rule would grant (spec 2026-08-08-11).
func TestJoinOrgViaLoginSlackWorkspaceBootstrapStaysOwner(t *testing.T) {
	t.Parallel()

	svc, dbSvc, ctx := setupAuthTestService(t)
	org := joinTestOrg(ctx, t, dbSvc, "fresh-slack-org", false)
	linkSlackWorkspace(ctx, t, dbSvc, org.UID, "T-FRESH")
	user := joinTestUser(ctx, t, dbSvc, "founder@workspace.example")

	member, pending, err := svc.JoinOrgViaLogin(ctx, org, user, WithSlackWorkspace("T-FRESH"))
	require.NoError(t, err)
	require.False(t, pending)
	require.NotNil(t, member)
	require.Equal(t, models.MemberRoleOwner, member.Role)
}

// TestParseParamBool pins how the opt-out parameter is decoded: JSON(B)
// round-trips can hand back a bool, a string or a number, and anything else
// must report "not decodable" (ok=false) rather than quietly becoming a
// boolean — the caller decides which way an unreadable switch fails.
func TestParseParamBool(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		raw    any
		want   bool
		wantOK bool
	}{
		{name: "bool false", raw: false, want: false, wantOK: true},
		{name: "bool true", raw: true, want: true, wantOK: true},
		{name: "string false", raw: "false", want: false, wantOK: true},
		{name: "padded string", raw: " true ", want: true, wantOK: true},
		{name: "number zero", raw: float64(0), want: false, wantOK: true},
		{name: "number one", raw: float64(1), want: true, wantOK: true},
		{name: "word off is not decodable", raw: "off", wantOK: false},
		{name: "word no is not decodable", raw: "no", wantOK: false},
		{name: "nil is not decodable", raw: nil, wantOK: false},
		{name: "map is not decodable", raw: map[string]any{}, wantOK: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			value, ok := parseParamBool(tt.raw)
			require.Equal(t, tt.wantOK, ok)

			if tt.wantOK {
				require.Equal(t, tt.want, value)
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
		seed := models.NewUser("seed@acme.example")
		require.NoError(t, svc.db.CreateUser(ctx, seed))
		require.NoError(t, svc.db.CreateOrganizationMember(
			ctx, models.NewOrganizationMember(org.UID, seed.UID, models.MemberRoleAdmin)))
		require.NoError(t, svc.db.SetOrgParameter(
			ctx, org.UID, "registration.email_pattern", `@acme\.example$`, false))

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
		result, svc, org, ctx := run(t, "", "patrice@acme.onmicrosoft.example")

		require.True(t, result.Pending, "a non-matching account must not be admitted")
		require.Empty(t, result.RefreshToken, "no org-scoped session may be minted")
		require.NotEmpty(t, result.AccessToken, "the user is authenticated, just not a member")

		user, err := svc.db.GetUserByEmail(ctx, "patrice@acme.onmicrosoft.example")
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

		result, svc, org, ctx := run(t, "alice@acme.example", "alice@acme.onmicrosoft.example")

		require.False(t, result.Pending)
		require.NotEmpty(t, result.RefreshToken, "an admitted user gets a full org-scoped session")

		user, err := svc.db.GetUserByEmail(ctx, "alice@acme.example")
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

	seed := models.NewUser("seed@acme.example")
	require.NoError(t, svc.db.CreateUser(ctx, seed))
	require.NoError(t, svc.db.CreateOrganizationMember(
		ctx, models.NewOrganizationMember(org.UID, seed.UID, models.MemberRoleAdmin)))
	require.NoError(t, svc.db.SetOrgParameter(
		ctx, org.UID, "registration.email_pattern", `@acme\.example$`, false))

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
