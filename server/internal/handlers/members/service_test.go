package members

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
)

func TestValidateRole(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		role    string
		isValid bool
	}{
		{
			name:    "valid owner role",
			role:    "owner",
			isValid: true,
		},
		{
			name:    "valid admin role",
			role:    "admin",
			isValid: true,
		},
		{
			name:    "valid user role",
			role:    "user",
			isValid: true,
		},
		{
			name:    "valid viewer role",
			role:    "viewer",
			isValid: true,
		},
		{
			name:    "invalid role",
			role:    "superuser",
			isValid: false,
		},
		{
			name:    "empty role",
			role:    "",
			isValid: false,
		},
		{
			name:    "uppercase role",
			role:    "ADMIN",
			isValid: false,
		},
		{
			name:    "mixed case role",
			role:    "Admin",
			isValid: false,
		},
		{
			name:    "role with spaces",
			role:    " admin ",
			isValid: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			r := require.New(t)

			// Exercise the production predicate, not a copy of it — a
			// hand-rolled duplicate silently stops tracking the real role set
			// (it did exactly that when `owner` was introduced).
			r.Equal(tc.isValid, isValidRole(models.MemberRole(tc.role)))
		})
	}
}

func TestMemberResponseFields(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	// Test that MemberResponse struct has expected fields
	resp := &MemberResponse{
		UID:       "test-uid",
		UserUID:   "user-uid",
		Email:     "test@example.com",
		Name:      "Test User",
		AvatarURL: "https://example.com/avatar.png",
		Role:      "admin",
		JoinedAt:  nil,
	}

	r.Equal("test-uid", resp.UID)
	r.Equal("user-uid", resp.UserUID)
	r.Equal("test@example.com", resp.Email)
	r.Equal("Test User", resp.Name)
	r.Equal("https://example.com/avatar.png", resp.AvatarURL)
	r.Equal("admin", resp.Role)
	r.Nil(resp.JoinedAt)
}

func TestAddMemberRequestFields(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	req := AddMemberRequest{
		Email: "new@example.com",
		Role:  "user",
	}

	r.Equal("new@example.com", req.Email)
	r.Equal("user", req.Role)
}

func TestUpdateMemberRequestFields(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	role := "viewer"
	req := UpdateMemberRequest{
		Role: &role,
	}

	r.NotNil(req.Role)
	r.Equal("viewer", *req.Role)

	// Test with nil role
	reqNil := UpdateMemberRequest{}
	r.Nil(reqNil.Role)
}

func TestListMembersResponseFields(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	resp := &ListMembersResponse{
		Data: []*MemberResponse{
			{UID: "member-1", Email: "member1@example.com"},
			{UID: "member-2", Email: "member2@example.com"},
		},
	}

	r.Len(resp.Data, 2)
	r.Equal("member-1", resp.Data[0].UID)
	r.Equal("member-2", resp.Data[1].UID)
}

func TestErrorVariables(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	// Verify error messages are meaningful
	r.Contains(ErrOrganizationNotFound.Error(), "organization")
	r.Contains(ErrMemberNotFound.Error(), "member")
	r.Contains(ErrUserNotFound.Error(), "user")
	r.Contains(ErrAlreadyMember.Error(), "already")
	r.Contains(ErrCannotRemoveLastAdmin.Error(), "last admin")
	r.Contains(ErrCannotDemoteLastAdmin.Error(), "last admin")
	r.Contains(ErrInvalidRole.Error(), "role")
}

