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

	"github.com/fclairamb/solidping/server/internal/authhandoff"
	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/defaults"
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

// pendingMembershipParam is the query flag naming the org a completed social
// login was not admitted to. It rides on the handoff redirect, and the
// dashboard forwards it to its no-org page to explain why the login did not
// land in the org.
const pendingMembershipParam = "membershipPending"

// membershipRequestsPath is the dashboard path where org admins review
// pending membership requests — the target of the "New membership request"
// notification email. Keep in sync with the real route:
// web/dash0/src/routes/orgs/$org/organization.requests.tsx registers
// "/orgs/$org/organization/requests" (and organization.tsx wires it into the
// sidebar); it does NOT live under "members".
func membershipRequestsPath(orgSlug string) string {
	return fmt.Sprintf("%s/orgs/%s/organization/requests", config.DashboardBasePath, orgSlug)
}

// Admission rules, as recorded in the member.joined audit payload's `source`.
const (
	joinSourceBootstrap      = "org_bootstrap"
	joinSourceInvitation     = "invitation_at_login"
	joinSourceSlackWorkspace = "slack_workspace"
	joinSourceEmailPattern   = "email_pattern"
	joinSourceAutoJoin       = "auto_join_pattern"
)

// ProviderLoginResult is the outcome of a completed federated login for one
// organization: either a real org-scoped session, or (Pending) a membership
// request awaiting an admin decision plus a session on another org the user
// already belongs to (FallbackOrgSlug) or, failing that, an org-less one.
type ProviderLoginResult struct {
	AccessToken  string
	RefreshToken string
	ExpiresIn    int
	// Pending is true when the user authenticated successfully but was NOT
	// admitted to the org. No session on THAT org exists: the tokens are
	// scoped to FallbackOrgSlug when set, and are otherwise an org-less
	// session (empty org slug, no refresh token).
	Pending bool
	// FallbackOrgSlug is, for a pending login only, the org the user IS an
	// admitted member of that the session was minted on instead (spec
	// 2026-09-25-15). The identity is proven, so a real member signing in from
	// someone else's login page gets a session they can use rather than an
	// org-less one. Empty means the user belongs to no org at all.
	FallbackOrgSlug string
	// PendingOrgSlug is the org the no-org screen should NAME as "your sign-in
	// succeeded but this org hasn't admitted you yet". It is the org's slug
	// whenever a membership request was actually opened, and deliberately EMPTY
	// when rule 6 suppressed one (the demo org, or the SaaS platform default org): naming an org nobody
	// asked for and no admin will ever see a request from is worse than saying
	// nothing. Meaningless unless Pending is true.
	PendingOrgSlug string
	// Requires2FA is true when the user has TOTP enabled: no session was
	// minted (the token fields are empty) and TempToken is the 2FA challenge
	// that Verify2FA / Recovery2FA trade for the session described by the
	// other fields (spec 2026-10-16-14).
	Requires2FA bool
	TempToken   string
}

// Outcome is the ProviderOutcome of this login, completed against the org
// orgSlug for the user userUID. Every federated connector builds its result
// through it, so none can drop a field (the 2FA challenge in particular).
func (r *ProviderLoginResult) Outcome(orgSlug, userUID string) *ProviderOutcome {
	return &ProviderOutcome{
		AccessToken:     r.AccessToken,
		RefreshToken:    r.RefreshToken,
		ExpiresIn:       r.ExpiresIn,
		OrgSlug:         orgSlug,
		UserUID:         userUID,
		Pending:         r.Pending,
		FallbackOrgSlug: r.FallbackOrgSlug,
		PendingOrgSlug:  r.PendingOrgSlug,
		Requires2FA:     r.Requires2FA,
		TempToken:       r.TempToken,
	}
}

// LoginOption carries a provider-verified fact about the login being
// completed into the shared admission policy. Connectors that have nothing to
// attest pass none, which leaves every attestation-driven rule inert.
type LoginOption func(*loginOptions)

