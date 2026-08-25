// Package members provides HTTP handlers for organization member management.
package members

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/fclairamb/solidping/server/internal/audit"
	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/email"
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
	// ErrCannotRemoveLastOwner is returned when trying to remove the last owner.
	ErrCannotRemoveLastOwner = errors.New("cannot remove the last owner from the organization")
	// ErrCannotDemoteLastOwner is returned when trying to demote the last owner.
	ErrCannotDemoteLastOwner = errors.New("cannot demote the last owner")
	// ErrOwnerRoleRequired is returned when a non-owner tries to grant or revoke
	// the owner role, or to modify/remove a member who is an owner.
	ErrOwnerRoleRequired = errors.New("only an owner can manage owners")
)

// Service provides business logic for member management.
type Service struct {
	db db.Service
	// email sends the admin-triggered "set up your paging" nudge. Nil on
	// instances with no outbound email, which makes the nudge endpoint answer a
	// clear error instead of silently doing nothing.
	email email.Sender
	// formatter renders the nudge through the shared template pipeline
	// (paging-nudge.html). Nil on instances that never wired one — the nudge
	// endpoint then answers a clear error rather than sending an empty email.
	formatter email.Formatter
	// appBaseURL builds the dashboard link inside that nudge.
	appBaseURL string
}

