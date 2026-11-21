package auth

import (
	"context"
	"testing"

	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/authhandoff"
	"github.com/fclairamb/solidping/server/internal/db/models"
)

func TestParseGroupsClaim(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name  string
		value any
		want  []string
	}{
		{"array", []any{"ops", "admins"}, []string{"ops", "admins"}},
		{"array skips non-strings and blanks", []any{"ops", 42, " ", true, "admins"}, []string{"ops", "admins"}},
		{"string slice", []string{"ops"}, []string{"ops"}},
		{"single string", "admins", []string{"admins"}},
		{"space-separated string", " ops  admins ", []string{"ops", "admins"}},
		{"missing", nil, nil},
		{"wrong type", map[string]any{"a": "b"}, nil},
		{"number", 12.0, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tc.want, parseGroupsClaim(tc.value))
		})
	}
}

func TestMapClaims_Groups(t *testing.T) {
	t.Parallel()

	idp := newFakeOIDCIdP(t)
	svc, _ := setupOIDCTestService(t, idp, nil)

	info := svc.mapClaims("sub", map[string]any{"groups": []any{"admins"}, "roles": "ops dev"})
	require.Equal(t, []string{"admins"}, info.Groups, "default claim is groups")

	svc.cfg.OIDC.GroupsClaim = "roles"
	info = svc.mapClaims("sub", map[string]any{"groups": []any{"admins"}, "roles": "ops dev"})
	require.Equal(t, []string{"ops", "dev"}, info.Groups)
}

// oidcLogin runs one callback for the IdP user with the given groups.
func oidcLogin(
	ctx context.Context, t *testing.T, svc *OIDCOAuthService, idp *fakeOIDCIdP, org *models.Organization,
	sub, email string, groups []any,
) *models.User {
	t.Helper()

	user, _ := oidcLoginOutcome(ctx, t, svc, idp, org, sub, email, groups)

	return user
}

// oidcLoginOutcome is oidcLogin, also returning the callback outcome.
func oidcLoginOutcome(
	ctx context.Context, t *testing.T, svc *OIDCOAuthService, idp *fakeOIDCIdP, org *models.Organization,
	sub, email string, groups []any,
) (*models.User, *OIDCOAuthResult) {
	t.Helper()

	idp.nextIDToken = idp.issueIDToken(t, func(c jwt.MapClaims) {
		c["sub"] = sub
		c[keyEmail] = email

		if groups != nil {
			c["groups"] = groups
		}
	})

	outcome, err := svc.HandleCallback(ctx, "code", org.Slug)
	require.NoError(t, err)

	user, err := svc.db.GetUserByEmail(ctx, email)
	require.NoError(t, err)

	return user, outcome
}

func countEvents(
	ctx context.Context, t *testing.T, svc *OIDCOAuthService, orgUID string, eventType models.EventType,
) int {
	t.Helper()

	count, err := svc.db.DB().NewSelect().Model((*models.Event)(nil)).
		Where("organization_uid = ?", orgUID).Where("event_type = ?", string(eventType)).Count(ctx)
	require.NoError(t, err)

	return count
}

// seedLocalSuperAdmin makes a member of org a locally granted super admin.
func seedLocalSuperAdmin(ctx context.Context, t *testing.T, svc *OIDCOAuthService, org *models.Organization) {
	t.Helper()

	admin := models.NewUser("root@acme.com")
	admin.SuperAdmin = true
	require.NoError(t, svc.db.CreateUser(ctx, admin))
	require.NoError(t, svc.db.CreateOrganizationMember(ctx,
		models.NewOrganizationMember(org.UID, admin.UID, models.MemberRoleOwner)))
}