// loginOptions is the resolved set of attestations for one login.
type loginOptions struct {
	// method names the connector completing this login ("oidc", "saml",
	// "github", …). It is recorded as auth_method on auth.login_succeeded, so
	// a security review can tell an SSO sign-in from a password one. A
	// connector that does not name itself falls back to AuthMethodOAuth.
	method string

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

// WithLoginMethod names the connector completing this login, for the audit
// trail. Every federated connector should pass one — without it their sign-ins
// are all recorded as the generic "oauth".
func WithLoginMethod(method string) LoginOption {
	return func(o *loginOptions) {
		o.method = method
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
		created, createErr := s.createLoginMembership(ctx, org.UID, user, models.MemberRoleOwner, "", joinSourceBootstrap)
		return created, false, createErr
	}

	// Rule 3: an outstanding invitation for this email admits the user with
	// the invited role, exactly like AcceptInvite would.
	if invite := s.findOrgInvitationForEmail(ctx, org.UID, user.Email); invite != nil {
		created, createErr := s.createLoginMembership(
			ctx, org.UID, user, models.MemberRole(invite.role), invite.inviterUID, joinSourceInvitation,
		)
		if createErr != nil {
			return nil, false, createErr
		}

		// Consume the invitation — it is single-use, like AcceptInvite's.
		_ = s.deleteInvite(ctx, org.UID, invite.key)

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
			created, createErr := s.createLoginMembership(
				ctx, org.UID, user, models.MemberRoleUser, "", joinSourceSlackWorkspace,
			)

			return created, false, createErr
		}
	}

	// Rule 5: the org's own auto-join pattern.
	if pattern := s.orgAutoJoinPattern(ctx, org.UID); pattern != nil && pattern.MatchString(user.Email) {
		created, createErr := s.createLoginMembership(
			ctx, org.UID, user, models.MemberRoleUser, "", joinSourceEmailPattern,
		)

		return created, false, createErr
	}

	// Rule 6: not admitted. Fall back to the membership-request flow.
	slog.InfoContext(ctx, "federated login did not qualify for org membership",
		"orgUID", org.UID, "userUID", user.UID)

	// ...unless the org can never act on a request: the public demo org, or
	// (on SaaS) the operator's own default org. Nobody admits sign-ins there,
	// so a request is noise for admins and a dead end for the user. Such a
	// login still yields an org-less pending session; the dashboard offers
	// the user an organization of their own instead.
	if reason := s.loginJoinRequestSuppressedReason(org); reason != "" {
		slog.InfoContext(ctx, "skipping the join request: the org does not admit sign-ins",
			"orgUID", org.UID, "userUID", user.UID, "orgSlug", org.Slug, "carveOut", reason)

		return nil, true, nil
	}

	if reqErr := s.ensureMembershipRequestForLogin(ctx, org, user.UID); reqErr != nil {
		return nil, false, reqErr
	}

	return nil, true, nil
}

// Reasons an org never accepts membership requests (see
// loginJoinRequestSuppressedReason).
const (
	suppressReasonDemoOrg     = "demo-org"
	suppressReasonSaaSDefault = "saas-default-org"
)

// loginJoinRequestSuppressedReason returns a non-empty carve-out name when
// rule 6 must end in a silent org-less session instead of a membership
// request, and "" when a request should be filed.
//
//   - The demo org (config demo.org_slug, any deployment mode, new or
//     returning user, whether or not demo.enabled is set: the org and its stale
//     requests exist in prod regardless of the flag). Nobody approves them.
//   - The SaaS platform default org. It is the operator's own org: people
//     join by invite. The dashboard itself sends org-less users to
//     /orgs/default/login, so a returning user there did NOT ask on purpose.
//     Self-hosted `default` keeps the request flow: it is usually the one real
//     org and a colleague's sign-in should reach its admins.
func (s *Service) loginJoinRequestSuppressedReason(org *models.Organization) string {
	if s.fullCfg == nil {
		return ""
	}

	if org.Slug == s.fullCfg.Demo.ResolvedOrgSlug() {
		return suppressReasonDemoOrg
	}

	if s.fullCfg.Deployment.Mode == config.DeploymentModeSaaS && org.Slug == defaults.Organization {
		return suppressReasonSaaSDefault
	}

	return ""
}

