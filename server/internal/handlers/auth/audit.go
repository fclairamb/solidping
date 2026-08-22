package auth

import (
	"context"

	"github.com/fclairamb/solidping/server/internal/audit"
	"github.com/fclairamb/solidping/server/internal/db/models"
)

// Audit payload keys owned by this package.
const (
	auditKeyMethod = "auth_method"
	auditKeyEmail  = "email"
	auditKeyRole   = "role"
	auditKeySource = "source"

	// Audit target types owned by this package. Named because they recur and
	// goconst (rightly) refuses a third bare literal.
	auditTargetUser   = "user"
	auditTargetToken  = "token"
	auditTargetMember = "member"
)

// Reasons recorded on auth.login_failed. Deliberately coarse — the trail must
// not become a user-enumeration oracle for anyone who can read it but could not
// already list the org's members.
const (
	auditReasonInvalidCredentials = "invalid_credentials"
)

// auditActorCtx derives the audit context for an auth operation.
//
// Authentication is the one place the request-meta middleware cannot carry the
// whole story: on the way IN to a login there is no authenticated principal
// yet, so the middleware only parked the IP and user agent. This folds the
// now-known user onto that, and prefers the handler's explicit authContext
// (which is what the OAuth/passkey callbacks have) when it carries a value.
func auditActorCtx(ctx context.Context, userUID string, authContext Context) context.Context {
	actor := audit.ActorFromContext(ctx)

	if authContext.RemoteAddr != "" {
		actor.SourceIP = authContext.RemoteAddr
	}

	if authContext.UserAgent != "" {
		actor.UserAgent = authContext.UserAgent
	}

	if userUID != "" {
		actor.UserUID = userUID
		actor.Type = models.ActorTypeUser
	}

	return audit.WithActor(ctx, actor)
}

// recordLoginFailed writes (or folds into) an auth.login_failed event.
//
// orgSlug is what the caller typed, which may be empty or may not resolve —
// the events table is org-scoped, so an attempt that cannot be attributed to
// an org has nowhere to be recorded and is simply not. That is a real gap and
// a deliberate one: inventing a global bucket would mean an unauthenticated
// stranger could write rows nobody's retention policy owns.
func (s *Service) recordLoginFailed(ctx context.Context, orgSlug, email, reason string) {
	if orgSlug == "" {
		return
	}

	org, err := s.db.GetOrganizationBySlug(ctx, orgSlug)
	if err != nil || org == nil {
		return
	}

	audit.RecordFailedLogin(ctx, s.db, org.UID, email, reason)
}

// sessionActor is who a newly minted session belongs to. Email is optional —
// it is only the audit trail's readable label, and recordLoginSucceeded fills
// it in best-effort when a caller has just the UID.
type sessionActor struct {
	UID   string
	Email string
}

// actorFromUser builds a sessionActor from a loaded user.
func actorFromUser(user *models.User) sessionActor {
	if user == nil {
		return sessionActor{}
	}

	return sessionActor{UID: user.UID, Email: user.Email}
}

// recordLoginSucceeded writes the auth.login_succeeded event for one newly
// minted session.
//
// Called from exactly one place — Service.startSession, which is the single
// point at which a refresh-token row (i.e. a real session) comes into
// existence. That indirection is the whole design: the first cut of this spec
// emitted from completeLogin on the theory that it was "the single funnel",
// and it was not. Federated logins go through GenerateTokensForOAuth, 2FA
// logins through completeLoginAfter2FA, and org switches, registration
// confirmations, invitation acceptances and org creation each mint their own
// session — so an SSO-only or 2FA-enforcing organization had an audit log
// containing zero logins. TestEverySessionMintingPathGoesThroughStartSession
// keeps it that way structurally.
func (s *Service) recordLoginSucceeded(
	ctx context.Context,
	actor sessionActor,
	org *models.Organization,
	role, method string,
	authContext Context,
) {
	if org == nil || actor.UID == "" {
		return
	}

	email := actor.Email
	if email == "" {
		// Best-effort: a caller that only had a UID (mintOrgSession) still
		// produces a readable row. A failed lookup costs the label, never the
		// event — an audit entry with no email beats no entry at all.
		if user, err := s.db.GetUser(ctx, actor.UID); err == nil && user != nil {
			email = user.Email
		}
	}

	audit.Record(auditActorCtx(ctx, actor.UID, authContext), s.db, org.UID,
		models.EventTypeAuthLoginSucceeded,
		audit.Target{Type: auditTargetUser, UID: actor.UID, Name: email},
		models.JSONMap{
			auditKeyMethod: method,
			auditKeyEmail:  email,
			auditKeyRole:   role,
		})
}

