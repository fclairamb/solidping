package dbcontract

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

func runProviders(t *testing.T, fix *fixture) {
	t.Helper()

	ctx := t.Context()
	must := require.New(t)
	svc := fix.svc

	provider := models.NewOrganizationProvider(fix.org.UID, models.ProviderTypeGitHub, "gh-contract")
	must.NoError(svc.CreateOrganizationProvider(ctx, provider))

	name := "GitHub"
	meta := models.JSONMap{"k": "v"}
	must.NoError(svc.UpdateOrganizationProvider(ctx, provider.UID, models.OrganizationProviderUpdate{
		ProviderName: &name, Metadata: &meta,
	}))

	got, err := svc.GetOrganizationProvider(ctx, provider.UID)
	must.NoError(err)
	must.Equal(name, got.ProviderName)

	userProvider := models.NewUserProvider(fix.user.UID, models.ProviderTypeGoogle, "g-contract")
	must.NoError(svc.CreateUserProvider(ctx, userProvider))

	list, err := svc.ListUserProvidersByUser(ctx, fix.user.UID)
	must.NoError(err)
	must.Len(list, 1)
	must.Equal(userProvider.UID, list[0].UID)

	byID, err := svc.GetUserProviderByProviderID(ctx, models.ProviderTypeGoogle, "g-contract")
	must.NoError(err)
	must.Equal(userProvider.UID, byID.UID)

	must.NoError(svc.DeleteUserProvider(ctx, userProvider.UID))

	list, err = svc.ListUserProvidersByUser(ctx, fix.user.UID)
	must.NoError(err)
	must.Empty(list)
}

func runUserTokens(t *testing.T, fix *fixture) {
	t.Helper()

	ctx := t.Context()
	must := require.New(t)
	svc := fix.svc

	user := models.NewUser("tokens@acme.com")
	must.NoError(svc.CreateUser(ctx, user))

	orgUID := fix.org.UID
	pat := models.NewUserToken(user.UID, &orgUID, "pat_contract_value", models.TokenTypePAT)
	refresh := models.NewUserToken(user.UID, nil, "refresh_contract_value", models.TokenTypeRefresh)
	must.NoError(svc.CreateUserToken(ctx, pat))
	must.NoError(svc.CreateUserToken(ctx, refresh))

	byRaw, err := svc.GetUserTokenByToken(ctx, "pat_contract_value")
	must.NoError(err)
	must.Equal(pat.UID, byRaw.UID)

	all, err := svc.ListUserTokens(ctx, user.UID)
	must.NoError(err)
	must.Len(all, 2)

	pats, err := svc.ListUserTokensByType(ctx, user.UID, models.TokenTypePAT)
	must.NoError(err)
	must.Len(pats, 1)

	now := time.Now()
	props := models.JSONMap{"label": "ci"}
	must.NoError(svc.UpdateUserToken(ctx, pat.UID, models.UserTokenUpdate{Properties: &props, LastActiveAt: &now}))

	activity, err := svc.TokenActivityByUsers(ctx, []string{user.UID})
	must.NoError(err)
	must.Contains(activity, user.UID)

	// Revoking everything scoped to the org leaves the global refresh token.
	revoked, err := svc.DeleteUserTokensByOrg(ctx, fix.org.UID)
	must.NoError(err)
	must.Equal(1, revoked)

	_, err = svc.GetUserTokenByToken(ctx, "pat_contract_value")
	must.Error(err, "a revoked token no longer authenticates")

	deleted, err := svc.DeleteUserToken(ctx, refresh.UID)
	must.NoError(err)
	must.True(deleted)

	deleted, err = svc.DeleteUserToken(ctx, refresh.UID)
	must.NoError(err)
	must.False(deleted, "second delete finds no live row")
}

