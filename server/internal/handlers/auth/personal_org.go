package auth

import (
	"context"
	"database/sql"
	"errors"
	"hash/fnv"
	"log/slog"
	"strings"
	"unicode"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/i18n/recipient"
)

// Auto-created personal organizations (spec 2026-10-30-04).
//
// A user who signs in belonging to no organization used to land on /no-org
// and be asked "what is an organization, which slug?", a question a new user
// cannot answer. ensurePersonalOrg settles it
// at the sign-in instead, in this order:
//
//  1. a pending invitation for the email: join that org (the invite is used);
//  2. an org whose registration.email_pattern matches: join it;
//  3. a pending membership request by this user: nothing, /no-org shows it;
//  4. otherwise, when auth.auto_personal_org is on (the default on SaaS, off
//     on self-hosted), create a personal org, the user its OWNER.
//
// Every no-org site calls it (password, LDAP and passkey logins through
// resolveOrgPreference, the registration confirmation and the federated
// callbacks through completePendingLogin), so the paths cannot drift. Org
// deletion deliberately does not: a user who deleted their last org lands on
// /no-org, and keeps landing there (one personal org per lifetime).

// autoPersonalOrgEnabled reports whether auth.auto_personal_org is on.
func (s *Service) autoPersonalOrgEnabled() bool {
	return s.fullCfg != nil && s.fullCfg.AutoPersonalOrgEnabled()
}

// ensurePersonalOrg settles a user who has no organization (see the order at
// the top of this file). It returns the membership the sign-in should land on,
// with Organization populated, or nil when the user stays org-less: setting
// off, a pending request, a super admin or a demo user (neither may create an
// org by hand: the demo write guard refuses POST /orgs), or a user whose one
// personal org already existed and was deleted.
//
// Best-effort: a failure is logged and the user lands on /no-org as before,
// never an error that fails the sign-in.
func (s *Service) ensurePersonalOrg(ctx context.Context, user *models.User) *models.OrganizationMember {
	if user == nil || user.SuperAdmin || user.Demo || !s.autoPersonalOrgEnabled() {
		return nil
	}

	member, invited := s.joinPendingInvitation(ctx, user)
	if member != nil {
		return member
	}

	if invited {
		// An invitation exists but could not be used (the org is at its seat
		// cap). The user is expected there: do not give them another org.
		return nil
	}

	s.autoJoinMatchingOrgs(ctx, user.UID, user.Email)

	if joined := s.fallbackMemberOrg(ctx, user.UID); joined != nil {
		return joined
	}

	if s.hasPendingMembershipRequest(ctx, user.UID) {
		return nil
	}

	return s.createPersonalOrg(ctx, user)
}

// joinPendingInvitation joins the user to an org that has a pending
// invitation for their email, consuming it like JoinOrgViaLogin's rule 3.
// invited reports that such an invitation exists, even when joining failed.
func (s *Service) joinPendingInvitation(
	ctx context.Context, user *models.User,
) (*models.OrganizationMember, bool) {
	if user.Email == "" {
		return nil, false
	}

	// The per-invitee index (invite_email:<hash>:<orgUID>) names the orgs
	// with a live invitation for this address: one prefix read, bounded by
	// the number of orgs that invited it, whatever the instance holds.
	index, err := s.db.ListStateEntries(ctx, nil, inviteEmailKeyPrefix+hashPendingToken(
		strings.ToLower(strings.TrimSpace(user.Email)))+":")
	if err != nil {
		slog.WarnContext(ctx, "personal org: cannot list invitations", "userUID", user.UID, "error", err)

		return nil, false
	}

	invited := false

	for _, entry := range index {
		if entry.Value == nil {
			continue
		}

		orgUID := stringFromMap(*entry.Value, keyInviteOrgUID)
		inviteKey := stringFromMap(*entry.Value, keyInviteKey)

		if orgUID == "" || inviteKey == "" {
			continue
		}

		stored, getErr := s.db.GetStateEntry(ctx, &orgUID, inviteKey)
		if getErr != nil || stored == nil || stored.Value == nil {
			continue
		}

		role := stringFromMap(*stored.Value, "role")
		if role == "" {
			role = string(models.MemberRoleUser)
		}

		invite := &orgInvitation{key: inviteKey, role: role, inviterUID: stringFromMap(*stored.Value, "inviterUID")}

		org, orgErr := s.db.GetOrganization(ctx, orgUID)
		if orgErr != nil || org.DeletedAt != nil {
			continue
		}

		invited = true

		member, joinErr := s.createLoginMembership(
			ctx, orgUID, user, models.MemberRole(invite.role), invite.inviterUID, joinSourceInvitation,
		)
		if joinErr != nil {
			slog.WarnContext(ctx, "personal org: cannot join the inviting org",
				"userUID", user.UID, "orgUID", orgUID, "error", joinErr)

			continue
		}

		_ = s.deleteInvite(ctx, orgUID, invite.key)
		member.Organization = org

		return member, true
	}

	return nil, invited
}

// hasPendingMembershipRequest reports whether the user is waiting for an
// answer to a membership request. A lookup error counts as "yes": in doubt the
// user keeps today's /no-org page rather than getting an org they may not want.
func (s *Service) hasPendingMembershipRequest(ctx context.Context, userUID string) bool {
	requests, err := s.db.ListMembershipRequests(ctx, models.ListMembershipRequestsFilter{
		UserUID: userUID,
		Status:  models.MembershipRequestStatusPending,
		Limit:   1,
	})
	if err != nil {
		slog.WarnContext(ctx, "personal org: cannot list membership requests", "userUID", userUID, "error", err)

		return true
	}

	return len(requests) > 0
}