// CompleteOrgLogin is the shared tail of every federated callback: run the
// admission rules, replay the cross-org auto-join, then mint the session that
// matches the outcome.
//
// A pending outcome never grants anything on the org that refused the login.
// The identity is proven, though, so when the user is an admitted member of
// another org the session is a normal one on that org (refresh token
// included, FallbackOrgSlug set): a real member who started from someone
// else's login page must not be handed a session they cannot use (spec
// 2026-09-25-15). A user with no membership at all gets the org-less access
// token (no refresh token) that lets the dashboard show the request-access
// surface without any org-scoped API access.
//
// A user with TOTP enabled gets no session at all here: the result carries
// Requires2FA and a 2FA temp token naming the org and role the session would
// have had, and Verify2FA / Recovery2FA mint it once the code is accepted
// (spec 2026-10-16-14). Admission (membership requests, invitations,
// auto-join) still runs before the code, as for an LDAP login: it only ever
// benefits the real account, and nothing usable is issued without the code.
// Passkeys are the only sign-in that skips TOTP, because a passkey already is
// a second factor (passkey_service.go FinishLogin, which never comes here).
func (s *Service) CompleteOrgLogin(
	ctx context.Context, org *models.Organization, user *models.User, opts ...LoginOption,
) (*ProviderLoginResult, error) {
	resolved := resolveLoginOptions(opts)

	member, pending, err := s.JoinOrgViaLogin(ctx, org, user, opts...)
	if err != nil {
		return nil, fmt.Errorf("failed to ensure membership: %w", err)
	}

	// Auto-join any *other* org whose pattern matches (unchanged behavior).
	// It runs before the pending fallback below on purpose: an org this very
	// login just auto-joined is a membership the session can land on.
	s.autoJoinMatchingOrgs(ctx, user.UID, user.Email)

	if pending {
		return s.completePendingLogin(ctx, org, user, resolved.method)
	}

	role := RoleSuperAdmin
	if member != nil {
		role = string(member.Role)
	}

	if user.TOTPEnabled {
		return s.twoFAChallenge(user, org.Slug, role, resolved.method)
	}

	// No explicit Context to thread here — GenerateTokensForOAuth falls back
	// to whatever the request-meta middleware parked on ctx (source IP, user
	// agent); a federated callback is an ordinary HTTP request, so that is
	// already populated.
	tokens, err := s.GenerateTokensForOAuth(ctx, user, org, role, resolved.method, Context{})
	if err != nil {
		return nil, fmt.Errorf("failed to generate tokens: %w", err)
	}

	return &ProviderLoginResult{
		AccessToken:  tokens.AccessToken,
		RefreshToken: tokens.RefreshToken,
		ExpiresIn:    tokens.ExpiresIn,
	}, nil
}

// completePendingLogin is CompleteOrgLogin's tail for a login the target org
// did not admit: choose the session (an org the user already belongs to, else
// org-less), then either mint it or, for a TOTP user, challenge for the code.
func (s *Service) completePendingLogin(
	ctx context.Context, org *models.Organization, user *models.User, method string,
) (*ProviderLoginResult, error) {
	// Name the org only when there is a request an admin can act on.
	pendingOrgSlug := ""
	if s.loginJoinRequestSuppressedReason(org) == "" {
		pendingOrgSlug = org.Slug
	}

	fallback := s.fallbackMemberOrg(ctx, user.UID)

	var (
		result *ProviderLoginResult
		err    error
	)

	switch {
	case user.TOTPEnabled:
		// The same choice the minting below makes, carried in the temp token:
		// completeLoginAfter2FA mints on the fallback org, or (empty slug) the
		// same org-less token pendingSession hands out.
		slug, role := "", ""
		if fallback != nil {
			slug, role = fallback.Organization.Slug, string(fallback.Role)
		}

		result, err = s.twoFAChallenge(user, slug, role, method)
		if err != nil {
			return nil, err
		}

		result.FallbackOrgSlug = slug
	default:
		if fallback != nil {
			result = s.fallbackMemberSession(ctx, user, fallback, method)
		}

		if result == nil {
			result, err = s.pendingSession(ctx, user)
			if err != nil {
				return nil, err
			}
		}
	}

	result.Pending = true
	result.PendingOrgSlug = pendingOrgSlug

	return result, nil
}

