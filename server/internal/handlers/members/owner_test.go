package members

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
)

// setupMembersTest builds a members service over an in-memory SQLite database.
func setupMembersTest(t *testing.T) (*Service, db.Service, context.Context) {
	t.Helper()

	ctx := t.Context()

	dbService, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	require.NoError(t, err)
	require.NoError(t, dbService.Initialize(ctx))

	t.Cleanup(func() { _ = dbService.Close() })

	return NewService(dbService), dbService, ctx
}

// seedOrg creates an organization with a single member per (email, role) pair
// and returns the org plus a lookup of email → membership.
func seedOrg(
	ctx context.Context, t *testing.T, dbService db.Service, slug string, roles map[string]models.MemberRole,
) (*models.Organization, map[string]*models.OrganizationMember) {
	t.Helper()

	org := models.NewOrganization(slug, slug)
	require.NoError(t, dbService.CreateOrganization(ctx, org))

	members := make(map[string]*models.OrganizationMember, len(roles))

	for email, role := range roles {
		user := models.NewUser(email)
		require.NoError(t, dbService.CreateUser(ctx, user))

		member := models.NewOrganizationMember(org.UID, user.UID, role)
		now := time.Now()
		member.JoinedAt = &now
		require.NoError(t, dbService.CreateOrganizationMember(ctx, member))

		members[email] = member
	}

	return org, members
}

// callerFor resolves the Caller struct for a seeded member.
func callerFor(member *models.OrganizationMember) Caller {
	return Caller{UserUID: member.UserUID}
}

// TestMemberRoleHierarchy pins the ordering owner > admin > user > viewer and
// the two ways it can go wrong: an owner failing an admin gate, or an unknown
// role passing one.
func TestMemberRoleHierarchy(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		role    models.MemberRole
		minRole models.MemberRole
		want    bool
	}{
		{"owner passes the owner gate", models.MemberRoleOwner, models.MemberRoleOwner, true},
		{"owner passes the admin gate", models.MemberRoleOwner, models.MemberRoleAdmin, true},
		{"owner passes the user gate", models.MemberRoleOwner, models.MemberRoleUser, true},
		{"owner passes the viewer gate", models.MemberRoleOwner, models.MemberRoleViewer, true},
		{"admin fails the owner gate", models.MemberRoleAdmin, models.MemberRoleOwner, false},
		{"admin passes the admin gate", models.MemberRoleAdmin, models.MemberRoleAdmin, true},
		{"user fails the admin gate", models.MemberRoleUser, models.MemberRoleAdmin, false},
		{"user passes the viewer gate", models.MemberRoleUser, models.MemberRoleViewer, true},
		{"viewer fails the user gate", models.MemberRoleViewer, models.MemberRoleUser, false},
		{"unknown role fails the viewer gate", models.MemberRole("root"), models.MemberRoleViewer, false},
		{"empty role fails the viewer gate", models.MemberRole(""), models.MemberRoleViewer, false},
		{"valid role never satisfies an unknown gate", models.MemberRoleOwner, models.MemberRole("god"), false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, tc.want, tc.role.AtLeast(tc.minRole))
		})
	}
}

// TestMemberRoleIsValid pins which strings are member roles at all.
func TestMemberRoleIsValid(t *testing.T) {
	t.Parallel()

	tests := []struct {
		role models.MemberRole
		want bool
	}{
		{models.MemberRoleOwner, true},
		{models.MemberRoleAdmin, true},
		{models.MemberRoleUser, true},
		{models.MemberRoleViewer, true},
		{models.MemberRole("Owner"), false},
		{models.MemberRole("superadmin"), false},
		{models.MemberRole(""), false},
	}

	for _, tc := range tests {
		t.Run(string(tc.role), func(t *testing.T) {
			t.Parallel()

			require.Equal(t, tc.want, tc.role.IsValid())
			require.Equal(t, tc.want, isValidRole(tc.role))
		})
	}
}