// NewService creates a new members service. Options are only needed by the
// admin pre-provisioning surface (spec 2026-08-12-03); the core member CRUD
// works with none.
func NewService(dbService db.Service, opts ...Option) *Service {
	svc := &Service{db: dbService}
	for _, opt := range opts {
		opt(svc)
	}

	return svc
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

// Caller identifies the authenticated user performing a member-management
// operation. Deliberately NOT the caller's JWT role claim: that claim is minted
// at login and goes stale the moment somebody's role changes, so a just-demoted
// owner would keep owner powers until their access token expired. The role is
// re-read from the membership row on every call.
type Caller struct {
	UserUID    string
	SuperAdmin bool
}

// roleIn resolves the caller's effective role in an organization. Super admins
// outrank everybody and need no membership row; a caller with no membership has
// no role at all (rank 0), which satisfies no gate.
func (s *Service) roleIn(ctx context.Context, caller Caller, orgUID string) models.MemberRole {
	if caller.SuperAdmin {
		return models.MemberRoleOwner
	}

	if caller.UserUID == "" {
		return ""
	}

	member, err := s.db.GetMemberByUserAndOrg(ctx, caller.UserUID, orgUID)
	if err != nil || member == nil {
		return ""
	}

	return member.Role
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

// AddMember adds a user to an organization by email. Only an owner may mint
// another owner.
func (s *Service) AddMember(
	ctx context.Context, orgSlug string, req AddMemberRequest, inviterUID *string, caller Caller,
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
	if !isValidRole(role) {
		return nil, ErrInvalidRole
	}

	// Only an owner may create another owner.
	if role == models.MemberRoleOwner && !s.roleIn(ctx, caller, org.UID).AtLeast(models.MemberRoleOwner) {
		return nil, ErrOwnerRoleRequired
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

	// member.joined rather than member.invited: this path creates the
	// membership row outright. member.invited belongs to the invitation flow,
	// where the membership does not exist yet.
	audit.Record(ctx, s.db, org.UID, models.EventTypeMemberJoined,
		auditMemberTarget(member, user), models.JSONMap{
			fieldRole:  string(member.Role),
			fieldEmail: user.Email,
			"source":   "admin_add",
		})

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

// Field names shared by the validation messages and the audit payloads.
const (
	fieldRole  = "role"
	fieldEmail = "email"
)

// auditMemberTarget names a membership for the audit trail. The target is the
// USER (that is who a reader is looking for), identified by UID with the email
// as the readable name.
func auditMemberTarget(member *models.OrganizationMember, user *models.User) audit.Target {
	target := audit.Target{Type: "member", UID: member.UserUID}
	if user != nil {
		target.Name = user.Email
	}

	return target
}

// UpdateMember updates a member's role.
func (s *Service) UpdateMember(
	ctx context.Context, orgSlug, memberUID string, req UpdateMemberRequest, caller Caller,
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

	previousRole := member.Role

	// Prepare update
	update := models.OrganizationMemberUpdate{}

	if req.Role != nil {
		newRole := models.MemberRole(*req.Role)
		if roleErr := s.authorizeRoleChange(ctx, caller, org.UID, member.Role, newRole); roleErr != nil {
			return nil, roleErr
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

	// Only when the role actually moved — a PATCH that re-sends the current
	// role is not a privilege change and must not read as one in the trail.
	if member.Role != previousRole {
		audit.Record(ctx, s.db, org.UID, models.EventTypeMemberRoleChanged,
			auditMemberTarget(member, user), models.JSONMap{
				fieldEmail:      user.Email,
				"previous_role": string(previousRole),
				fieldRole:       string(member.Role),
			})
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

// authorizeRoleChange validates a member role change: the role itself, the
// caller's authority over the owner role, and the two "don't strand the org"
// guards.
func (s *Service) authorizeRoleChange(
	ctx context.Context, caller Caller, orgUID string, currentRole, newRole models.MemberRole,
) error {
	if !isValidRole(newRole) {
		return ErrInvalidRole
	}

	// Ownership is owner-only in BOTH directions: an admin can neither promote
	// somebody to owner nor demote (or otherwise touch) an existing owner.
	// Without the second half, any admin could simply demote the owner and take
	// the org.
	if (newRole == models.MemberRoleOwner || currentRole == models.MemberRoleOwner) &&
		!s.roleIn(ctx, caller, orgUID).AtLeast(models.MemberRoleOwner) {
		return ErrOwnerRoleRequired
	}

	// Demoting the last owner. Multiple owners are allowed, which is also the
	// ownership-transfer path: promote someone to owner, then demote yourself.
	if isDemotingOwner(currentRole, newRole) {
		if lastOwnerErr := s.checkLastOwner(ctx, orgUID); lastOwnerErr != nil {
			return lastOwnerErr
		}
	}

	// Demoting the last admin-or-above.
	if isDemotingAdmin(currentRole, newRole) {
		if lastAdminErr := s.checkLastAdmin(ctx, orgUID); lastAdminErr != nil {
			return lastAdminErr
		}
	}

	return nil
}

// RemoveMember removes a member from the organization.
func (s *Service) RemoveMember(
	ctx context.Context, orgSlug, memberUID string, caller Caller,
) error {
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

	// Only an owner may remove an owner — otherwise an admin could evict the
	// org's owner and take over.
	if member.Role == models.MemberRoleOwner {
		if !s.roleIn(ctx, caller, org.UID).AtLeast(models.MemberRoleOwner) {
			return ErrOwnerRoleRequired
		}

		ownerCount, countErr := s.db.CountOwnersByOrg(ctx, org.UID)
		if countErr != nil {
			return countErr
		}

		if ownerCount <= 1 {
			return ErrCannotRemoveLastOwner
		}
	}

	// Check if removing the last admin-or-above (owners count as admins).
	if member.Role.AtLeast(models.MemberRoleAdmin) {
		adminCount, countErr := s.db.CountAdminsByOrg(ctx, org.UID)
		if countErr != nil {
			return countErr
		}

		if adminCount <= 1 {
			return ErrCannotRemoveLastAdmin
		}
	}

	if err := s.db.DeleteOrganizationMember(ctx, memberUID); err != nil {
		return err
	}

	// Best-effort name resolution: the membership is already gone and a
	// failed user lookup must not turn a successful removal into an error.
	user, _ := s.db.GetUser(ctx, member.UserUID)

	payload := models.JSONMap{fieldRole: string(member.Role)}
	if user != nil {
		payload[fieldEmail] = user.Email
	}

	audit.Record(ctx, s.db, org.UID, models.EventTypeMemberRemoved,
		auditMemberTarget(member, user), payload)

	return nil
}

// isValidRole checks if the given role is valid.
func isValidRole(role models.MemberRole) bool {
	return role.IsValid()
}

// isDemotingAdmin returns true if this change drops the member below the admin
// tier. Expressed against the hierarchy so owner→admin does NOT count as a
// demotion (the member still holds admin privileges) while owner→user does.
func isDemotingAdmin(currentRole, newRole models.MemberRole) bool {
	return currentRole.AtLeast(models.MemberRoleAdmin) && !newRole.AtLeast(models.MemberRoleAdmin)
}

// isDemotingOwner returns true if this change strips the owner role.
func isDemotingOwner(currentRole, newRole models.MemberRole) bool {
	return currentRole == models.MemberRoleOwner && newRole != models.MemberRoleOwner
}

// checkLastAdmin returns an error if the org has only one admin-or-above.
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

// checkLastOwner returns an error if the org has only one owner. An org must
// never be left ownerless: with no owner nobody could delete it or hand
// ownership on.
func (s *Service) checkLastOwner(ctx context.Context, orgUID string) error {
	ownerCount, countErr := s.db.CountOwnersByOrg(ctx, orgUID)
	if countErr != nil {
		return countErr
	}

	if ownerCount <= 1 {
		return ErrCannotDemoteLastOwner
	}

	return nil
}