// twoFAChallenge is the result handed out instead of a session when the user
// has TOTP enabled: no token, only a 2FA temp token naming the org and role
// the session will be minted on once the code is accepted.
func (s *Service) twoFAChallenge(user *models.User, orgSlug, role, method string) (*ProviderLoginResult, error) {
	tempToken, err := s.generate2FATempToken(user.UID, orgSlug, role, method)
	if err != nil {
		return nil, fmt.Errorf("failed to generate 2FA token: %w", err)
	}

	return &ProviderLoginResult{Requires2FA: true, TempToken: tempToken}, nil
}

// fallbackMemberSession mints, for a login the target org refused, a normal
// org-scoped session on member's org, an org the user IS an admitted member of
// (chosen by fallbackMemberOrg). It returns nil when anything goes wrong on the
// way: the caller then falls back to the org-less session, and the login never
// fails because of this.
func (s *Service) fallbackMemberSession(
	ctx context.Context, user *models.User, member *models.OrganizationMember, method string,
) *ProviderLoginResult {
	tokens, err := s.GenerateTokensForOAuth(ctx, user, member.Organization, string(member.Role), method, Context{})
	if err != nil {
		slog.WarnContext(ctx, "could not mint a session on the user's own org after a refused login",
			"userUID", user.UID, "orgUID", member.OrganizationUID, "error", err)

		return nil
	}

	return &ProviderLoginResult{
		AccessToken:     tokens.AccessToken,
		RefreshToken:    tokens.RefreshToken,
		ExpiresIn:       tokens.ExpiresIn,
		FallbackOrgSlug: member.Organization.Slug,
	}
}

// fallbackMemberOrg picks the org a refused federated login lands on instead:
// the org of the user's most recent refresh token while the user is still a
// member of it, else the most recently joined membership (ListMembersByUser is
// ordered created_at DESC). That is resolveDefaultOrg's order, and the one the
// dashboard's pickAccessibleOrg follows (spec 2026-09-08-01 §3). Unlike
// resolveDefaultOrg, a refresh-token row never wins on its own: a stale token
// for an org the user left must not mint a session there. Memberships whose
// org is gone are skipped. Nil means no usable membership (or a lookup error).
func (s *Service) fallbackMemberOrg(ctx context.Context, userUID string) *models.OrganizationMember {
	members, err := s.db.ListMembersByUser(ctx, userUID)
	if err != nil {
		slog.WarnContext(ctx, "could not list memberships for a refused login", "userUID", userUID, "error", err)

		return nil
	}

	usable := make([]*models.OrganizationMember, 0, len(members))

	for _, member := range members {
		if member.Organization != nil && member.Organization.UID != "" && member.Organization.DeletedAt == nil {
			usable = append(usable, member)
		}
	}

	if len(usable) == 0 {
		return nil
	}

	if tokens, tokErr := s.db.ListUserTokensByType(ctx, userUID, models.TokenTypeRefresh); tokErr == nil {
		var mostRecent *models.UserToken

		for _, token := range tokens {
			if token.OrganizationUID != nil && (mostRecent == nil || token.CreatedAt.After(mostRecent.CreatedAt)) {
				mostRecent = token
			}
		}

		if mostRecent != nil {
			for _, member := range usable {
				if member.OrganizationUID == *mostRecent.OrganizationUID {
					return member
				}
			}
		}
	}

	return usable[0]
}

