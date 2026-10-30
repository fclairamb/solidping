package auth

import (
	"context"
	"database/sql"
	"errors"
	"hash/fnv"
	"log/slog"
	"strings"
	"time"
	"unicode"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/i18n/recipient"
	"github.com/fclairamb/solidping/server/internal/prommetrics"
)

// Auto-created personal organizations (spec 2026-10-30-04).
//
// A user who signs in belonging to no organization used to land on /no-org
// and be asked "what is an organization, which slug?", a question a new user
// cannot answer. With auth.auto_personal_org on, ensurePersonalOrg settles it
// at the sign-in instead, in this order:
//
//  1. a pending invitation for the email: join that org (the invite is used);
//  2. an org whose registration.email_pattern matches: join it;
//  3. a pending membership request by this user: nothing, /no-org shows it;
//  4. otherwise create a personal org, the user its OWNER.
//
// Every no-org site calls it (password, LDAP and passkey logins through
// resolveOrgPreference, the registration confirmation, the federated
// callbacks through completePendingLogin, and the session minted after an
// org deletion), so the paths cannot drift.

// No-org funnel outcomes, the outcome label of solidping_no_org_outcomes_total.
const (
	noOrgOutcomeLanded        = "landed"
	noOrgOutcomeAutoCreated   = "auto_created"
	noOrgOutcomeCreated       = "created"
	noOrgOutcomeJoinRequested = "join_requested"
	noOrgOutcomeLeft          = "left"
)

// Abandoned personal-org cleanup thresholds.
const (
	// personalOrgWarnAge is when an empty personal org's owner is warned.
	personalOrgWarnAge = 23 * 24 * time.Hour
	// personalOrgDeleteAge is the earliest age at which it is deleted.
	personalOrgDeleteAge = 30 * 24 * time.Hour
	// personalOrgNoticePeriod is the least time between the warning and the
	// deletion: an org first seen past day 30 still gets its seven days.
	personalOrgNoticePeriod = personalOrgDeleteAge - personalOrgWarnAge
	// personalOrgWarnedKey is the org-scoped state entry recording the
	// warning (value: warnedAt, RFC 3339).
	personalOrgWarnedKey = "personal_org.cleanup_warned"
	keyWarnedAt          = "warnedAt"
	// personalOrgSlugFallback seeds a slug when the name gives nothing usable.
	personalOrgSlugFallback = "my-org"
)