func TestOIDCGroups_SuperAdminGrantedThenRevoked(t *testing.T) {
	t.Parallel()

	idp := newFakeOIDCIdP(t)
	svc, ctx := setupOIDCTestService(t, idp, nil)
	svc.cfg.OIDC.SuperAdminGroups = "sp-admins, other"
	org := setupOIDCTestOrg(ctx, t, svc)
	seedLocalSuperAdmin(ctx, t, svc, org)

	user := oidcLogin(ctx, t, svc, idp, org, "u1", "alice@acme.com", []any{"staff", "sp-admins"})
	require.True(t, user.SuperAdmin, "granted on the first login")
	require.NotNil(t, user.SuperAdminSource)
	require.Equal(t, models.SuperAdminSourceOIDC, *user.SuperAdminSource)
	require.Equal(t, 1, countEvents(ctx, t, svc, org.UID, models.EventTypeAuthSuperAdminChanged))

	user = oidcLogin(ctx, t, svc, idp, org, "u1", "alice@acme.com", []any{"sp-admins"})
	require.True(t, user.SuperAdmin)
	require.Equal(t, 1, countEvents(ctx, t, svc, org.UID, models.EventTypeAuthSuperAdminChanged),
		"no event when nothing changed")

	user = oidcLogin(ctx, t, svc, idp, org, "u1", "alice@acme.com", []any{"staff"})
	require.False(t, user.SuperAdmin, "revoked once out of the group")
	require.Nil(t, user.SuperAdminSource)
	require.Equal(t, 2, countEvents(ctx, t, svc, org.UID, models.EventTypeAuthSuperAdminChanged))
}

func TestOIDCGroups_LocalSuperAdminUntouched(t *testing.T) {
	t.Parallel()

	idp := newFakeOIDCIdP(t)
	svc, ctx := setupOIDCTestService(t, idp, nil)
	svc.cfg.OIDC.SuperAdminGroups = "sp-admins"
	org := setupOIDCTestOrg(ctx, t, svc)
	seedLocalSuperAdmin(ctx, t, svc, org)

	local := models.NewUser("bob@acme.com")
	local.SuperAdmin = true
	require.NoError(t, svc.db.CreateUser(ctx, local))

	// In the group: stays a LOCAL super admin (no source recorded).
	user := oidcLogin(ctx, t, svc, idp, org, "u2", "bob@acme.com", []any{"sp-admins"})
	require.True(t, user.SuperAdmin)
	require.Nil(t, user.SuperAdminSource)

	// Out of the group: never demoted by the claim.
	user = oidcLogin(ctx, t, svc, idp, org, "u2", "bob@acme.com", nil)
	require.True(t, user.SuperAdmin)
	require.Equal(t, 0, countEvents(ctx, t, svc, org.UID, models.EventTypeAuthSuperAdminChanged))
}

func TestOIDCGroups_LastSuperAdminNeverDemoted(t *testing.T) {
	t.Parallel()

	idp := newFakeOIDCIdP(t)
	svc, ctx := setupOIDCTestService(t, idp, nil)
	svc.cfg.OIDC.SuperAdminGroups = "sp-admins"
	org := setupOIDCTestOrg(ctx, t, svc)

	user := oidcLogin(ctx, t, svc, idp, org, "u3", "carol@acme.com", []any{"sp-admins"})
	require.True(t, user.SuperAdmin)

	count, err := svc.db.CountSuperAdmins(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, count)

	user = oidcLogin(ctx, t, svc, idp, org, "u3", "carol@acme.com", []any{"staff"})
	require.True(t, user.SuperAdmin, "the last super admin is kept")
}

