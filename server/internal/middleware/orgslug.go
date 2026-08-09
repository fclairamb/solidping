package middleware

import (
	"net/http"
	"net/url"
	"strings"

	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/handlers/auth"
	"github.com/fclairamb/solidping/server/internal/httpx"
)

// orgPatternSegment is the chi route-pattern segment naming the organization
// slug. Route patterns are declared bunrouter-style (":org") in server.go and
// translated to chi's "{org}" at registration (httpx.convertPattern).
const orgPatternSegment = "{org}"

// OrgSlugRedirect keeps a renamed organization's previous URLs alive.
//
// Renaming an org used to break every link a customer had already pasted
// elsewhere — status pages, SVG badges, the /embed/v1 widget, bookmarked
// dashboard URLs. Instead, the old slug is remembered
// (models.OrganizationPreviousSlug) and requests to it are redirected to the
// organization's current slug.
//
// Two invariants make this safe:
//
//   - A live organization ALWAYS wins. The live lookup runs first, so an alias
//     can never shadow an existing org, and the redirect stops the instant
//     another org claims the freed slug.
//   - A soft-deleted organization is never reachable. The alias lookup joins
//     organizations with `deleted_at IS NULL`, so a deleted org's slug 404s
//     immediately with no alias or tombstone (spec 2026-08-08-11), even if it
//     had been renamed first.
//
// It must run BEFORE RequireAuth/RequireOrgAccess: the token-scope check in
// auth.AuthorizeOrgAccess compares the JWT orgSlug claim with the URL param and
// would answer 403 for a freshly re-minted token hitting an old URL, masking
// the redirect. Running first also means an unauthenticated request to an old
// public URL redirects rather than 401s.
type OrgSlugRedirect struct {
	dbService db.Service
}

// NewOrgSlugRedirect builds the previous-slug redirect middleware.
func NewOrgSlugRedirect(dbService db.Service) *OrgSlugRedirect {
	return &OrgSlugRedirect{dbService: dbService}
}

// Middleware resolves the :org path parameter and, when it only matches a
// renamed organization's previous slug, redirects to the same URL with the
// canonical slug. Anything else (live org, unknown slug, no :org param) falls
// straight through, so a genuinely unknown org still 404s downstream with the
// normal error shape.
func (m *OrgSlugRedirect) Middleware(next httpx.HandlerFunc) httpx.HandlerFunc {
	return func(writer http.ResponseWriter, req *http.Request) error {
		org, live := m.resolve(req)
		if org == nil {
			return next(writer, req)
		}

		if live {
			// Memoize so RequireOrgAccess doesn't repeat the lookup.
			return next(writer, req.WithContext(auth.WithResolvedOrg(req.Context(), org)))
		}

		target, ok := canonicalOrgURL(httpx.RoutePattern(req), req.URL, org.Slug)
		if !ok {
			return next(writer, req)
		}

		http.Redirect(writer, req, target, redirectStatusFor(req.Method))

		return nil
	}
}

// resolve returns the organization the :org param points at, and whether it was
// found as a live slug (true) or only as a rename alias (false). A nil org
// means "nothing to do".
func (m *OrgSlugRedirect) resolve(req *http.Request) (*models.Organization, bool) {
	slug := httpx.Param(req, "org")
	if slug == "" {
		return nil, false
	}

	ctx := req.Context()

	if org, err := m.dbService.GetOrganizationBySlug(ctx, slug); err == nil && org != nil {
		return org, true
	}

	org, err := m.dbService.GetOrganizationByPreviousSlug(ctx, slug)
	if err != nil || org == nil || org.Slug == slug {
		return nil, false
	}

	return org, false
}

// redirectStatusFor picks the redirect status. Safe methods get the cacheable
// 301 the spec calls for; everything else gets 308, which is the same
// permanent-move semantics but forbids the method rewrite a 301 licenses — a
// POST to a renamed org (heartbeat ingest, status-page subscribe) must arrive
// as a POST with its body intact, not as a GET.
func redirectStatusFor(method string) int {
	if method == http.MethodGet || method == http.MethodHead {
		return http.StatusMovedPermanently
	}

	return http.StatusPermanentRedirect
}

// canonicalOrgURL rewrites the organization segment of reqURL to newSlug,
// keeping the rest of the path and the query string untouched. The segment is
// located through the matched chi route pattern rather than by searching the
// path for the old slug, so a check, status page or file whose own slug happens
// to equal the org's previous slug is never rewritten by accident.
//
// It returns ok=false when the pattern is unavailable or does not carry an
// {org} segment before any wildcard, in which case the caller leaves the
// request alone.
func canonicalOrgURL(pattern string, reqURL *url.URL, newSlug string) (string, bool) {
	index, ok := orgSegmentIndex(pattern)
	if !ok {
		return "", false
	}

	segments := strings.Split(reqURL.EscapedPath(), "/")
	if index >= len(segments) {
		return "", false
	}

	segments[index] = newSlug

	rewritten, err := url.Parse(strings.Join(segments, "/"))
	if err != nil {
		return "", false
	}

	rewritten.RawQuery = reqURL.RawQuery
	rewritten.Fragment = reqURL.Fragment

	return rewritten.RequestURI(), true
}

// orgSegmentIndex returns the index of the {org} segment in a chi route
// pattern. It refuses patterns with a wildcard before {org}: a wildcard can
// swallow any number of path segments, so index alignment with the request path
// would no longer hold.
func orgSegmentIndex(pattern string) (int, bool) {
	if pattern == "" {
		return 0, false
	}

	for i, segment := range strings.Split(pattern, "/") {
		switch {
		case segment == orgPatternSegment:
			return i, true
		case strings.HasPrefix(segment, "*"):
			return 0, false
		}
	}

	return 0, false
}
