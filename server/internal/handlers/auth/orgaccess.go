package auth

import (
	"context"

	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
)

// OrgAccessDenial classifies the outcome of an org-access authorization check
// so each transport can map it to its own signal (a REST HTTP status vs. a
// WebSocket close code).
type OrgAccessDenial int

const (
	// OrgAccessGranted means the user may access the organization.
	OrgAccessGranted OrgAccessDenial = iota
	// OrgAccessOrgNotFound means the organization slug does not resolve. Only
	// reachable once the caller has already cleared the token-scope check (or
	// is a super-admin), so it never leaks org existence to an unrelated user.
	OrgAccessOrgNotFound
	// OrgAccessDenied means the user is authenticated but not authorized for
	// this org (token scoped to a different org, or not a member).
	OrgAccessDenied
)

// AuthorizeOrgAccess is the single source of truth for whether an authenticated
// user may access an organization's resources. Both the REST middleware
// (middleware.RequireOrgAccess) and the realtime WebSocket handshake
// (realtimews.authorizeOrg) call it, so the two surfaces cannot drift — the
// cross-tenant leak fixed by spec 2026-07-15-01 existed precisely because REST
// had no such check while the WS had its own hand-maintained copy.
//
// The rule is token-scope equality PLUS membership:
//
//   - A claims super-admin may access any org.
//   - A non-super-admin's access token must be scoped to the org it is
//     accessing (claims.OrgSlug == orgSlug). This is deliberate and
//     load-bearing: downstream handlers trust claims.Role / claims.OrgSlug as
//     the caller's role FOR THE ORG BEING ACCESSED (see the many
//     `claims.Role == "admin"` admin gates). Allowing membership-only cross-org
//     access with a token minted for a different org would let a stale
//     claims.Role grant elevated privileges in an org where the user has a
//     lower role. The dashboard therefore re-mints the token (switch-org) when
//     navigating between orgs so claims.OrgSlug tracks the URL.
//   - A DB-only super-admin (user.SuperAdmin) skips the membership check but
//     NOT the token-scope check — a DB super-admin whose token is scoped
//     elsewhere is still denied, matching the historical REST behavior.
//   - Everyone else must be a member of the org.
//
// The token-scope check runs before the org is loaded so a user whose token is
// not scoped to the org can't use the not-found-vs-denied distinction to probe
// which org slugs exist.
func AuthorizeOrgAccess(
	ctx context.Context,
	dbService db.Service,
	claims *Claims,
	user *models.User,
	orgSlug string,
) (*models.Organization, OrgAccessDenial) {
	// Token-scope gate (super-admins by claim cross orgs freely). Checked
	// before loading the org to avoid leaking org existence.
	if !claims.IsSuperAdmin() && claims.OrgSlug != orgSlug {
		return nil, OrgAccessDenied
	}

	org := ResolvedOrgFromContext(ctx, orgSlug)
	if org == nil {
		loaded, err := dbService.GetOrganizationBySlug(ctx, orgSlug)
		if err != nil {
			return nil, OrgAccessOrgNotFound
		}

		org = loaded
	}

	// Claims and DB super-admins skip the membership check.
	if claims.IsSuperAdmin() || user.SuperAdmin {
		return org, OrgAccessGranted
	}

	if _, err := dbService.GetMemberByUserAndOrg(ctx, user.UID, org.UID); err != nil {
		return nil, OrgAccessDenied
	}

	return org, OrgAccessGranted
}

// resolvedOrgContextKey memoizes an organization already loaded by slug earlier
// in the request. It is a pure optimization with no authorization meaning.
type resolvedOrgContextKey struct{}

// WithResolvedOrg memoizes an organization loaded by slug on the request
// context. middleware.OrgSlugRedirect has to resolve the :org slug before the
// auth chain runs (to decide whether the request is hitting a renamed org's
// previous slug); memoizing the result keeps AuthorizeOrgAccess from repeating
// the very same query one middleware later.
//
// The memo is per-request and keyed by slug, so it can never serve a different
// organization than a fresh lookup would have.
func WithResolvedOrg(ctx context.Context, org *models.Organization) context.Context {
	if org == nil {
		return ctx
	}

	return context.WithValue(ctx, resolvedOrgContextKey{}, org)
}

// ResolvedOrgFromContext returns the memoized organization when it matches the
// requested slug, or nil when the caller must query the database.
func ResolvedOrgFromContext(ctx context.Context, slug string) *models.Organization {
	org, ok := ctx.Value(resolvedOrgContextKey{}).(*models.Organization)
	if !ok || org == nil || org.Slug != slug {
		return nil
	}

	return org
}