func runPasskeys(t *testing.T, fix *fixture) {
	t.Helper()

	ctx := t.Context()
	must := require.New(t)
	svc := fix.svc

	user := models.NewUser("passkeys@acme.com")
	must.NoError(svc.CreateUser(ctx, user))

	key := models.NewUserPasskey(user.UID, "laptop", []byte("cred-1"), []byte("pub-1"))
	must.NoError(svc.CreateUserPasskey(ctx, key))

	byUID, err := svc.GetUserPasskey(ctx, key.UID)
	must.NoError(err)
	must.Equal("laptop", byUID.Name)

	byCred, err := svc.GetUserPasskeyByCredentialID(ctx, []byte("cred-1"))
	must.NoError(err)
	must.Equal(key.UID, byCred.UID)

	renamed := "phone"
	count := uint32(7)
	used := time.Now()
	backup := true
	must.NoError(svc.UpdateUserPasskey(ctx, key.UID, models.UserPasskeyUpdate{
		Name: &renamed, SignCount: &count, LastUsedAt: &used, BackupState: &backup,
	}))

	list, err := svc.ListUserPasskeysByUser(ctx, user.UID)
	must.NoError(err)
	must.Len(list, 1)
	must.Equal("phone", list[0].Name)
	must.Equal(uint32(7), list[0].SignCount)

	must.NoError(svc.DeleteUserPasskey(ctx, key.UID))

	_, err = svc.GetUserPasskey(ctx, key.UID)
	must.Error(err)

	_, err = svc.GetUserPasskeyByCredentialID(ctx, []byte("cred-1"))
	must.Error(err)
}

func runSystemParameters(t *testing.T, fix *fixture) {
	t.Helper()

	ctx := t.Context()
	must := require.New(t)
	svc := fix.svc

	must.NoError(svc.SetSystemParameter(ctx, "contract.key", "one", false))

	got, err := svc.GetSystemParameter(ctx, "contract.key")
	must.NoError(err)
	must.NotNil(got)

	must.NoError(svc.SetSystemParameter(ctx, "contract.key", "two", true))

	all, err := svc.ListSystemParameters(ctx)
	must.NoError(err)
	must.NotEmpty(all)

	must.NoError(svc.DeleteSystemParameter(ctx, "contract.key"))

	gone, err := svc.GetSystemParameter(ctx, "contract.key")
	must.NoError(err)
	must.Nil(gone, "a deleted parameter reads as absent")
}

// runSuperAdminSource: users.super_admin_source round-trips, clears, and
// CountSuperAdmins counts live super admins only (spec 2026-11-21-02).
func runSuperAdminSource(t *testing.T, fix *fixture) {
	t.Helper()

	ctx := t.Context()
	must := require.New(t)
	svc := fix.svc

	before, err := svc.CountSuperAdmins(ctx)
	must.NoError(err)

	user := models.NewUser("super-source@acme.com")
	must.NoError(svc.CreateUser(ctx, user))

	granted, source := true, models.SuperAdminSourceOIDC
	must.NoError(svc.UpdateUser(ctx, user.UID, &models.UserUpdate{SuperAdmin: &granted, SuperAdminSource: &source}))

	got, err := svc.GetUser(ctx, user.UID)
	must.NoError(err)
	must.True(got.SuperAdmin)
	must.NotNil(got.SuperAdminSource)
	must.Equal(models.SuperAdminSourceOIDC, *got.SuperAdminSource)

	count, err := svc.CountSuperAdmins(ctx)
	must.NoError(err)
	must.Equal(before+1, count)

	revoked := false
	must.NoError(svc.UpdateUser(ctx, user.UID, &models.UserUpdate{SuperAdmin: &revoked, ClearSuperAdminSource: true}))

	got, err = svc.GetUser(ctx, user.UID)
	must.NoError(err)
	must.False(got.SuperAdmin)
	must.Nil(got.SuperAdminSource)

	count, err = svc.CountSuperAdmins(ctx)
	must.NoError(err)
	must.Equal(before, count)
}

