// Package members provides HTTP handlers for organization member management.
package members

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
)

var (
	// ErrOrganizationNotFound is returned when an organization is not found.
	ErrOrganizationNotFound = errors.New("organization not found")
	// ErrMemberNotFound is returned when a member is not found.
	ErrMemberNotFound = errors.New("member not found")
	// ErrUserNotFound is returned when a user is not found.
	ErrUserNotFound = errors.New("user not found")
	// ErrAlreadyMember is returned when a user is already a member of the organization.
	ErrAlreadyMember = errors.New("user is already a member")
	// ErrCannotRemoveLastAdmin is returned when trying to remove the last admin.
	ErrCannotRemoveLastAdmin = errors.New("cannot remove the last admin from the organization")
	// ErrCannotDemoteLastAdmin is returned when trying to demote the last admin.
	ErrCannotDemoteLastAdmin = errors.New("cannot demote the last admin")
	// ErrInvalidRole is returned when an invalid role is provided.
	ErrInvalidRole = errors.New("invalid role")
)

// Service provides business logic for member management.
type Service struct {
	db db.Service
}

// NewService creates a new members service.
func NewService(dbService db.Service) *Service {
	return &Service{
		db: dbService,
	}
}

// MemberResponse represents a member in API responses.
type MemberResponse struct {
	UID       string     `json:"uid"`
	UserUID   string     `json:"userUid"`
	Email     string     `json:"email"`
	Name      string     `json:"name,omitempty"`
	AvatarURL string     `json:"avatarUrl,omitempty"`
	Role      string     `json:"role"`
	JoinedAt  *time.Time `json:"joinedAt,omitempty"`
	CreatedAt time.Time  `json:"createdAt"`
}

// ListMembersResponse represents the response for listing members.
type ListMembersResponse struct {
	Data []*MemberResponse `json:"data"`
}

// AddMemberRequest represents the request to add a member.
type AddMemberRequest struct {
	Email string `json:"email"`
	Role  string `json:"role"`
}

// UpdateMemberRequest represents the request to update a member.
type UpdateMemberRequest struct {
	Role *string `json:"role,omitempty"`
}

// ListMembers returns all members of an organization.
func (s *Service) ListMembers(ctx context.Context, orgSlug string) (*ListMembersResponse, error) {
	org, err := s.db.GetOrganizationBySlug(ctx, orgSlug)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrOrganizationNotFound
		}

		return nil, err
	}

	members, err := s.db.ListMembersByOrg(ctx, org.UID)
	if err != nil {
		return nil, err
	}

	response := &ListMembersResponse{
		Data: make([]*MemberResponse, 0, len(members)),
	}

	for _, member := range members {
		user, userErr := s.db.GetUser(ctx, member.UserUID)
		if userErr != nil {
			continue // Skip members with missing users
		}

		response.Data = append(response.Data, &MemberResponse{
			UID:       member.UID,
			UserUID:   member.UserUID,
			Email:     user.Email,
			Name:      user.Name,
			AvatarURL: user.AvatarURL,
			Role:      string(member.Role),
			JoinedAt:  member.JoinedAt,
			CreatedAt: member.CreatedAt,
		})
	}

	return response, nil
}

// GetMember returns a specific member by UID.
func (s *Service) GetMember(ctx context.Context, orgSlug, memberUID string) (*MemberResponse, error) {
	org, err := s.db.GetOrganizationBySlug(ctx, orgSlug)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrOrganizationNotFound
		}

		return nil, err
	}

	member, err := s.db.GetOrganizationMember(ctx, memberUID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrMemberNotFound
		}

		return nil, err
	}

	// Verify member belongs to this organization
	if member.OrganizationUID != org.UID {
		return nil, ErrMemberNotFound
	}

	user, err := s.db.GetUser(ctx, member.UserUID)
	if err != nil {
		return nil, ErrUserNotFound
	}

	return &MemberResponse{
		UID:       member.UID,
		UserUID:   member.UserUID,
		Email:     user.Email,
		Name:      user.Name,
		AvatarURL: user.AvatarURL,
		Role:      string(member.Role),
		JoinedAt:  member.JoinedAt,
		CreatedAt: member.CreatedAt,
	}, nil
}

