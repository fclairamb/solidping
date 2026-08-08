package auth

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

// registrationEmailPatternKey is the org parameter holding the regex that
// decides which email addresses may join the org on their own.
const registrationEmailPatternKey = "registration.email_pattern"

// pendingMembershipParam is the query flag the dashboard's no-org page reads
// to explain why a completed social login did not land in the org.
const pendingMembershipParam = "membershipPending"

// noOrgPath is the dashboard surface a user lands on when they are
// authenticated but hold no membership — it already renders the
// "request access / pending request" flow.
const noOrgPath = "/dash0/no-org"

// ProviderLoginResult is the outcome of a completed federated login for one
// organization: either a real org-scoped session, or (Pending) an org-less
// session plus a membership request awaiting an admin decision.
type ProviderLoginResult struct {
	AccessToken  string
	RefreshToken string
	ExpiresIn    int
	// Pending is true when the user authenticated successfully but was NOT
	// admitted to the org. No org-scoped session exists in that case: the
	// access token carries an empty org slug and there is no refresh token.
	Pending bool
}

// JoinOrgViaLogin decides what an authenticated user gets when they complete a
// login flow that was initiated from org's login page. It is the single
// admission chokepoint for every federated connector (Microsoft, Google,
// GitHub, GitLab, Discord, Slack, OIDC, SAML, LDAP) — before it existed each
// connector carried its own `ensureMembership` copy that added *any*
// authenticated account to *any* org whose slug appeared in the login URL,
// bypassing the org's registration.email_pattern entirely.
//
// Admission rules, in order:
//
//  0. Super admin      → no membership row, implicit access (member is nil).
//  1. Existing member  → returned as-is (pure login, nothing changes).
//  2. Zero-member org  → joins as admin (bootstrap / self-hosted onboarding).
//  3. Pending invite for the email → joins with the invited role, invite consumed.
//  4. Email matches registration.email_pattern → joins as user.
//  5. Otherwise        → NO membership; a membership_requests row is created or
//     re-opened and pending=true is returned.
//
// The MaxUsers cap (CheckMembershipSlot) is enforced here, once, ahead of any
// membership creation.
//
// A nil member with pending=false means "no membership row is needed" — only
// the super-admin case produces that.
func (s *Service) JoinOrgViaLogin(
	ctx context.Context, org *models.Organization, user *models.User,
) (*models.OrganizationMember, bool, error) {
	// Rule 1 first: an existing membership always wins, super admin or not.
	member, err := s.db.GetMemberByUserAndOrg(ctx, user.UID, org.UID)
	if err == nil {
		return member, false, nil
	}

	if !errors.Is(err, sql.ErrNoRows) {
		return nil, false, fmt.Errorf("failed to get member: %w", err)
	}

	// Rule 0: super admins reach every org through resolveOrgForSuperAdmin;
	// they need no membership row and must never be demoted to a membership
	// request by an org's email pattern.
	if user.SuperAdmin {
		return nil, false, nil
	}

	// Rule 2: bootstrap. The first ever member of an org becomes its admin.
	members, err := s.db.ListMembersByOrg(ctx, org.UID)
	if err != nil {
		return nil, false, fmt.Errorf("failed to list members: %w", err)
	}

	if len(members) == 0 {
		created, createErr := s.createLoginMembership(ctx, org.UID, user.UID, models.MemberRoleAdmin, "")
		return created, false, createErr
	}

	// Rule 3: an outstanding invitation for this email admits the user with
	// the invited role, exactly like AcceptInvite would.
	if invite := s.findOrgInvitationForEmail(ctx, org.UID, user.Email); invite != nil {
		created, createErr := s.createLoginMembership(
			ctx, org.UID, user.UID, models.MemberRole(invite.role), invite.inviterUID,
		)
		if createErr != nil {
			return nil, false, createErr
		}

		// Consume the invitation — it is single-use, like AcceptInvite's.
		_, _ = s.db.DeleteStateEntry(ctx, &org.UID, invite.key)

		return created, false, nil
	}

	// Rule 4: the org's own auto-join pattern.
	if pattern := s.orgAutoJoinPattern(ctx, org.UID); pattern != nil && pattern.MatchString(user.Email) {
		created, createErr := s.createLoginMembership(ctx, org.UID, user.UID, models.MemberRoleUser, "")
		return created, false, createErr
	}

	// Rule 5: not admitted. Fall back to the membership-request flow.
	slog.InfoContext(ctx, "federated login did not qualify for org membership",
		"orgUID", org.UID, "userUID", user.UID)

	if reqErr := s.ensureMembershipRequestForLogin(ctx, org, user.UID); reqErr != nil {
		return nil, false, reqErr
	}

	return nil, true, nil
}