// TestUpdateMemberOwnerGuards covers the whole owner-management matrix in one
// table: who may grant ownership, who may touch an owner, and what happens to
// the last owner. Each negative is paired with a positive control so a blanket
// "everything is refused" bug cannot pass.
func TestUpdateMemberOwnerGuards(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		// seeded org membership
		roles map[string]models.MemberRole
		// who performs the change and on whom
		caller string
		target string
		// the requested new role
		newRole models.MemberRole
		wantErr error
	}{
		{
			name: "owner promotes an admin to owner",
			roles: map[string]models.MemberRole{
				"owner@x.example": models.MemberRoleOwner,
				"admin@x.example": models.MemberRoleAdmin,
			},
			caller:  "owner@x.example",
			target:  "admin@x.example",
			newRole: models.MemberRoleOwner,
		},
		{
			name: "admin cannot promote anybody to owner",
			roles: map[string]models.MemberRole{
				"owner@x.example": models.MemberRoleOwner,
				"admin@x.example": models.MemberRoleAdmin,
				"user@x.example":  models.MemberRoleUser,
			},
			caller:  "admin@x.example",
			target:  "user@x.example",
			newRole: models.MemberRoleOwner,
			wantErr: ErrOwnerRoleRequired,
		},
		{
			// The takeover attempt: without this guard any admin could demote
			// the owner and seize the org.
			name: "admin cannot demote the owner",
			roles: map[string]models.MemberRole{
				"owner@x.example":  models.MemberRoleOwner,
				"owner2@x.example": models.MemberRoleOwner,
				"admin@x.example":  models.MemberRoleAdmin,
			},
			caller:  "admin@x.example",
			target:  "owner@x.example",
			newRole: models.MemberRoleUser,
			wantErr: ErrOwnerRoleRequired,
		},
		{
			// Positive control for the case above: the same demotion is fine
			// when an owner performs it and another owner remains.
			name: "owner demotes a co-owner when another owner remains",
			roles: map[string]models.MemberRole{
				"owner@x.example":  models.MemberRoleOwner,
				"owner2@x.example": models.MemberRoleOwner,
			},
			caller:  "owner@x.example",
			target:  "owner2@x.example",
			newRole: models.MemberRoleAdmin,
		},
		{
			name: "the last owner cannot be demoted",
			roles: map[string]models.MemberRole{
				"owner@x.example": models.MemberRoleOwner,
				"admin@x.example": models.MemberRoleAdmin,
			},
			caller:  "owner@x.example",
			target:  "owner@x.example",
			newRole: models.MemberRoleAdmin,
			wantErr: ErrCannotDemoteLastOwner,
		},
		{
			name: "an admin may still be demoted while an owner remains",
			roles: map[string]models.MemberRole{
				"owner@x.example": models.MemberRoleOwner,
				"admin@x.example": models.MemberRoleAdmin,
			},
			caller:  "owner@x.example",
			target:  "admin@x.example",
			newRole: models.MemberRoleUser,
		},
		{
			name: "a plain admin can still change a plain user's role",
			roles: map[string]models.MemberRole{
				"owner@x.example": models.MemberRoleOwner,
				"admin@x.example": models.MemberRoleAdmin,
				"user@x.example":  models.MemberRoleUser,
			},
			caller:  "admin@x.example",
			target:  "user@x.example",
			newRole: models.MemberRoleViewer,
		},
		{
			name: "an unknown role is rejected",
			roles: map[string]models.MemberRole{
				"owner@x.example": models.MemberRoleOwner,
				"user@x.example":  models.MemberRoleUser,
			},
			caller:  "owner@x.example",
			target:  "user@x.example",
			newRole: models.MemberRole("root"),
			wantErr: ErrInvalidRole,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			svc, dbService, ctx := setupMembersTest(t)
			org, members := seedOrg(ctx, t, dbService, "guards", tc.roles)

			newRole := string(tc.newRole)
			resp, err := svc.UpdateMember(ctx, org.Slug, members[tc.target].UID,
				UpdateMemberRequest{Role: &newRole}, callerFor(members[tc.caller]))

			if tc.wantErr != nil {
				require.ErrorIs(t, err, tc.wantErr)

				// The refused change must not have landed.
				unchanged, getErr := dbService.GetOrganizationMember(ctx, members[tc.target].UID)
				require.NoError(t, getErr)
				require.Equal(t, tc.roles[tc.target], unchanged.Role)

				return
			}

			require.NoError(t, err)
			require.Equal(t, string(tc.newRole), resp.Role)
		})
	}
}