// AddMember adds a user to an organization by email.
func (s *Service) AddMember(
	ctx context.Context, orgSlug string, req AddMemberRequest, inviterUID *string,
) (*MemberResponse, error) {
	org, err := s.db.GetOrganizationBySlug(ctx, orgSlug)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrOrganizationNotFound
		}

		return nil, err
	}

	// Validate role
	role := models.MemberRole(req.Role)
	if role != models.MemberRoleAdmin && role != models.MemberRoleUser && role != models.MemberRoleViewer {
		return nil, ErrInvalidRole
	}

	// Find user by email
	user, err := s.db.GetUserByEmail(ctx, req.Email)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrUserNotFound
		}

		return nil, err
	}

	// Check if already a member
	existingMember, err := s.db.GetMemberByUserAndOrg(ctx, user.UID, org.UID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}

	if existingMember != nil {
		return nil, ErrAlreadyMember
	}

	// Create membership
	now := time.Now()
	member := models.NewOrganizationMember(org.UID, user.UID, role)
	member.InvitedByUID = inviterUID
	member.InvitedAt = &now

	if err := s.db.CreateOrganizationMember(ctx, member); err != nil {
		return nil, err
	}

	return &MemberResponse{
		UID:       member.UID,
		UserUID:   member.UserUID,
		Email:     user.Email,
		Name:      user.Name,
		AvatarURL: user.AvatarURL,
		Role:      string(member.Role),
		JoinedAt:  member.JoinedAt,
		CreatedAt: member.CreatedAt,
	}, nil
}

// UpdateMember updates a member's role.
func (s *Service) UpdateMember(
	ctx context.Context, orgSlug, memberUID string, req UpdateMemberRequest,
) (*MemberResponse, error) {
	org, err := s.db.GetOrganizationBySlug(ctx, orgSlug)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrOrganizationNotFound
		}

		return nil, err
	}

	member, err := s.db.GetOrganizationMember(ctx, memberUID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrMemberNotFound
		}

		return nil, err
	}

	// Verify member belongs to this organization
	if member.OrganizationUID != org.UID {
		return nil, ErrMemberNotFound
	}

	// Prepare update
	update := models.OrganizationMemberUpdate{}

	if req.Role != nil {
		newRole := models.MemberRole(*req.Role)
		if !isValidRole(newRole) {
			return nil, ErrInvalidRole
		}

		// Check if demoting an admin
		if isDemotingAdmin(member.Role, newRole) {
			if lastAdminErr := s.checkLastAdmin(ctx, org.UID); lastAdminErr != nil {
				return nil, lastAdminErr
			}
		}

		update.Role = &newRole
	}

	updateErr := s.db.UpdateOrganizationMember(ctx, memberUID, update)
	if updateErr != nil {
		return nil, updateErr
	}

	// Reload member and user
	member, err = s.db.GetOrganizationMember(ctx, memberUID)
	if err != nil {
		return nil, err
	}

	user, err := s.db.GetUser(ctx, member.UserUID)
	if err != nil {
		return nil, ErrUserNotFound
	}

	return &MemberResponse{
		UID:       member.UID,
		UserUID:   member.UserUID,
		Email:     user.Email,
		Name:      user.Name,
		AvatarURL: user.AvatarURL,
		Role:      string(member.Role),
		JoinedAt:  member.JoinedAt,
		CreatedAt: member.CreatedAt,
	}, nil
}

// RemoveMember removes a member from the organization.
func (s *Service) RemoveMember(ctx context.Context, orgSlug, memberUID string) error {
	org, err := s.db.GetOrganizationBySlug(ctx, orgSlug)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrOrganizationNotFound
		}

		return err
	}

	member, err := s.db.GetOrganizationMember(ctx, memberUID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrMemberNotFound
		}

		return err
	}

	// Verify member belongs to this organization
	if member.OrganizationUID != org.UID {
		return ErrMemberNotFound
	}

	// Check if removing the last admin
	if member.Role == models.MemberRoleAdmin {
		adminCount, countErr := s.db.CountAdminsByOrg(ctx, org.UID)
		if countErr != nil {
			return countErr
		}

		if adminCount <= 1 {
			return ErrCannotRemoveLastAdmin
		}
	}

	return s.db.DeleteOrganizationMember(ctx, memberUID)
}

// isValidRole checks if the given role is valid.
func isValidRole(role models.MemberRole) bool {
	return role == models.MemberRoleAdmin ||
		role == models.MemberRoleUser ||
		role == models.MemberRoleViewer
}

// isDemotingAdmin returns true if this change would demote an admin.
func isDemotingAdmin(currentRole, newRole models.MemberRole) bool {
	return currentRole == models.MemberRoleAdmin && newRole != models.MemberRoleAdmin
}

// checkLastAdmin returns an error if the org has only one admin.
func (s *Service) checkLastAdmin(ctx context.Context, orgUID string) error {
	adminCount, countErr := s.db.CountAdminsByOrg(ctx, orgUID)
	if countErr != nil {
		return countErr
	}

	if adminCount <= 1 {
		return ErrCannotDemoteLastAdmin
	}

	return nil
}