// CompleteOrgLogin is the shared tail of every federated callback: run the
// admission rules, replay the cross-org auto-join, then mint the session that
// matches the outcome. A pending outcome yields an org-less access token (no
// refresh token) so the dashboard can show the request-access surface without
// ever granting org-scoped API access.
func (s *Service) CompleteOrgLogin(
	ctx context.Context, org *models.Organization, user *models.User,
) (*ProviderLoginResult, error) {
	member, pending, err := s.JoinOrgViaLogin(ctx, org, user)
	if err != nil {
		return nil, fmt.Errorf("failed to ensure membership: %w", err)
	}

	// Auto-join any *other* org whose pattern matches (unchanged behavior).
	s.autoJoinMatchingOrgs(ctx, user.UID, user.Email)

	if pending {
		return s.pendingSession(ctx, user)
	}

	role := RoleSuperAdmin
	if member != nil {
		role = string(member.Role)
	}

	tokens, err := s.GenerateTokensForOAuth(ctx, user, org, role)
	if err != nil {
		return nil, fmt.Errorf("failed to generate tokens: %w", err)
	}

	return &ProviderLoginResult{
		AccessToken:  tokens.AccessToken,
		RefreshToken: tokens.RefreshToken,
		ExpiresIn:    tokens.ExpiresIn,
	}, nil
}

// pendingSession mints the org-less session handed to a user who authenticated
// but was not admitted: an access token with an empty org slug and no role,
// and deliberately no refresh token — the same shape completeLogin produces
// for a password login by a user with no membership.
func (s *Service) pendingSession(ctx context.Context, user *models.User) (*ProviderLoginResult, error) {
	accessToken, err := s.generateAccessToken(user.UID, "", "", "")
	if err != nil {
		return nil, fmt.Errorf("failed to generate token: %w", err)
	}

	now := time.Now()
	if updateErr := s.db.UpdateUser(ctx, user.UID, &models.UserUpdate{LastActiveAt: &now}); updateErr != nil {
		slog.ErrorContext(ctx, "Failed to update user last_active_at", "error", updateErr, "userUID", user.UID)
	}

	return &ProviderLoginResult{
		AccessToken: accessToken,
		ExpiresIn:   int(s.cfg.AccessTokenExpiry.Seconds()),
		Pending:     true,
	}, nil
}

// createLoginMembership creates the membership row for an admitted federated
// login, enforcing the MaxUsers cap first. The very first member of an org
// bypasses the cap (count=0 < cap) so bootstrapping always succeeds.
func (s *Service) createLoginMembership(
	ctx context.Context, orgUID, userUID string, role models.MemberRole, inviterUID string,
) (*models.OrganizationMember, error) {
	if err := s.CheckMembershipSlot(ctx, orgUID); err != nil {
		return nil, err
	}

	member := models.NewOrganizationMember(orgUID, userUID, role)
	now := time.Now()
	member.JoinedAt = &now

	if inviterUID != "" {
		member.InvitedByUID = &inviterUID
		member.InvitedAt = &now
	}

	if err := s.db.CreateOrganizationMember(ctx, member); err != nil {
		return nil, fmt.Errorf("failed to create member: %w", err)
	}

	return member, nil
}

// orgInvitation is a pending invitation resolved for a given email.
type orgInvitation struct {
	key        string
	role       string
	inviterUID string
}