// TestRemoveMemberOwnerGuards mirrors TestUpdateMemberOwnerGuards for removal:
// an admin cannot evict an owner, and the last owner cannot be removed at all.
func TestRemoveMemberOwnerGuards(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		roles   map[string]models.MemberRole
		caller  string
		target  string
		wantErr error
	}{
		{
			name: "admin cannot remove an owner",
			roles: map[string]models.MemberRole{
				"owner@x.example":  models.MemberRoleOwner,
				"owner2@x.example": models.MemberRoleOwner,
				"admin@x.example":  models.MemberRoleAdmin,
			},
			caller:  "admin@x.example",
			target:  "owner@x.example",
			wantErr: ErrOwnerRoleRequired,
		},
		{
			name: "owner removes a co-owner when another owner remains",
			roles: map[string]models.MemberRole{
				"owner@x.example":  models.MemberRoleOwner,
				"owner2@x.example": models.MemberRoleOwner,
			},
			caller: "owner@x.example",
			target: "owner2@x.example",
		},
		{
			name: "the last owner cannot remove themselves",
			roles: map[string]models.MemberRole{
				"owner@x.example": models.MemberRoleOwner,
				"admin@x.example": models.MemberRoleAdmin,
			},
			caller:  "owner@x.example",
			target:  "owner@x.example",
			wantErr: ErrCannotRemoveLastOwner,
		},
		{
			name: "an admin can still be removed while an owner remains",
			roles: map[string]models.MemberRole{
				"owner@x.example": models.MemberRoleOwner,
				"admin@x.example": models.MemberRoleAdmin,
			},
			caller: "owner@x.example",
			target: "admin@x.example",
		},
		{
			// The last-admin guard counts owners as admins, so removing the
			// sole owner-as-admin of an org with no other privileged member is
			// stopped by the owner guard first (asserted above); here the org
			// has an owner AND an admin, so removing the admin is fine — the
			// pre-owner behavior would have blocked it if owners did not count.
			name: "removing the only plain admin is allowed because the owner counts",
			roles: map[string]models.MemberRole{
				"owner@x.example": models.MemberRoleOwner,
				"admin@x.example": models.MemberRoleAdmin,
				"user@x.example":  models.MemberRoleUser,
			},
			caller: "owner@x.example",
			target: "admin@x.example",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			svc, dbService, ctx := setupMembersTest(t)
			org, members := seedOrg(ctx, t, dbService, "removal", tc.roles)

			err := svc.RemoveMember(ctx, org.Slug, members[tc.target].UID, callerFor(members[tc.caller]))

			if tc.wantErr != nil {
				require.ErrorIs(t, err, tc.wantErr)

				// The member must still be there.
				still, getErr := dbService.GetOrganizationMember(ctx, members[tc.target].UID)
				require.NoError(t, getErr)
				require.Nil(t, still.DeletedAt)

				return
			}

			require.NoError(t, err)
		})
	}
}

// TestAddMemberOwnerGrant pins that minting a brand-new owner is owner-only.
func TestAddMemberOwnerGrant(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		caller  string
		role    models.MemberRole
		wantErr error
	}{
		{name: "owner adds an owner", caller: "owner@x.example", role: models.MemberRoleOwner},
		{
			name: "admin cannot add an owner", caller: "admin@x.example",
			role: models.MemberRoleOwner, wantErr: ErrOwnerRoleRequired,
		},
		{name: "admin can still add an admin", caller: "admin@x.example", role: models.MemberRoleAdmin},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			svc, dbService, ctx := setupMembersTest(t)
			org, members := seedOrg(ctx, t, dbService, "adding", map[string]models.MemberRole{
				"owner@x.example": models.MemberRoleOwner,
				"admin@x.example": models.MemberRoleAdmin,
			})

			newcomer := models.NewUser("newcomer@x.example")
			require.NoError(t, dbService.CreateUser(ctx, newcomer))

			resp, err := svc.AddMember(ctx, org.Slug,
				AddMemberRequest{Email: newcomer.Email, Role: string(tc.role)},
				nil, callerFor(members[tc.caller]))

			if tc.wantErr != nil {
				require.ErrorIs(t, err, tc.wantErr)

				_, getErr := dbService.GetMemberByUserAndOrg(ctx, newcomer.UID, org.UID)
				require.Error(t, getErr, "the refused member must not exist")

				return
			}

			require.NoError(t, err)
			require.Equal(t, string(tc.role), resp.Role)
		})
	}
}

// TestSuperAdminManagesOwners pins that a super admin outranks the owner gate —
// they hold no membership row at all, so a naive membership lookup would give
// them rank 0 and lock them out of owner management.
func TestSuperAdminManagesOwners(t *testing.T) {
	t.Parallel()

	svc, dbService, ctx := setupMembersTest(t)
	org, members := seedOrg(ctx, t, dbService, "superadmin", map[string]models.MemberRole{
		"owner@x.example":  models.MemberRoleOwner,
		"owner2@x.example": models.MemberRoleOwner,
	})

	newRole := string(models.MemberRoleAdmin)
	resp, err := svc.UpdateMember(ctx, org.Slug, members["owner2@x.example"].UID,
		UpdateMemberRequest{Role: &newRole}, Caller{UserUID: "not-a-member", SuperAdmin: true})
	require.NoError(t, err)
	require.Equal(t, string(models.MemberRoleAdmin), resp.Role)
}