// recordNoOrgOutcome counts one outcome of the no-org funnel.
func recordNoOrgOutcome(outcome string) {
	prommetrics.NoOrgOutcomes.WithLabelValues(outcome).Inc()
}

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
	if user == nil || !s.autoPersonalOrgEnabled() || user.SuperAdmin || user.Demo {
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

	// The global index entries (invite:<hash>, no org) name the org of every
	// live invitation, so this is one query plus one per inviting org.
	index, err := s.db.ListStateEntries(ctx, nil, inviteKeyPrefix)
	if err != nil {
		slog.WarnContext(ctx, "personal org: cannot list invitations", "userUID", user.UID, "error", err)

		return nil, false
	}

	seen := make(map[string]struct{}, len(index))
	invited := false

	for _, entry := range index {
		if entry.Value == nil {
			continue
		}

		orgUID := stringFromMap(*entry.Value, keyInviteOrgUID)
		if orgUID == "" {
			continue
		}

		if _, done := seen[orgUID]; done {
			continue
		}

		seen[orgUID] = struct{}{}

		invite := s.findOrgInvitationForEmail(ctx, orgUID, user.Email)
		if invite == nil {
			continue
		}

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
// was deleted (by them or by the cleanup), nil.
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

	recordNoOrgOutcome(noOrgOutcomeAutoCreated)
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

// ColleagueHintOrg is the one organization the colleague hint names.
type ColleagueHintOrg struct {
	Slug string `json:"slug"`
	Name string `json:"name"`
}

// ColleagueHintResponse is the body of GET /api/v1/auth/colleague-hint. Org is
// null when there is nothing to suggest; then the other fields are empty too.
type ColleagueHintResponse struct {
	Org *ColleagueHintOrg `json:"org"`
	// Domain is the email domain the caller shares with the org's members.
	Domain string `json:"domain,omitempty"`
	// PersonalOrgSlug is the caller's personal org, the one whose dashboard
	// shows the banner.
	PersonalOrgSlug string `json:"personalOrgSlug,omitempty"`
}

// colleagueHintLookupLimit: two rows are enough to tell "exactly one" apart.
const colleagueHintLookupLimit = 2

// ColleagueHint names the organization the user's colleagues already use, for
// the dashboard banner of a personal org (spec 2026-10-30-04): "People from
// acme.com use SolidPing in <org>: request to join".
//
// It answers nil unless every condition holds, so it cannot be used to list
// or probe organizations:
//   - auth.auto_personal_org is on and the user has a live personal org;
//   - the email domain is not a webmail one;
//   - existing members of exactly one other organization (personal orgs
//     excluded) share that domain;
//   - that org accepts join requests, and the user has no pending one there.
func (s *Service) ColleagueHint(ctx context.Context, userUID string) (*ColleagueHintResponse, error) {
	if !s.autoPersonalOrgEnabled() {
		return nil, nil //nolint:nilnil // no hint is a legitimate answer
	}

	user, err := s.db.GetUser(ctx, userUID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrUserNotFound
		}

		return nil, err
	}

	at := strings.LastIndex(user.Email, "@")
	if at < 0 || at == len(user.Email)-1 {
		return nil, nil //nolint:nilnil // no domain, no hint
	}

	domain := strings.ToLower(user.Email[at+1:])
	if isFreeMailDomain(domain) {
		return nil, nil //nolint:nilnil // webmail says nothing about colleagues
	}

	personal, err := s.db.GetAutoPersonalOrgByOwner(ctx, userUID, false)
	if err != nil {
		return nil, err
	}

	if personal == nil {
		return nil, nil //nolint:nilnil // the hint belongs to a personal org's dashboard
	}

	orgs, err := s.db.ListOrgsWithMemberEmailDomain(ctx, domain, userUID, colleagueHintLookupLimit)
	if err != nil {
		return nil, err
	}

	if len(orgs) != 1 {
		return nil, nil //nolint:nilnil // none, or several: naming them would list orgs
	}

	org := orgs[0]
	if s.loginJoinRequestSuppressedReason(org) != "" {
		return nil, nil //nolint:nilnil // an org that never admits requests is not suggested
	}

	request, err := s.db.GetMembershipRequestByOrgAndUser(ctx, org.UID, userUID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}

	if request != nil && request.Status == models.MembershipRequestStatusPending {
		return nil, nil //nolint:nilnil // already asked
	}

	return &ColleagueHintResponse{
		Org:             &ColleagueHintOrg{Slug: org.Slug, Name: org.Name},
		Domain:          domain,
		PersonalOrgSlug: personal.Slug,
	}, nil
}

// PersonalOrgSweepResult counts what one cleanup pass did.
type PersonalOrgSweepResult struct {
	Warned  int
	Deleted int
}

// SweepAbandonedPersonalOrgs is the body of the personal_org_cleanup job
// (spec 2026-10-30-04). An auto-created personal org that still has no check
// and a single member gets its owner warned by email at day 23, and is
// deleted at day 30, through DeleteOrg like an owner's own deletion. The
// deletion needs the warning to be at least seven days old, so an org first
// seen past day 30 still gets its notice period. Orgs that ever had a check, a
// second member or entitlements are not listed at all
// (ListAbandonedAutoPersonalOrgs), and neither are orgs created by hand.
//
// It runs whatever auth.auto_personal_org says now: orgs created while it was
// on are cleaned up after it is turned off.
func (s *Service) SweepAbandonedPersonalOrgs(ctx context.Context, now time.Time) (PersonalOrgSweepResult, error) {
	var result PersonalOrgSweepResult

	orgs, err := s.db.ListAbandonedAutoPersonalOrgs(ctx, now.Add(-personalOrgWarnAge))
	if err != nil {
		return result, err
	}

	for _, org := range orgs {
		warnedAt, warned := s.personalOrgWarnedAt(ctx, org.UID)

		switch {
		case !warned:
			if s.warnAbandonedPersonalOrg(ctx, org, now) {
				result.Warned++
			}
		case now.Sub(org.CreatedAt) >= personalOrgDeleteAge && now.Sub(warnedAt) >= personalOrgNoticePeriod:
			if _, delErr := s.DeleteOrg(ctx, org.Slug, "", DeleteOrgRequest{Slug: org.Slug}, Context{}); delErr != nil {
				slog.ErrorContext(ctx, "personal org cleanup: deletion failed",
					"orgUID", org.UID, "error", delErr)

				continue
			}

			slog.InfoContext(ctx, "abandoned personal organization deleted", "orgUID", org.UID, "orgSlug", org.Slug)

			result.Deleted++
		}
	}

	return result, nil
}

// personalOrgWarnedAt reads when the org's owner was warned, if ever.
func (s *Service) personalOrgWarnedAt(ctx context.Context, orgUID string) (time.Time, bool) {
	entry, err := s.db.GetStateEntry(ctx, &orgUID, personalOrgWarnedKey)
	if err != nil || entry == nil || entry.Value == nil {
		return time.Time{}, false
	}

	warnedAt, parseErr := time.Parse(time.RFC3339, stringFromMap(*entry.Value, keyWarnedAt))
	if parseErr != nil {
		// Recorded but unreadable: count it as sent now, never re-send.
		return entry.CreatedAt, true
	}

	return warnedAt, true
}

// warnAbandonedPersonalOrg records the warning and emails the owner. The
// record is written first, with SetStateEntryIfNotExists, so two job runs
// can never both send it.
func (s *Service) warnAbandonedPersonalOrg(ctx context.Context, org *models.Organization, now time.Time) bool {
	value := models.JSONMap{keyWarnedAt: now.UTC().Format(time.RFC3339)}

	created, err := s.db.SetStateEntryIfNotExists(ctx, &org.UID, personalOrgWarnedKey, &value, nil)
	if err != nil || !created {
		if err != nil {
			slog.ErrorContext(ctx, "personal org cleanup: cannot record the warning", "orgUID", org.UID, "error", err)
		}

		return false
	}

	if org.AutoPersonalOwnerUID == nil {
		return true
	}

	owner, err := s.db.GetUser(ctx, *org.AutoPersonalOwnerUID)
	if err != nil || owner == nil || owner.Email == "" {
		slog.WarnContext(ctx, "personal org cleanup: cannot load the owner to warn", "orgUID", org.UID, "error", err)

		return true
	}

	deleteAt := org.CreatedAt.Add(personalOrgDeleteAge)
	if earliest := now.Add(personalOrgNoticePeriod); deleteAt.Before(earliest) {
		deleteAt = earliest
	}

	baseURL := ""
	if s.fullCfg != nil {
		baseURL = strings.TrimRight(s.fullCfg.Server.BaseURL, "/")
	}

	s.enqueueEmail(ctx, org.UID, owner.Email, "personal-org-cleanup-warning.html", recipient.ForUser(owner),
		map[string]any{
			"OrgName":      org.Name,
			"OrgSlug":      org.Slug,
			"DeleteAt":     deleteAt,
			"DashboardURL": baseURL + config.DashboardBasePath + "/orgs/" + org.Slug + "/checks/new",
		})

	return true
}

// SweepPersonalOrgs adapts SweepAbandonedPersonalOrgs to
// services.PersonalOrgSweeper, the slice the personal_org_cleanup job sees.
func (s *Service) SweepPersonalOrgs(ctx context.Context, now time.Time) (int, int, error) {
	result, err := s.SweepAbandonedPersonalOrgs(ctx, now)

	return result.Warned, result.Deleted, err
}