// Authentication methods recorded on auth.login_succeeded. The federated
// values are supplied by each connector through WithLoginMethod; the rest are
// the local session-minting paths, which are just as much "a session was
// issued" facts as an SSO callback is.
const (
	// AuthMethodPassword is a local password login.
	AuthMethodPassword = "password"
	// AuthMethodLDAP is a login verified against the configured LDAP directory.
	AuthMethodLDAP = "ldap"
	// AuthMethodPasskey is a WebAuthn login.
	AuthMethodPasskey = "passkey"
	// AuthMethodOAuth is the fallback for a federated connector that did not
	// name itself.
	AuthMethodOAuth = "oauth"
	// AuthMethodInvitation is a session minted by accepting an invitation.
	AuthMethodInvitation = "invitation"
	// AuthMethodRegistration is a session minted by confirming a registration.
	AuthMethodRegistration = "registration"
	// AuthMethodSwitchOrg is a session minted for a different organization the
	// user already belongs to.
	AuthMethodSwitchOrg = "switch_org"
	// AuthMethodOrgSession is a session re-minted because the org itself
	// changed under the caller (creation, slug rename).
	AuthMethodOrgSession = "org_session"
	// SecondFactorTOTP / SecondFactorRecoveryCode are appended to the
	// first-factor method, e.g. "password+totp", so the trail records BOTH
	// factors rather than losing the first one at the 2FA hand-off.
	SecondFactorTOTP         = "totp"
	SecondFactorRecoveryCode = "recovery_code"
)

// authMethods is the canonical set of BASE auth_method values this server can
// record — the single source of truth the event catalog is checked against.
//
// It deliberately lives in production code rather than in the test: a list
// restated in a test is a second source of truth, and the failure mode this
// guards (documentation promising a value nothing can emit) is exactly what
// two lists drifting apart produces.
//
// 2FA-completed logins are NOT listed: they are a base method with a second
// factor appended by withSecondFactor ("password+totp"), not values of their
// own.
func authMethods() []string {
	return []string{
		// Local first factors.
		AuthMethodPassword,
		AuthMethodLDAP,
		AuthMethodPasskey,
		// Federated connectors — the same constants the signup analytics use,
		// so a connector cannot be labeled one way here and another there.
		signupMethodOIDC,
		signupMethodSAML,
		signupMethodGitHub,
		signupMethodGitLab,
		signupMethodGoogle,
		signupMethodMicrosoft,
		signupMethodDiscord,
		signupMethodSlack,
		// Fallback for a connector that did not name itself.
		AuthMethodOAuth,
		// Local session-minting paths.
		AuthMethodInvitation,
		AuthMethodRegistration,
		AuthMethodSwitchOrg,
		AuthMethodOrgSession,
	}
}

// federatedAuthMethods is the subset that must reach the audit trail through a
// connector calling WithLoginMethod. Anything here that no connector names
// would be recorded as the generic AuthMethodOAuth instead.
func federatedAuthMethods() map[string]string {
	return map[string]string{
		"signupMethodOIDC":      signupMethodOIDC,
		"signupMethodSAML":      signupMethodSAML,
		"signupMethodGitHub":    signupMethodGitHub,
		"signupMethodGitLab":    signupMethodGitLab,
		"signupMethodGoogle":    signupMethodGoogle,
		"signupMethodMicrosoft": signupMethodMicrosoft,
		"signupMethodDiscord":   signupMethodDiscord,
		"signupMethodSlack":     signupMethodSlack,
	}
}

// secondFactors is the set of second-factor suffixes withSecondFactor appends.
func secondFactors() []string {
	return []string{SecondFactorTOTP, SecondFactorRecoveryCode}
}

// withSecondFactor renders the combined method for a 2FA-completed login.
func withSecondFactor(firstFactor, secondFactor string) string {
	if firstFactor == "" {
		firstFactor = AuthMethodPassword
	}

	return firstFactor + "+" + secondFactor
}

// recordMemberJoined writes the member.joined event for a membership row that
// a login flow created on the fly (federated auto-join, invite consumed at
// login, org bootstrap). Without it an SSO org's entire membership appears
// out of nowhere.
func (s *Service) recordMemberJoined(
	ctx context.Context, orgUID, userUID, email string, role models.MemberRole, source string,
) {
	if userUID == "" {
		return
	}

	audit.Record(auditActorCtx(ctx, userUID, Context{}), s.db, orgUID,
		models.EventTypeMemberJoined,
		audit.Target{Type: auditTargetMember, UID: userUID, Name: email},
		models.JSONMap{
			auditKeyEmail:  email,
			auditKeyRole:   string(role),
			auditKeySource: source,
		})
}