func TestOIDCGroups_OrgAdminRoleInLoginOrg(t *testing.T) {
	t.Parallel()

	idp := newFakeOIDCIdP(t)
	svc, ctx := setupOIDCTestService(t, idp, nil)
	svc.cfg.OIDC.OrgAdminGroups = "acme-admins"
	org := setupOIDCTestOrg(ctx, t, svc)
	seedLocalSuperAdmin(ctx, t, svc, org)

	// Not a member yet, in the group: joins as admin.
	user := oidcLogin(ctx, t, svc, idp, org, "u4", "dave@acme.com", []any{"acme-admins"})
	member, err := svc.db.GetMemberByUserAndOrg(ctx, user.UID, org.UID)
	require.NoError(t, err)
	require.Equal(t, models.MemberRoleAdmin, member.Role)
	require.False(t, user.SuperAdmin, "org admin groups never grant super admin")

	// A plain member in the group is raised to admin.
	plain := models.NewUser("erin@acme.com")
	require.NoError(t, svc.db.CreateUser(ctx, plain))
	plainMember := models.NewOrganizationMember(org.UID, plain.UID, models.MemberRoleUser)
	require.NoError(t, svc.db.CreateOrganizationMember(ctx, plainMember))

	oidcLogin(ctx, t, svc, idp, org, "u5", "erin@acme.com", []any{"acme-admins"})
	member, err = svc.db.GetMemberByUserAndOrg(ctx, plain.UID, org.UID)
	require.NoError(t, err)
	require.Equal(t, models.MemberRoleAdmin, member.Role)
	require.Equal(t, 1, countEvents(ctx, t, svc, org.UID, models.EventTypeMemberRoleChanged))
}

// OIDC-only first boot: the empty default org goes to the first super-admin
// group member, not to whoever signs in first.
func TestOIDCGroups_FirstGroupUserOwnsEmptyOrg(t *testing.T) {
	t.Parallel()

	idp := newFakeOIDCIdP(t)
	svc, ctx := setupOIDCTestService(t, idp, nil)
	svc.cfg.OIDC.SuperAdminGroups = "sp-admins"
	org := setupOIDCTestOrg(ctx, t, svc)

	outsider := oidcLogin(ctx, t, svc, idp, org, "u6", "frank@acme.com", []any{"staff"})
	_, err := svc.db.GetMemberByUserAndOrg(ctx, outsider.UID, org.UID)
	require.Error(t, err, "a non-group user does not bootstrap the org")

	admin := oidcLogin(ctx, t, svc, idp, org, "u7", "grace@acme.com", []any{"sp-admins"})
	require.True(t, admin.SuperAdmin)

	member, err := svc.db.GetMemberByUserAndOrg(ctx, admin.UID, org.UID)
	require.NoError(t, err)
	require.Equal(t, models.MemberRoleOwner, member.Role)
}

// Groups the IdP only exposes on the userinfo endpoint count too.
func TestOIDCGroups_UserInfoGroupsMerged(t *testing.T) {
	t.Parallel()

	idp := newFakeOIDCIdP(t)
	svc, ctx := setupOIDCTestService(t, idp, nil)
	svc.cfg.OIDC.SuperAdminGroups = "sp-admins"
	org := setupOIDCTestOrg(ctx, t, svc)
	seedLocalSuperAdmin(ctx, t, svc, org)

	idp.userInfo = map[string]any{"sub": "u8", "groups": []any{"sp-admins"}}
	user := oidcLogin(ctx, t, svc, idp, org, "u8", "heidi@acme.com", nil)
	require.True(t, user.SuperAdmin)

	// A userinfo answer for another subject is ignored.
	idp.userInfo = map[string]any{"sub": "someone-else", "groups": []any{"sp-admins"}}
	user = oidcLogin(ctx, t, svc, idp, org, "u9", "ivan@acme.com", nil)
	require.False(t, user.SuperAdmin)
}

// No mapping configured: today's behavior, the claim is ignored.
func TestOIDCGroups_OffByDefault(t *testing.T) {
	t.Parallel()

	idp := newFakeOIDCIdP(t)
	svc, ctx := setupOIDCTestService(t, idp, nil)
	org := setupOIDCTestOrg(ctx, t, svc)

	user := oidcLogin(ctx, t, svc, idp, org, "u10", "judy@acme.com", []any{"sp-admins"})
	require.False(t, user.SuperAdmin)

	member, err := svc.db.GetMemberByUserAndOrg(ctx, user.UID, org.UID)
	require.NoError(t, err)
	require.Equal(t, models.MemberRoleOwner, member.Role, "the empty-org bootstrap is unchanged")
}

