package members

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

// MemberChannelCoverage describes ONE delivery channel a member has configured,
// stripped down to what an admin legitimately needs in order to judge paging
// coverage.
//
// Deliberately no `value`: the whole point of per-user contacts is that the
// phone number, Telegram chat id or push endpoint belongs to the member, not to
// their admin. An admin needs to know "Bob has an unverified phone" to chase him
// — not what Bob's number is.
type MemberChannelCoverage struct {
	Type     string `json:"type"`
	Verified bool   `json:"verified"`
	Enabled  bool   `json:"enabled"`
}

// MemberCoverageResponse is one member's paging coverage.
type MemberCoverageResponse struct {
	UserUID  string                  `json:"userUid"`
	Email    string                  `json:"email"`
	Name     string                  `json:"name,omitempty"`
	Role     string                  `json:"role"`
	Channels []MemberChannelCoverage `json:"channels"`
	// EmailFallbackOnly is the signal this endpoint exists for: nothing but
	// email can reach this person. Escalation still pages them (the silent
	// email fallback in job_escalation_step.go), but at 3am an email is not a
	// page — so an admin rostering them on-call deserves a warning.
	EmailFallbackOnly bool `json:"emailFallbackOnly"`
}

// ListMemberCoverageResponse wraps the coverage list.
type ListMemberCoverageResponse struct {
	Data []*MemberCoverageResponse `json:"data"`
}

// ListCoverage returns per-member paging coverage for an organization.
//
// Routes are normally `users/me`-scoped; this is the one admin-only view over
// them, and it exposes channel TYPES and verification flags only — never a
// contact value.
func (s *Service) ListCoverage(ctx context.Context, orgSlug string) (*ListMemberCoverageResponse, error) {
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

	response := &ListMemberCoverageResponse{
		Data: make([]*MemberCoverageResponse, 0, len(members)),
	}

	for _, member := range members {
		user, userErr := s.db.GetUser(ctx, member.UserUID)
		if userErr != nil {
			continue // Skip members with missing users, like ListMembers does.
		}

		routes, routesErr := s.db.ListUserContactsWithRoutes(ctx, member.UserUID, org.UID)
		if routesErr != nil {
			return nil, fmt.Errorf("list coverage for %s: %w", member.UserUID, routesErr)
		}

		response.Data = append(response.Data, buildCoverage(user, member, routes))
	}

	return response, nil
}

// buildCoverage projects a member's routes down to the admin-visible shape.
func buildCoverage(
	user *models.User, member *models.OrganizationMember, routes []*models.UserNotificationRoute,
) *MemberCoverageResponse {
	coverage := &MemberCoverageResponse{
		UserUID:  user.UID,
		Email:    user.Email,
		Name:     user.Name,
		Role:     string(member.Role),
		Channels: make([]MemberChannelCoverage, 0, len(routes)),
	}

	for _, route := range routes {
		if route.Contact == nil {
			continue
		}

		coverage.Channels = append(coverage.Channels, MemberChannelCoverage{
			Type:     route.Contact.Type,
			Verified: route.Contact.VerifiedAt != nil,
			Enabled:  route.Enabled,
		})
	}

	coverage.EmailFallbackOnly = isEmailFallbackOnly(coverage.Channels)

	return coverage
}

// isEmailFallbackOnly reports whether nothing but email can actually reach this
// person right now.
//
// A channel counts only when it is BOTH enabled and verified: a disabled route
// is never dispatched, and an unverified phone is never paged (that is the
// whole point of verification). So "Bob added a phone but never confirmed the
// code" still reads as email-only — which is exactly the situation an admin
// needs to see, and the one a naive "has a phone contact" check would hide.
func isEmailFallbackOnly(channels []MemberChannelCoverage) bool {
	for _, channel := range channels {
		if channel.Type == models.UserContactTypeEmail {
			continue
		}

		if channel.Enabled && channel.Verified {
			return false
		}
	}

	return true
}
