package auth

import (
	"context"
	"log/slog"
	"regexp"
	"strings"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/i18n/recipient"
)

// ensurePersonalOrg creates a personal organization for a user who has no
// membership and nothing else that could give them one, so a sign-in does not
// end on /no-org (spec 2026-10-30-04).
//
// It keeps today's behavior (returns nil, creates nothing) when the user
// could still be admitted somewhere:
//  1. an outstanding invitation addressed to their email;
//  2. a registration email pattern that matches their email;
//  3. a pending membership request of theirs.
//
// Callers only invoke it for a user who has no usable membership. A user who
// just deleted their last org is deliberately NOT routed here (org_delete.go):
// recreating one would undo their action.
//
// Best-effort: any failure is logged and yields nil, so the login falls back
// to the org-less session instead of failing.
func (s *Service) ensurePersonalOrg(
	ctx context.Context, user *models.User,
) *models.OrganizationMember {
	if user == nil || user.SuperAdmin {
		return nil
	}

	if s.hasPendingInvitation(ctx, user.Email) ||
		len(s.autoJoinOrgUIDs(ctx, user.Email)) > 0 ||
		s.hasPendingMembershipRequest(ctx, user.UID) {
		return nil
	}

	firstName := personalFirstName(user)
	name := recipient.ForUser(user).T("common.personalOrg.name", "firstName", firstName)

	created, err := s.CreateOrg(ctx, user.UID, CreateOrgRequest{
		Name:     name,
		SlugBase: firstName,
	}, contextFromRequestMeta(ctx, Context{}))
	if err != nil {
		slog.WarnContext(ctx, "could not create a personal organization", "userUID", user.UID, "error", err)

		return nil
	}

	org, err := s.db.GetOrganizationBySlug(ctx, created.Slug)
	if err != nil {
		slog.WarnContext(ctx, "could not load the personal organization", "userUID", user.UID, "error", err)

		return nil
	}

	member, err := s.db.GetMemberByUserAndOrg(ctx, user.UID, org.UID)
	if err != nil {
		slog.WarnContext(ctx, "could not load the personal organization membership", "userUID", user.UID, "error", err)

		return nil
	}

	member.Organization = org

	return member
}

// personalFirstName is the first word of the user's name, falling back to the
// local part of their email when the name is empty.
func personalFirstName(user *models.User) string {
	if fields := strings.Fields(user.Name); len(fields) > 0 {
		return fields[0]
	}

	local, _, _ := strings.Cut(user.Email, "@")

	return local
}

// hasPendingInvitation reports whether any organization holds an outstanding
// invitation for email. It walks the global invitation index (one entry per
// invitation, same key as the org-scoped entry).
func (s *Service) hasPendingInvitation(ctx context.Context, email string) bool {
	index, err := s.db.ListStateEntries(ctx, nil, inviteKeyPrefix)
	if err != nil {
		slog.WarnContext(ctx, "failed to list the invitation index", "error", err)

		return false
	}

	seen := map[string]bool{}

	for _, entry := range index {
		if entry.Value == nil {
			continue
		}

		orgUID := stringFromMap(*entry.Value, keyInviteOrgUID)
		if orgUID == "" || seen[orgUID] {
			continue
		}

		seen[orgUID] = true

		if s.findOrgInvitationForEmail(ctx, orgUID, email) != nil {
			return true
		}
	}

	return false
}

// hasPendingMembershipRequest reports whether the user has a request awaiting
// an admin's decision.
func (s *Service) hasPendingMembershipRequest(ctx context.Context, userUID string) bool {
	requests, err := s.db.ListMembershipRequests(ctx, models.ListMembershipRequestsFilter{
		UserUID: userUID,
		Status:  models.MembershipRequestStatusPending,
		Limit:   1,
	})
	if err != nil {
		slog.WarnContext(ctx, "failed to list membership requests", "userUID", userUID, "error", err)

		return false
	}

	return len(requests) > 0
}

// autoJoinOrgUIDs returns the organizations whose registration email pattern
// matches email. Unsafe or invalid patterns are skipped.
func (s *Service) autoJoinOrgUIDs(ctx context.Context, email string) []string {
	params, err := s.db.ListOrgParametersByKey(ctx, "registration.email_pattern")
	if err != nil {
		slog.ErrorContext(ctx, "Failed to list org email patterns", "error", err)

		return nil
	}

	var uids []string

	for _, param := range params {
		if param.OrganizationUID == nil {
			continue
		}

		patternVal, ok := param.Value["value"].(string)
		if !ok || patternVal == "" {
			continue
		}

		// Defensive: leftover unsafe patterns (set before validation existed)
		// must be skipped so they cannot adopt every signup.
		if err := validateAutoJoinRegex(patternVal); err != nil {
			slog.WarnContext(
				ctx, "skipping unsafe auto-join regex",
				"orgUID", *param.OrganizationUID, "error", err,
			)

			continue
		}

		re, err := regexp.Compile(patternVal)
		if err != nil || !re.MatchString(email) {
			continue
		}

		uids = append(uids, *param.OrganizationUID)
	}

	return uids
}