// The grant and the revoke of super admin are reported in the login
// response diagnostics, through the handoff code the dashboard redeems.
func TestOIDCGroups_RoleChangesInLoginResponse(t *testing.T) {
	t.Parallel()

	idp := newFakeOIDCIdP(t)
	svc, ctx := setupOIDCTestService(t, idp, nil)
	svc.cfg.OIDC.SuperAdminGroups = "sp-admins"
	org := setupOIDCTestOrg(ctx, t, svc)
	seedLocalSuperAdmin(ctx, t, svc, org)
	require.NoError(t, svc.db.CreateOrganizationMember(ctx, models.NewOrganizationMember(org.UID,
		mustCreateUser(ctx, t, svc, "kim@acme.com").UID, models.MemberRoleUser)))

	exchange := func(outcome *OIDCOAuthResult) []string {
		t.Helper()

		code, err := authhandoff.Issue(ctx, svc.db, outcome.handoffSession("/"))
		require.NoError(t, err)

		resp, err := svc.authService.ExchangeHandoffCode(ctx, code)
		require.NoError(t, err)

		return resp.RoleChanges
	}

	_, outcome := oidcLoginOutcome(ctx, t, svc, idp, org, "u11", "kim@acme.com", []any{"sp-admins"})
	require.Equal(t, []string{RoleChangeSuperAdminGranted}, outcome.RoleChanges)
	require.Equal(t, []string{RoleChangeSuperAdminGranted}, exchange(outcome))

	_, outcome = oidcLoginOutcome(ctx, t, svc, idp, org, "u11", "kim@acme.com", []any{"sp-admins"})
	require.Empty(t, outcome.RoleChanges, "nothing changed, nothing reported")
	require.Empty(t, exchange(outcome))

	_, outcome = oidcLoginOutcome(ctx, t, svc, idp, org, "u11", "kim@acme.com", nil)
	require.Equal(t, []string{RoleChangeSuperAdminRevoked}, outcome.RoleChanges)
	require.Equal(t, []string{RoleChangeSuperAdminRevoked}, exchange(outcome))
}

func mustCreateUser(ctx context.Context, t *testing.T, svc *OIDCOAuthService, email string) *models.User {
	t.Helper()

	user := models.NewUser(email)
	require.NoError(t, svc.db.CreateUser(ctx, user))

	return user
}

// The admin role the org-admin claim granted is demoted to user once the user
// leaves the group; a locally granted admin role is never touched.
func TestOIDCGroups_OrgAdminRevokedOnlyWhenClaimGranted(t *testing.T) {
	t.Parallel()

	idp := newFakeOIDCIdP(t)
	svc, ctx := setupOIDCTestService(t, idp, nil)
	svc.cfg.OIDC.OrgAdminGroups = "acme-admins"
	org := setupOIDCTestOrg(ctx, t, svc)
	seedLocalSuperAdmin(ctx, t, svc, org)

	// Claim grant: joins as admin, source recorded.
	user, outcome := oidcLoginOutcome(ctx, t, svc, idp, org, "u12", "leo@acme.com", []any{"acme-admins"})
	require.Equal(t, []string{RoleChangeOrgAdminGranted}, outcome.RoleChanges)
	member, err := svc.db.GetMemberByUserAndOrg(ctx, user.UID, org.UID)
	require.NoError(t, err)
	require.Equal(t, models.MemberRoleAdmin, member.Role)
	require.NotNil(t, member.RoleSource)
	require.Equal(t, models.MemberRoleSourceOIDCGroup, *member.RoleSource)

	// Out of the group: demoted to user, source cleared, audited.
	before := countEvents(ctx, t, svc, org.UID, models.EventTypeMemberRoleChanged)
	_, outcome = oidcLoginOutcome(ctx, t, svc, idp, org, "u12", "leo@acme.com", []any{"staff"})
	require.Equal(t, []string{RoleChangeOrgAdminRevoked}, outcome.RoleChanges)
	member, err = svc.db.GetMemberByUserAndOrg(ctx, user.UID, org.UID)
	require.NoError(t, err)
	require.Equal(t, models.MemberRoleUser, member.Role)
	require.Nil(t, member.RoleSource)
	require.Equal(t, before+1, countEvents(ctx, t, svc, org.UID, models.EventTypeMemberRoleChanged))

	// A plain member raised by the claim, then made admin locally: the local
	// change owns the role, so leaving the group no longer demotes it.
	user, _ = oidcLoginOutcome(ctx, t, svc, idp, org, "u12", "leo@acme.com", []any{"acme-admins"})
	member, err = svc.db.GetMemberByUserAndOrg(ctx, user.UID, org.UID)
	require.NoError(t, err)
	require.NotNil(t, member.RoleSource)

	role := models.MemberRoleAdmin
	require.NoError(t, svc.db.UpdateOrganizationMember(ctx, member.UID, models.OrganizationMemberUpdate{Role: &role}))

	_, outcome = oidcLoginOutcome(ctx, t, svc, idp, org, "u12", "leo@acme.com", nil)
	require.Empty(t, outcome.RoleChanges)
	member, err = svc.db.GetMemberByUserAndOrg(ctx, user.UID, org.UID)
	require.NoError(t, err)
	require.Equal(t, models.MemberRoleAdmin, member.Role, "a local admin role is never demoted")
	require.Nil(t, member.RoleSource)
}

