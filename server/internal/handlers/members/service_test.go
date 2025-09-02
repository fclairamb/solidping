package members

import (
	"testing"

	"github.com/stretchr/testify/require"

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
			role := models.MemberRole(tc.role)
			isValid := role == models.MemberRoleAdmin ||
				role == models.MemberRoleUser ||
				role == models.MemberRoleViewer

			r.Equal(tc.isValid, isValid)
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