// pendingSession mints the org-less session handed to a user who authenticated
// but was not admitted: an access token with an empty org slug and no role,
// and deliberately no refresh token — the same shape completeLogin produces
// for a password login by a user with no membership.
func (s *Service) pendingSession(ctx context.Context, user *models.User) (*ProviderLoginResult, error) {
	accessToken, err := s.generateAccessToken(user.UID, "", "", "", user.Demo)
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
// It is also the single point at which a federated login creates a membership,
// so it is where member.joined is recorded — without it an SSO org's entire
// membership would appear in the trail from nowhere (spec 2026-08-21-09).
// `source` says WHICH admission rule let the user in, which is the fact an
// access review actually needs.
func (s *Service) createLoginMembership(
	ctx context.Context, orgUID string, user *models.User, role models.MemberRole, inviterUID, source string,
) (*models.OrganizationMember, error) {
	if err := s.CheckMembershipSlot(ctx, orgUID); err != nil {
		return nil, err
	}

	member := models.NewOrganizationMember(orgUID, user.UID, role)
	now := time.Now()
	member.JoinedAt = &now

	if inviterUID != "" {
		member.InvitedByUID = &inviterUID
		member.InvitedAt = &now
	}

	if err := s.db.CreateOrganizationMember(ctx, member); err != nil {
		return nil, fmt.Errorf("failed to create member: %w", err)
	}

	s.recordMemberJoined(ctx, orgUID, user.UID, user.Email, role, source)

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

// handoffCompletePath is the dashboard route that redeems a handoff code
// (web/dash0/src/routes/auth.complete.tsx). Every federated login ends there.
const handoffCompletePath = config.DashboardBasePath + "/auth/complete"

// handoffCodeParam is the query parameter carrying the single-use handoff
// code. Named `code` on purpose: the dashboard's analytics redaction already
// scrubs that name from every URL it reports (lib/analytics-redaction.ts).
const handoffCodeParam = "code"

// handoffRedirect is where a provider callback sends the browser once the
// session is stored under a handoff code (spec 2026-09-25-12): the dashboard's
// handoff route, carrying the code and nothing else secret.
//
// membershipPending names the org whose admission is pending (see
// ProviderOutcome.PendingOrgSlug). It is not a secret and rides in the URL so
// the dashboard can still say "your request was sent" if the exchange fails.
// Empty means "do not name an org": no request was opened, so the dashboard
// must not render the "a join request was sent to its admins" alert.
//
// The session tokens are never in this URL. Neither is the login's own
// redirect_uri: it travels inside the sealed handoff payload as returnTo, so
// this redirect always stays on our own dashboard route.
func handoffRedirect(code, membershipPending string) string {
	query := url.Values{}
	query.Set(handoffCodeParam, code)

	if membershipPending != "" {
		query.Set(pendingMembershipParam, membershipPending)
	}

	return handoffCompletePath + "?" + query.Encode()
}

// ProviderOutcome is what every federated callback knows once the login is
// done: the session CompleteOrgLogin minted and whether the org admitted the
// user. It is the input of the shared redirect tail.
type ProviderOutcome struct {
	AccessToken  string
	RefreshToken string
	ExpiresIn    int
	// OrgSlug is the org the login was completed against.
	OrgSlug string
	UserUID string
	// Pending is true when the org did not admit the user: the tokens above
	// are then scoped to FallbackOrgSlug, or org-less (no refresh token) when
	// that is empty.
	Pending bool
	// FallbackOrgSlug is the org a pending login's session was minted on
	// instead — see ProviderLoginResult.FallbackOrgSlug. Meaningless unless
	// Pending is true.
	FallbackOrgSlug string
	// PendingOrgSlug is the org to NAME on the no-org screen — see
	// ProviderLoginResult.PendingOrgSlug. Meaningless unless Pending is true.
	PendingOrgSlug string
	// Requires2FA / TempToken: see ProviderLoginResult. When set, the token
	// fields are empty and the handoff carries the challenge instead.
	Requires2FA bool
	TempToken   string
}

// handoffSession is the session a handoff code carries for this outcome. A
// pending outcome's session is never scoped to the org the login targeted: it
// is scoped to the fallback org the user belongs to, or org-less without one.
// Either way it still names the pending org.
func (o *ProviderOutcome) handoffSession(returnTo string) *authhandoff.Session {
	session := &authhandoff.Session{
		AccessToken:  o.AccessToken,
		RefreshToken: o.RefreshToken,
		ExpiresIn:    o.ExpiresIn,
		UserUID:      o.UserUID,
		ReturnTo:     returnTo,
	}

	if o.Requires2FA {
		// No session exists yet: the code carries the challenge only.
		session.AccessToken = ""
		session.RefreshToken = ""
		session.ExpiresIn = 0
		session.Requires2FA = true
		session.TempToken = o.TempToken
	}

	if o.Pending {
		session.MembershipPending = o.PendingOrgSlug
		session.OrgSlug = o.FallbackOrgSlug
	} else {
		session.OrgSlug = o.OrgSlug
	}

	return session
}

// RedirectWithHandoff stores the outcome's session under a single-use handoff
// code and redirects the browser to the dashboard's handoff route with that
// code — never with a token. It also sets the SPA session cookie, as every
// federated callback did before (the embedded MCP OAuth consent flow
// authenticates with it); a pending session is either org-less or scoped to
// an org the user already belongs to, so that cookie never grants anything on
// the org that refused the login.
//
// It is exported for callbacks that live outside this package (the Slack
// app-install callback). baseURL, when set, makes the target absolute;
// callers that redirect within the same origin pass "". returnTo is where the
// dashboard should land afterwards, subject to its own guards; a value that is
// not a same-origin relative path is replaced by the org's default landing
// before it is sealed (sanitizePostLoginRedirect), whoever the caller is.
//
// On error nothing has been written to the response: the caller decides how
// to report the failure.
func RedirectWithHandoff(
	writer http.ResponseWriter, req *http.Request, dbService db.Service,
	baseURL string, outcome *ProviderOutcome, returnTo string,
) error {
	session := outcome.handoffSession(sanitizePostLoginRedirect(req.Context(), returnTo, outcome.OrgSlug))

	code, err := authhandoff.Issue(req.Context(), dbService, session)
	if err != nil {
		return fmt.Errorf("issue handoff code: %w", err)
	}

	// A 2FA challenge has no session yet: the /2fa/verify handler sets the
	// cookie once the code is accepted.
	if session.AccessToken != "" {
		setAccessTokenCookie(writer, req, session.AccessToken, session.ExpiresIn)
	}

	http.Redirect(writer, req, baseURL+handoffRedirect(code, session.MembershipPending), http.StatusFound)

	return nil
}

// finishProviderCallback is the shared redirect tail of every federated
// callback handler (google, github, gitlab, microsoft, discord, slack, oidc,
// saml). Admitted or pending, the browser goes to the dashboard's handoff
// route with a single-use code; the dashboard redeems it and then lands on
// returnTo (admitted), the user's own org (pending, with a fallback org), or
// the no-org request-access surface (pending, no membership at all).
//
// returnTo is the redirect_uri the login was started with, already passed
// through sanitizePostLoginRedirect by the callback. If the handoff cannot be
// stored, the browser goes back there with the generic sign-in error, like any
// other callback failure.
func finishProviderCallback(
	writer http.ResponseWriter, req *http.Request, dbService db.Service,
	provider, returnTo string, outcome *ProviderOutcome,
) error {
	returnTo = sanitizePostLoginRedirect(req.Context(), returnTo, outcome.OrgSlug)

	if err := RedirectWithHandoff(writer, req, dbService, "", outcome, returnTo); err != nil {
		description := logOAuthFailure(req, provider, err)
		redirectOAuthError(writer, req, returnTo, OAuthCodeFailed, description)
	}

	return nil
}

// redirectOAuthError sends the browser to baseURI with the OAuth error
// parameters the dashboard understands. Every provider's redirectWithError
// ends here.
//
// baseURI is normally already sanitized by the caller (against the login's
// org); this is the last line of defense, so anything that is not a
// same-origin relative path goes to "/" instead of being echoed.
func redirectOAuthError(writer http.ResponseWriter, req *http.Request, baseURI, code, description string) {
	if !isSafePostLoginRedirect(baseURI) {
		baseURI = "/"
	}

	parsedURL, err := url.Parse(baseURI)
	if err != nil {
		parsedURL, _ = url.Parse("/")
	}

	query := parsedURL.Query()
	query.Set("error", code)
	query.Set("error_description", description)
	parsedURL.RawQuery = query.Encode()

	http.Redirect(writer, req, parsedURL.String(), http.StatusFound)
}