// The claim never demotes the last owner/admin of the org.
func TestOIDCGroups_LastOrgAdminNeverDemoted(t *testing.T) {
	t.Parallel()

	idp := newFakeOIDCIdP(t)
	svc, ctx := setupOIDCTestService(t, idp, nil)
	svc.cfg.OIDC.OrgAdminGroups = "acme-admins"
	org := setupOIDCTestOrg(ctx, t, svc)

	// The org has a plain member only, so the claim admin is its only admin.
	require.NoError(t, svc.db.CreateOrganizationMember(ctx, models.NewOrganizationMember(org.UID,
		mustCreateUser(ctx, t, svc, "mia@acme.com").UID, models.MemberRoleUser)))

	user, _ := oidcLoginOutcome(ctx, t, svc, idp, org, "u13", "noah@acme.com", []any{"acme-admins"})

	_, outcome := oidcLoginOutcome(ctx, t, svc, idp, org, "u13", "noah@acme.com", nil)
	require.Equal(t, []string{RoleChangeOrgAdminKept}, outcome.RoleChanges)

	member, err := svc.db.GetMemberByUserAndOrg(ctx, user.UID, org.UID)
	require.NoError(t, err)
	require.Equal(t, models.MemberRoleAdmin, member.Role, "the last admin is kept")
}

// An owner is never touched by the org-admin claim, in or out of the group.
func TestOIDCGroups_OwnerUntouchedByOrgAdminClaim(t *testing.T) {
	t.Parallel()

	idp := newFakeOIDCIdP(t)
	svc, ctx := setupOIDCTestService(t, idp, nil)
	svc.cfg.OIDC.OrgAdminGroups = "acme-admins"
	org := setupOIDCTestOrg(ctx, t, svc)
	seedLocalSuperAdmin(ctx, t, svc, org)

	owner := mustCreateUser(ctx, t, svc, "olga@acme.com")
	require.NoError(t, svc.db.CreateOrganizationMember(ctx,
		models.NewOrganizationMember(org.UID, owner.UID, models.MemberRoleOwner)))

	for _, groups := range [][]any{{"acme-admins"}, nil} {
		_, outcome := oidcLoginOutcome(ctx, t, svc, idp, org, "u14", "olga@acme.com", groups)
		require.Empty(t, outcome.RoleChanges)
	}

	member, err := svc.db.GetMemberByUserAndOrg(ctx, owner.UID, org.UID)
	require.NoError(t, err)
	require.Equal(t, models.MemberRoleOwner, member.Role)
}