// TestOwnershipTransferPath walks the documented hand-over: promote a second
// owner, then demote yourself. Each half is refused in isolation, so this is
// the only way out of an org you own.
func TestOwnershipTransferPath(t *testing.T) {
	t.Parallel()

	svc, dbService, ctx := setupMembersTest(t)
	org, members := seedOrg(ctx, t, dbService, "transfer", map[string]models.MemberRole{
		"founder@x.example":  models.MemberRoleOwner,
		"newowner@x.example": models.MemberRoleAdmin,
	})

	founder := callerFor(members["founder@x.example"])

	// Step 0: the founder cannot simply demote themselves — they are the last owner.
	admin := string(models.MemberRoleAdmin)
	_, err := svc.UpdateMember(ctx, org.Slug, members["founder@x.example"].UID,
		UpdateMemberRequest{Role: &admin}, founder)
	require.ErrorIs(t, err, ErrCannotDemoteLastOwner)

	// Step 1: promote the successor.
	owner := string(models.MemberRoleOwner)
	_, err = svc.UpdateMember(ctx, org.Slug, members["newowner@x.example"].UID,
		UpdateMemberRequest{Role: &owner}, founder)
	require.NoError(t, err)

	// Step 2: now the founder may step down.
	_, err = svc.UpdateMember(ctx, org.Slug, members["founder@x.example"].UID,
		UpdateMemberRequest{Role: &admin}, founder)
	require.NoError(t, err)

	// And having stepped down, they can no longer manage the remaining owner.
	_, err = svc.UpdateMember(ctx, org.Slug, members["newowner@x.example"].UID,
		UpdateMemberRequest{Role: &admin}, founder)
	require.ErrorIs(t, err, ErrOwnerRoleRequired)
}

// TestCallerRoleIsReadFromTheDatabase pins that the caller's privileges come
// from the live membership row, not from anything the caller supplies: a
// demoted owner immediately loses owner powers.
func TestCallerRoleIsReadFromTheDatabase(t *testing.T) {
	t.Parallel()

	svc, dbService, ctx := setupMembersTest(t)
	org, members := seedOrg(ctx, t, dbService, "livecheck", map[string]models.MemberRole{
		"owner@x.example":  models.MemberRoleOwner,
		"owner2@x.example": models.MemberRoleOwner,
		"user@x.example":   models.MemberRoleUser,
	})

	demoted := callerFor(members["owner2@x.example"])

	// Control: while still an owner, they can grant ownership.
	owner := string(models.MemberRoleOwner)
	_, err := svc.UpdateMember(ctx, org.Slug, members["user@x.example"].UID,
		UpdateMemberRequest{Role: &owner}, demoted)
	require.NoError(t, err)

	// Demote them, then retry — the same Caller value must now be refused.
	viewer := models.MemberRoleUser
	require.NoError(t, dbService.UpdateOrganizationMember(ctx, members["owner2@x.example"].UID,
		models.OrganizationMemberUpdate{Role: &viewer}))

	admin := string(models.MemberRoleAdmin)
	_, err = svc.UpdateMember(ctx, org.Slug, members["user@x.example"].UID,
		UpdateMemberRequest{Role: &admin}, demoted)
	require.ErrorIs(t, err, ErrOwnerRoleRequired)
}

// TestNonMemberCallerCannotManageOwners pins that an identity with no
// membership row (and no super-admin flag) has no role at all.
func TestNonMemberCallerCannotManageOwners(t *testing.T) {
	t.Parallel()

	svc, dbService, ctx := setupMembersTest(t)
	org, members := seedOrg(ctx, t, dbService, "stranger", map[string]models.MemberRole{
		"owner@x.example":  models.MemberRoleOwner,
		"owner2@x.example": models.MemberRoleOwner,
	})

	admin := string(models.MemberRoleAdmin)
	_, err := svc.UpdateMember(ctx, org.Slug, members["owner2@x.example"].UID,
		UpdateMemberRequest{Role: &admin}, Caller{UserUID: "stranger-uid"})
	require.ErrorIs(t, err, ErrOwnerRoleRequired)

	_, err = svc.UpdateMember(ctx, org.Slug, members["owner2@x.example"].UID,
		UpdateMemberRequest{Role: &admin}, Caller{})
	require.ErrorIs(t, err, ErrOwnerRoleRequired)
}
