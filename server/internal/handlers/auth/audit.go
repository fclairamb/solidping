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
	auditTargetUser  = "user"
	auditTargetToken = "token"
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

// recordLoginSucceeded writes the auth.login_succeeded event.
//
// completeLogin is the single funnel every successful interactive login goes
// through — password, LDAP, passkey and every OAuth/OIDC provider — so one
// emission here covers all of them and cannot drift out of sync with a new
// provider added later.
func (s *Service) recordLoginSucceeded(
	ctx context.Context,
	user *models.User,
	resolvedOrg *models.Organization,
	role, method string,
	authContext Context,
) {
	if resolvedOrg == nil {
		return
	}

	audit.Record(auditActorCtx(ctx, user.UID, authContext), s.db, resolvedOrg.UID,
		models.EventTypeAuthLoginSucceeded,
		audit.Target{Type: auditTargetUser, UID: user.UID, Name: user.Email},
		models.JSONMap{
			auditKeyMethod: method,
			auditKeyEmail:  user.Email,
			auditKeyRole:   role,
		})
}
