package auth

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
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

// registrationSlackAutoJoinKey is the org parameter that switches off the
// Slack workspace attestation rule. Absent means enabled: an org that exists
// *because* a Slack workspace was linked to it should let that workspace's
// members in by default. Set it to false to fall back to the pre-attestation
// behavior (email pattern / membership request).
const registrationSlackAutoJoinKey = "registration.slack_workspace_auto_join"

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

// LoginOption carries a provider-verified fact about the login being
// completed into the shared admission policy. Connectors that have nothing to
// attest pass none, which leaves every attestation-driven rule inert.
type LoginOption func(*loginOptions)

// loginOptions is the resolved set of attestations for one login.
type loginOptions struct {
	// slackTeamID is the Slack workspace ID Slack itself returned in the
	// OAuth token exchange. It is an *attestation*: the exchange (and the
	// openid.connect.userInfo call that follows it) can only succeed for a
	// member of that workspace, so its presence proves workspace membership.
	// It never comes from a query parameter, a form field, or any other
	// browser-supplied value.
	slackTeamID string
}

// WithSlackWorkspace attests that the user completing this login is a member
// of the given Slack workspace (team ID), as proven by a Slack OAuth token
// exchange this server performed itself.
func WithSlackWorkspace(teamID string) LoginOption {
	return func(o *loginOptions) {
		o.slackTeamID = teamID
	}
}