// runMemberRoleSource: organization_members.role_source round-trips, and a
// role change naming no source clears it (spec 2026-11-21-02), while a
// same-role update keeps it (security scan 2026-11-22, AU1).
func runMemberRoleSource(t *testing.T, fix *fixture) {
	t.Helper()

	ctx := t.Context()
	must := require.New(t)
	svc := fix.svc

	org := models.NewOrganization("role-source-org", "")
	must.NoError(svc.CreateOrganization(ctx, org))

	user := models.NewUser("role-source@acme.com")
	must.NoError(svc.CreateUser(ctx, user))

	source := models.MemberRoleSourceOIDCGroup
	member := models.NewOrganizationMember(org.UID, user.UID, models.MemberRoleAdmin)
	member.RoleSource = &source
	must.NoError(svc.CreateOrganizationMember(ctx, member))

	got, err := svc.GetMemberByUserAndOrg(ctx, user.UID, org.UID)
	must.NoError(err)
	must.NotNil(got.RoleSource)
	must.Equal(models.MemberRoleSourceOIDCGroup, *got.RoleSource)

	// Re-setting the current role is not a change: the source stays.
	same := models.MemberRoleAdmin
	must.NoError(svc.UpdateOrganizationMember(ctx, member.UID, models.OrganizationMemberUpdate{Role: &same}))

	got, err = svc.GetMemberByUserAndOrg(ctx, user.UID, org.UID)
	must.NoError(err)
	must.Equal(models.MemberRoleAdmin, got.Role)
	must.NotNil(got.RoleSource, "a same-role update keeps the source")
	must.Equal(models.MemberRoleSourceOIDCGroup, *got.RoleSource)

	role := models.MemberRoleUser
	must.NoError(svc.UpdateOrganizationMember(ctx, member.UID, models.OrganizationMemberUpdate{Role: &role}))

	got, err = svc.GetMemberByUserAndOrg(ctx, user.UID, org.UID)
	must.NoError(err)
	must.Equal(models.MemberRoleUser, got.Role)
	must.Nil(got.RoleSource, "a role change naming no source is a local grant")
}

func runEntitlements(t *testing.T, fix *fixture) {
	t.Helper()

	ctx := t.Context()
	must := require.New(t)
	svc := fix.svc

	ent := models.NewOrgEntitlements(fix.org.UID, models.EntitlementSourceAdmin)
	reason := "contract"
	after := models.JSONMap{"source": "admin"}
	audit := models.NewOrgEntitlementAudit(fix.org.UID, "admin", "tester", nil, after, &reason)
	must.NoError(svc.UpsertOrgEntitlements(ctx, ent, audit))

	// A second upsert replaces the row in place.
	second := models.NewOrgEntitlements(fix.org.UID, models.EntitlementSourceBilling)
	ref := "ref-1"
	second.ExternalRef = &ref
	audit2 := models.NewOrgEntitlementAudit(fix.org.UID, "billing-service", "billing", after, after, nil)
	must.NoError(svc.UpsertOrgEntitlements(ctx, second, audit2))

	got, err := svc.GetOrgEntitlements(ctx, fix.org.UID)
	must.NoError(err)
	must.NotNil(got.ExternalRef)
	must.Equal("ref-1", *got.ExternalRef)
	must.Equal(models.EntitlementSourceBilling, got.Payload.Source)

	extra := models.NewOrgEntitlementAudit(fix.org.UID, "billing-service", "billing", after, after, nil)
	must.NoError(svc.CreateOrgEntitlementAudit(ctx, extra))

	audits, err := svc.ListOrgEntitlementAudits(ctx, models.ListOrgEntitlementAuditsFilter{
		OrganizationUID: fix.org.UID, Limit: 10,
	})
	must.NoError(err)
	must.Len(audits, 3)

	delAudit := models.NewOrgEntitlementAudit(fix.org.UID, "admin", "tester", after, after, nil)
	must.NoError(svc.DeleteOrgEntitlements(ctx, fix.org.UID, delAudit))

	gone, err := svc.GetOrgEntitlements(ctx, fix.org.UID)
	if err == nil {
		must.Nil(gone)
	}
}
