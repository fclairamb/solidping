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
