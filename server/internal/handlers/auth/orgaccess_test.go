package auth_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/handlers/auth"
)

// TestAuthorizeOrgAccess_TwoOrgMemberMatrix is the regression test the
// cross-tenant leak (spec 2026-07-15-01) needed and never had. It drives the
// SINGLE shared authorizer that BOTH surfaces call — middleware.RequireOrgAccess
// (REST) and realtimews.authorizeOrg (the live WS) — so a green matrix here is
// the contract that REST and the WS agree for every (token-org × target-org)
// pair, including the negative case (a non-member is refused by both).
//
// The user is a genuine NON-super-admin member of two orgs, which is the exact
// shape that reproduces the bug: a super-admin short-circuits every check
// (claims.IsSuperAdmin()), which is why the seeded admin accounts hide it.
func TestAuthorizeOrgAccess_TwoOrgMemberMatrix(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	r := require.New(t)

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	orgDefault := models.NewOrganization("default", "Default Org")
	orgTest := models.NewOrganization("test", "Test Org")
	r.NoError(dbSvc.CreateOrganization(ctx, orgDefault))
	r.NoError(dbSvc.CreateOrganization(ctx, orgTest))

	// dual is a member of BOTH orgs, non-super-admin — the multi-org user whose
	// token is minted for one org while they browse the other.
	dual := models.NewUser("dual@example.com")
	r.NoError(dbSvc.CreateUser(ctx, dual))
	r.NoError(dbSvc.CreateOrganizationMember(ctx, models.NewOrganizationMember(orgDefault.UID, dual.UID, models.MemberRoleAdmin)))
	r.NoError(dbSvc.CreateOrganizationMember(ctx, models.NewOrganizationMember(orgTest.UID, dual.UID, models.MemberRoleUser)))

	// stranger is a member of neither org.
	stranger := models.NewUser("stranger@example.com")
	r.NoError(dbSvc.CreateUser(ctx, stranger))

	super := models.NewUser("super@example.com")
	super.SuperAdmin = true
	r.NoError(dbSvc.CreateUser(ctx, super))

	claims := func(user *models.User, tokenOrg, role string) *auth.Claims {
		return &auth.Claims{UserUID: user.UID, OrgSlug: tokenOrg, Role: role}
	}

	tests := []struct {
		name     string
		user     *models.User
		tokenOrg string
		role     string
		target   string
		want     auth.OrgAccessDenial
	}{
		// Member, token scoped to the org being accessed -> granted.
		{"dual token=default target=default", dual, "default", "admin", "default", auth.OrgAccessGranted},
		{"dual token=test target=test", dual, "test", "user", "test", auth.OrgAccessGranted},
		// Member, but token scoped to the OTHER org -> denied (token-scope rule),
		// even though the user is a genuine member of the target. This is the
		// exact cross-org navigation case from the repro.
		{"dual token=test target=default", dual, "test", "user", "default", auth.OrgAccessDenied},
		{"dual token=default target=test", dual, "default", "admin", "test", auth.OrgAccessDenied},
		// Non-member, token scoped to the target it is probing -> denied by the
		// membership check. The negative case that must be refused everywhere.
		{"stranger token=default target=default", stranger, "default", "user", "default", auth.OrgAccessDenied},
		{"stranger token=test target=test", stranger, "test", "user", "test", auth.OrgAccessDenied},
		// Token scoped to a non-existent org, accessing it -> not-found (the
		// scope check passes, then the load fails).
		{"dual token=missing target=missing", dual, "missing", "user", "missing", auth.OrgAccessOrgNotFound},
		// Scope mismatch to a non-existent target is denied BEFORE the load, so
		// existence never leaks.
		{"dual token=default target=missing", dual, "default", "admin", "missing", auth.OrgAccessDenied},
		// Super-admin crosses orgs freely; a missing org still resolves to
		// not-found for them.
		{"super target=default", super, "default", auth.RoleSuperAdmin, "default", auth.OrgAccessGranted},
		{"super token=test target=default", super, "test", auth.RoleSuperAdmin, "default", auth.OrgAccessGranted},
		{"super target=missing", super, "default", auth.RoleSuperAdmin, "missing", auth.OrgAccessOrgNotFound},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			rr := require.New(t)

			org, denial := auth.AuthorizeOrgAccess(
				t.Context(), dbSvc, claims(tc.user, tc.tokenOrg, tc.role), tc.user, tc.target)

			rr.Equal(tc.want, denial)
			if tc.want == auth.OrgAccessGranted {
				rr.NotNil(org)
				rr.Equal(tc.target, org.Slug)
			} else {
				rr.Nil(org)
			}
		})
	}
}