// resolveLoginOptions folds the variadic options into one struct.
func resolveLoginOptions(opts []LoginOption) loginOptions {
	var resolved loginOptions

	for _, opt := range opts {
		if opt != nil {
			opt(&resolved)
		}
	}

	return resolved
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
//  2. Zero-member org  → joins as OWNER (bootstrap / self-hosted onboarding);
//     whoever brings an org into existence owns it.
//  3. Pending invite for the email → joins with the invited role, invite consumed.
//  4. Slack workspace attestation for the workspace THIS org is linked to →
//     joins as user (see slackWorkspaceAdmits).
//  5. Email matches registration.email_pattern → joins as user.
//  6. Otherwise        → NO membership; a membership_requests row is created or
//     re-opened and pending=true is returned.
//
// The MaxUsers cap (CheckMembershipSlot) is enforced here, once, ahead of any
// membership creation.
//
// A nil member with pending=false means "no membership row is needed" — only
// the super-admin case produces that.
func (s *Service) JoinOrgViaLogin(
	ctx context.Context, org *models.Organization, user *models.User, opts ...LoginOption,
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

	// Rule 2: bootstrap. The first ever member of an org becomes its OWNER.
	//
	// This is the connector org-creation path: Slack/Discord/OIDC
	// findOrCreateOrganization mints an empty org and then funnels through
	// here, so "whoever caused the org to exist owns it" holds for every
	// connector without each one growing its own membership code (spec
	// 2026-08-08-11). It matches CreateOrg, which makes its caller the owner.
	members, err := s.db.ListMembersByOrg(ctx, org.UID)
	if err != nil {
		return nil, false, fmt.Errorf("failed to list members: %w", err)
	}

	if len(members) == 0 {
		created, createErr := s.createLoginMembership(ctx, org.UID, user.UID, models.MemberRoleOwner, "")
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

	// Rule 4: the identity provider attested that this user belongs to the
	// external workspace this very org is linked to.
	if s.slackWorkspaceAdmits(ctx, org, resolveLoginOptions(opts).slackTeamID) {
		// The seat cap is checked first so a capped org falls through to the
		// remaining rules (and ultimately to a membership request) instead of
		// failing the whole login — but it is never bypassed.
		if slotErr := s.CheckMembershipSlot(ctx, org.UID); slotErr != nil {
			slog.WarnContext(ctx, "slack workspace auto-join blocked by the membership cap",
				"orgUID", org.UID, "userUID", user.UID, "error", slotErr)
		} else {
			created, createErr := s.createLoginMembership(ctx, org.UID, user.UID, models.MemberRoleUser, "")

			return created, false, createErr
		}
	}

	// Rule 5: the org's own auto-join pattern.
	if pattern := s.orgAutoJoinPattern(ctx, org.UID); pattern != nil && pattern.MatchString(user.Email) {
		created, createErr := s.createLoginMembership(ctx, org.UID, user.UID, models.MemberRoleUser, "")
		return created, false, createErr
	}

	// Rule 6: not admitted. Fall back to the membership-request flow.
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
	ctx context.Context, org *models.Organization, user *models.User, opts ...LoginOption,
) (*ProviderLoginResult, error) {
	member, pending, err := s.JoinOrgViaLogin(ctx, org, user, opts...)
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
// means "this org has no rule-5 path", never "match everything". Unsafe
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

// slackWorkspaceAdmits answers the one question rule 4 asks: did Slack just
// attest that this user belongs to the workspace THIS organization is linked
// to?
//
// Everything about the decision is server-side:
//
//   - teamID is the workspace ID returned by the Slack OAuth token exchange
//     this server performed. Slack only completes that exchange (and the
//     openid.connect.userInfo call after it) for a member of that workspace,
//     so it is an attestation of membership — not a claim the browser made.
//     An empty teamID means "no attestation": every non-Slack connector.
//   - The org↔workspace link is read from organization_providers, the single
//     source of truth for that mapping. The lookup filters soft-deleted rows,
//     so a revoked/unlinked workspace admits nobody.
//   - The resolved link must point at the org actually being joined. The org
//     slug in the login URL is attacker-controlled, so a member of workspace
//     A presenting that attestation while asking for org B (linked to
//     workspace B, or to nothing) is refused here and falls through to the
//     membership-request path.
//   - An org can opt out entirely via registration.slack_workspace_auto_join,
//     which fails closed when it is present but unreadable (see
//     slackWorkspaceAutoJoinEnabled).
//
// Slack guests (single/multi-channel) pass the OAuth exactly like full
// members and openid.connect.userInfo does not expose guest status, so V1
// admits them; a users.info-based downgrade is a documented follow-up.
func (s *Service) slackWorkspaceAdmits(
	ctx context.Context, org *models.Organization, teamID string,
) bool {
	if teamID == "" {
		return false
	}

	if !s.slackWorkspaceAutoJoinEnabled(ctx, org.UID) {
		slog.InfoContext(ctx, "slack workspace auto-join disabled for org",
			"orgUID", org.UID, "teamID", teamID)

		return false
	}

	provider, err := s.db.GetOrganizationProviderByProviderID(ctx, models.ProviderTypeSlack, teamID)
	if err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			slog.WarnContext(ctx, "failed to resolve slack workspace link during login",
				"orgUID", org.UID, "teamID", teamID, "error", err)
		}

		return false
	}

	if provider == nil {
		return false
	}

	if provider.OrganizationUID != org.UID {
		// Cross-tenant attempt (or simply an org that is linked to another
		// workspace): the attestation proves membership of a workspace that is
		// not this org's.
		slog.WarnContext(ctx, "slack workspace attestation does not belong to the org being joined",
			"orgUID", org.UID, "attestedOrgUID", provider.OrganizationUID, "teamID", teamID)

		return false
	}

	return true
}

// slackWorkspaceAutoJoinEnabled reads the org's opt-out switch, distinguishing
// three states because they must not fail the same way:
//
//   - Absent → enabled. The org exists because of its Slack workspace link, so
//     admitting that workspace's members is the documented default.
//   - Present and decodable → whatever it says.
//   - Present but NOT decodable ("off", "no", "disabled", a null value…) →
//     DISABLED, with a warning naming the org and the offending value. Someone
//     wrote this parameter to turn admission off; honoring the intent (deny) is
//     the only safe reading of a switch we cannot parse, and the warning makes
//     the typo diagnosable instead of silent.
//
// A read *error* is different again: a transient DB hiccup must not lock every
// workspace member out of every org, so it keeps the default (enabled) and is
// logged.
func (s *Service) slackWorkspaceAutoJoinEnabled(ctx context.Context, orgUID string) bool {
	param, err := s.db.GetOrgParameter(ctx, orgUID, registrationSlackAutoJoinKey)
	if err != nil {
		slog.WarnContext(ctx, "failed to read slack workspace auto-join parameter; keeping the default",
			"orgUID", orgUID, "key", registrationSlackAutoJoinKey, "error", err)

		return true
	}

	if param == nil {
		return true
	}

	raw := param.Value["value"]

	enabled, ok := parseParamBool(raw)
	if !ok {
		slog.WarnContext(ctx, "unreadable slack workspace auto-join parameter; treating it as disabled",
			"orgUID", orgUID, "key", registrationSlackAutoJoinKey,
			"value", fmt.Sprintf("%v", raw))

		return false
	}

	return enabled
}

// parseParamBool decodes a boolean org parameter. Values round-trip through
// JSON(B) and may arrive as a real bool, as a string ("false", "0", …) or as a
// number, so all three are accepted. It returns (value, decoded): decoded is
// false for "present but not decodable", in which case value is meaningless
// and it is up to the caller to decide which way an unreadable switch fails.
func parseParamBool(raw any) (bool, bool) {
	switch typed := raw.(type) {
	case bool:
		return typed, true
	case string:
		parsed, err := strconv.ParseBool(strings.TrimSpace(typed))
		if err != nil {
			return false, false
		}

		return parsed, true
	case float64:
		return typed != 0, true
	case int:
		return typed != 0, true
	default:
		return false, false
	}
}

// ensureMembershipRequestForLogin creates — or re-opens — the membership
// request that represents "this authenticated user would like into this org".
// A request already pending is left alone; a rejected one keeps its rejected
// state (the admin's decision and its cooldown stand, and the no-org screen
// surfaces rejected requests too); a canceled one is re-opened.
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

// RedirectPendingMembership sends a browser whose login completed WITHOUT
// admission to the shared request-access surface, carrying the org-less
// session — the same treatment finishProviderCallback gives a pending
// federated login, exported for callbacks that live outside this package (the
// Slack app-install callback). baseURL, when set, makes the target absolute;
// callers that redirect within the same origin can pass "".
func RedirectPendingMembership(
	writer http.ResponseWriter, req *http.Request,
	baseURL, orgSlug, accessToken string, expiresIn int,
) {
	setAccessTokenCookie(writer, accessToken, expiresIn)
	http.Redirect(writer, req,
		baseURL+pendingMembershipRedirect(orgSlug, accessToken, expiresIn),
		http.StatusFound)
}

// finishProviderCallback is the shared redirect tail of every federated
// callback handler. It hands the browser either the provider's normal success
// redirect or — when the org did not admit the user — the pending
// request-access surface, and sets the SPA session cookie in both cases (the
// pending session is org-less, so it grants nothing org-scoped).
func finishProviderCallback(
	writer http.ResponseWriter, req *http.Request,
	successURL, orgSlug, accessToken string, expiresIn int, pending bool,
) error {
	redirectURL := successURL
	if pending {
		redirectURL = pendingMembershipRedirect(orgSlug, accessToken, expiresIn)
	}

	setAccessTokenCookie(writer, accessToken, expiresIn)
	http.Redirect(writer, req, redirectURL, http.StatusFound)

	return nil
}