// createPersonalOrg creates the user's personal org, or returns the one a
// concurrent sign-in just created. A user gets one in their lifetime: once it
// was deleted, nil.
func (s *Service) createPersonalOrg(ctx context.Context, user *models.User) *models.OrganizationMember {
	previous, err := s.db.GetAutoPersonalOrgByOwner(ctx, user.UID, true)
	if err != nil {
		slog.WarnContext(ctx, "personal org: lookup failed", "userUID", user.UID, "error", err)

		return nil
	}

	if previous != nil {
		if previous.DeletedAt != nil {
			return nil
		}

		return s.personalOrgMember(ctx, user.UID, previous)
	}

	name, slugBase := personalOrgName(user)

	ownerUID := user.UID

	org, err := s.createOrgForUser(ctx, user.UID, CreateOrgRequest{Name: name, SlugBase: slugBase}, &ownerUID)
	if err != nil {
		// The partial unique index refused a second live personal org: a
		// concurrent first sign-in won the race. Land on its org.
		if live, lookupErr := s.db.GetAutoPersonalOrgByOwner(ctx, user.UID, false); lookupErr == nil && live != nil {
			return s.personalOrgMember(ctx, user.UID, live)
		}

		slog.ErrorContext(ctx, "personal org: creation failed", "userUID", user.UID, "error", err)

		return nil
	}

	slog.InfoContext(ctx, "personal organization created", "userUID", user.UID, "orgUID", org.UID, "orgSlug", org.Slug)

	return s.personalOrgMember(ctx, user.UID, org)
}

// personalOrgMember is the user's OWNER membership of their personal org. The
// row may not be readable yet when a concurrent sign-in is still creating it:
// the membership is then the one that sign-in is writing.
func (s *Service) personalOrgMember(
	ctx context.Context, userUID string, org *models.Organization,
) *models.OrganizationMember {
	member, err := s.db.GetMemberByUserAndOrg(ctx, userUID, org.UID)
	if err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			slog.WarnContext(ctx, "personal org: membership lookup failed", "userUID", userUID, "error", err)
		}

		member = models.NewOrganizationMember(org.UID, userUID, models.MemberRoleOwner)
	}

	member.Organization = org

	return member
}

// personalOrgName is the name and slug hint of a personal org: "{firstName}'s
// organization" in the user's language, the slug from the first name, like
// the /no-org suggestion (createOrg.suggestedPersonal in dash0). Without a
// usable first name, a friendly two-word name drawn from the user's UID.
func personalOrgName(user *models.User) (string, string) {
	if first := firstNameOf(user.Name); first != "" {
		return recipient.ForUser(user).T("account.personalOrg.name", "firstName", first), first
	}

	name := randomOrgName(user.UID)

	return name, name
}

// firstNameOf mirrors dash0's firstNameOf: the first whitespace-separated
// token of the display name, when it holds at least one letter or digit.
func firstNameOf(name string) string {
	fields := strings.Fields(name)
	if len(fields) == 0 {
		return ""
	}

	for _, r := range fields[0] {
		if unicode.IsLetter(r) || unicode.IsNumber(r) {
			return fields[0]
		}
	}

	return ""
}

// The two-word vocabulary of dash0's org-name-suggestion.ts, kept in sync.
//
//nolint:gochecknoglobals // immutable word lists
var (
	orgNameAdjectives = []string{
		"Bright", "Calm", "Clever", "Golden", "Happy", "Lucky",
		"Quiet", "Rapid", "Silver", "Sunny", "Swift", "Vivid",
	}
	orgNameNouns = []string{
		"Anchor", "Beacon", "Comet", "Falcon", "Harbor", "Lantern",
		"Meadow", "Otter", "Panda", "River", "Summit", "Willow",
	}
)

// randomOrgName is a stable two-word name for seed.
func randomOrgName(seed string) string {
	hasher := fnv.New32a()
	_, _ = hasher.Write([]byte(seed))
	base := int(hasher.Sum32() % uint32(len(orgNameAdjectives)*len(orgNameNouns)))

	return orgNameAdjectives[base%len(orgNameAdjectives)] + " " +
		orgNameNouns[(base/len(orgNameAdjectives))%len(orgNameNouns)]
}

// withPersonalOrg folds a no-org resolution into ensurePersonalOrg: when the
// user gets an org, the login lands on it (LoginActionDefault) instead of
// /no-org.
func (s *Service) withPersonalOrg(
	ctx context.Context, user *models.User,
	org *models.Organization, role string, action LoginAction, summaries []OrganizationSummary,
) (*models.Organization, string, LoginAction, []OrganizationSummary) {
	if action != LoginActionNoOrg || user.SuperAdmin {
		return org, role, action, summaries
	}

	member := s.ensurePersonalOrg(ctx, user)
	if member == nil {
		return org, role, action, summaries
	}

	summaries = append(summaries, OrganizationSummary{
		Slug:    member.Organization.Slug,
		Name:    member.Organization.Name,
		LogoURL: member.Organization.LogoURL,
		Role:    string(member.Role),
	})

	return member.Organization, string(member.Role), LoginActionDefault, summaries
}