// seedClaimAdmin seeds an org with an owner and an admin whose role came from
// the OIDC org-admin group claim (role_source = 'oidc_group').
func seedClaimAdmin(
	ctx context.Context, t *testing.T, dbService db.Service,
) (*models.Organization, *models.OrganizationMember, *models.OrganizationMember) {
	t.Helper()

	org, members := seedOrg(ctx, t, dbService, "claim-org", map[string]models.MemberRole{
		"owner@acme.com": models.MemberRoleOwner,
	})

	user := models.NewUser("claim-admin@acme.com")
	require.NoError(t, dbService.CreateUser(ctx, user))

	source := models.MemberRoleSourceOIDCGroup
	admin := models.NewOrganizationMember(org.UID, user.UID, models.MemberRoleAdmin)
	admin.RoleSource = &source
	require.NoError(t, dbService.CreateOrganizationMember(ctx, admin))

	return org, members["owner@acme.com"], admin
}

func roleChangedEvents(
	ctx context.Context, t *testing.T, dbService db.Service, orgUID string,
) []*models.Event {
	t.Helper()

	var events []*models.Event

	require.NoError(t, dbService.DB().NewSelect().Model(&events).
		Where("organization_uid = ?", orgUID).
		Where("event_type = ?", string(models.EventTypeMemberRoleChanged)).
		Scan(ctx))

	return events
}

// A claim admin re-sending their own role must not launder the claim grant
// into a local one (security scan 2026-11-22, AU1): the PATCH is a no-op that
// keeps role_source, so leaving the IdP group still demotes them.
func TestUpdateMemberSameRoleKeepsClaimSource(t *testing.T) {
	t.Parallel()

	svc, dbService, ctx := setupMembersTest(t)
	org, _, admin := seedClaimAdmin(ctx, t, dbService)

	role := string(models.MemberRoleAdmin)
	resp, err := svc.UpdateMember(ctx, org.Slug, admin.UID, UpdateMemberRequest{Role: &role}, callerFor(admin))
	require.NoError(t, err)
	require.Equal(t, role, resp.Role)

	got, err := dbService.GetOrganizationMember(ctx, admin.UID)
	require.NoError(t, err)
	require.Equal(t, models.MemberRoleAdmin, got.Role)
	require.NotNil(t, got.RoleSource, "a same-role PATCH keeps the claim source")
	require.Equal(t, models.MemberRoleSourceOIDCGroup, *got.RoleSource)
	require.Empty(t, roleChangedEvents(ctx, t, dbService, org.UID), "a no-op PATCH records nothing")
}

// Positive control: a real change by the owner takes the role over. Down to
// user and back to admin leaves a local grant, and the change clearing the
// claim source is audited with that previous source.
func TestUpdateMemberRealChangeTakesOverClaimGrant(t *testing.T) {
	t.Parallel()

	svc, dbService, ctx := setupMembersTest(t)
	org, owner, admin := seedClaimAdmin(ctx, t, dbService)

	userRole := string(models.MemberRoleUser)
	_, err := svc.UpdateMember(ctx, org.Slug, admin.UID, UpdateMemberRequest{Role: &userRole}, callerFor(owner))
	require.NoError(t, err)

	events := roleChangedEvents(ctx, t, dbService, org.UID)
	require.Len(t, events, 1)
	require.Equal(t, models.MemberRoleSourceOIDCGroup, events[0].Payload["previous_role_source"])
	require.Equal(t, string(models.MemberRoleAdmin), events[0].Payload["previous_role"])

	adminRole := string(models.MemberRoleAdmin)
	_, err = svc.UpdateMember(ctx, org.Slug, admin.UID, UpdateMemberRequest{Role: &adminRole}, callerFor(owner))
	require.NoError(t, err)

	got, err := dbService.GetOrganizationMember(ctx, admin.UID)
	require.NoError(t, err)
	require.Equal(t, models.MemberRoleAdmin, got.Role)
	require.Nil(t, got.RoleSource, "a real role change naming no source is a local grant")

	events = roleChangedEvents(ctx, t, dbService, org.UID)
	require.Len(t, events, 2)

	for _, ev := range events {
		if ev.Payload["role"] == string(models.MemberRoleAdmin) {
			_, hasSource := ev.Payload["previous_role_source"]
			require.False(t, hasSource, "the local user role had no source")
		}
	}
}