// findOrgInvitationForEmail returns the org's outstanding (non-expired)
// invitation addressed to email, or nil. Lookup errors are treated as
// "no invitation" — a login must not fail because the invite store hiccuped;
// the user simply falls through to the pattern / request rules.
func (s *Service) findOrgInvitationForEmail(ctx context.Context, orgUID, email string) *orgInvitation {
	if email == "" {
		return nil
	}

	entries, err := s.db.ListStateEntries(ctx, &orgUID, inviteKeyPrefix)
	if err != nil {
		slog.WarnContext(ctx, "failed to list invitations during federated login",
			"orgUID", orgUID, "error", err)

		return nil
	}

	for _, entry := range entries {
		if entry.Value == nil {
			continue
		}

		val := *entry.Value
		if !strings.EqualFold(stringFromMap(val, "email"), email) {
			continue
		}

		role := stringFromMap(val, "role")
		if role == "" {
			role = string(models.MemberRoleUser)
		}

		return &orgInvitation{
			key:        entry.Key,
			role:       role,
			inviterUID: stringFromMap(val, "inviterUID"),
		}
	}

	return nil
}

// orgAutoJoinPattern returns the org's registration.email_pattern compiled for
// matching, or nil when it is absent, empty, unsafe or uncompilable — nil
// means "this org has no rule-4 path", never "match everything". Unsafe
// patterns (rejected by validateAutoJoinRegex) are skipped with a warning
// rather than honored — the same defensive posture autoJoinMatchingOrgs
// takes, so a leftover over-broad regex written before validation existed
// cannot adopt every social login.
func (s *Service) orgAutoJoinPattern(ctx context.Context, orgUID string) *regexp.Regexp {
	param, err := s.db.GetOrgParameter(ctx, orgUID, registrationEmailPatternKey)
	if err != nil || param == nil {
		if err != nil {
			slog.WarnContext(ctx, "failed to read org email pattern", "orgUID", orgUID, "error", err)
		}

		return nil
	}

	pattern, ok := param.Value["value"].(string)
	if !ok || pattern == "" {
		return nil
	}

	if validateErr := validateAutoJoinRegex(pattern); validateErr != nil {
		slog.WarnContext(ctx, "skipping unsafe auto-join regex",
			"orgUID", orgUID, "error", validateErr)

		return nil
	}

	compiled, compileErr := regexp.Compile(pattern)
	if compileErr != nil {
		return nil
	}

	return compiled
}

// ensureMembershipRequestForLogin creates — or re-opens — the membership
// request that represents "this authenticated user would like into this org".
// A request already pending is left alone; a rejected one keeps its rejected
// state (the admin's decision and its cooldown stand, and the no-org screen
// surfaces rejected requests too); a cancelled one is re-opened.
func (s *Service) ensureMembershipRequestForLogin(
	ctx context.Context, org *models.Organization, userUID string,
) error {
	existing, err := s.db.GetMembershipRequestByOrgAndUser(ctx, org.UID, userUID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("failed to look up membership request: %w", err)
	}

	if existing == nil {
		request := models.NewMembershipRequest(org.UID, userUID, nil)
		if createErr := s.db.CreateMembershipRequest(ctx, request); createErr != nil {
			return fmt.Errorf("failed to create membership request: %w", createErr)
		}

		s.notifyAdminsOfMembershipRequest(ctx, org, userUID, nil)

		return nil
	}

	switch existing.Status {
	case models.MembershipRequestStatusPending,
		models.MembershipRequestStatusRejected,
		models.MembershipRequestStatusApproved:
		return nil
	case models.MembershipRequestStatusCancelled:
		existing.Status = models.MembershipRequestStatusPending
		existing.DecisionReason = nil
		existing.DecidedAt = nil
		existing.DecidedByUID = nil

		if updateErr := s.db.UpdateMembershipRequest(ctx, existing); updateErr != nil {
			return fmt.Errorf("failed to reopen membership request: %w", updateErr)
		}

		s.notifyAdminsOfMembershipRequest(ctx, org, userUID, nil)
	}

	return nil
}

// pendingMembershipRedirect is where a provider callback sends a browser whose
// login completed without admission: the dashboard's no-org surface (which
// already renders "request access" and the user's pending requests), carrying
// the org-less session and an explicit flag naming the org that was refused.
// Deliberately NOT the org dashboard, and deliberately without an `org` handoff
// param — there is no org-scoped session to hand off.
func pendingMembershipRedirect(orgSlug, accessToken string, expiresIn int) string {
	query := url.Values{}
	query.Set("access_token", accessToken)
	query.Set("expires_in", strconv.Itoa(expiresIn))
	query.Set(pendingMembershipParam, orgSlug)

	return noOrgPath + "?" + query.Encode()
}
