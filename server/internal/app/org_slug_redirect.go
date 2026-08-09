package app

import (
	"context"
	"net/http"
	"net/url"
	"regexp"
	"strings"
)

// spaOrgSlugRegex mirrors auth.orgSlugRegex. It is a cheap pre-filter: a path
// segment that could never have been an org slug is not worth two database
// lookups on every static-asset request.
var spaOrgSlugRegex = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,18}[a-z0-9]$`)

// dash0OrgsPrefixSegments is the "/dash0/orgs/<slug>/..." shape: ["", "dash0",
// "orgs", "<slug>", ...].
const (
	dash0OrgSegmentIndex   = 3
	dash0OrgsMarkerIndex   = 2
	status0OrgSegmentIndex = 2
	// status0MaxSegments bounds "/status0/<org>/<page>" (4 segments incl. the
	// leading empty one). Anything deeper is an asset path, not a page URL.
	status0MaxSegments = 4
)

// redirectRenamedOrgSPA redirects a single-page-app URL that still carries a
// renamed organization's previous slug.
//
// The API and public JSON/SVG surfaces are handled structurally by
// middleware.OrgSlugRedirect, but the SPA routes are registered as wildcards
// ("/dash0/*path", "/status0/*path") with no {org} path parameter to key off,
// so the org segment is located positionally here instead. Without this a
// customer's bookmarked dashboard URL or pasted status-page link would load the
// app shell and then fail against an org that no longer answers on that slug.
//
// Returns true when a redirect was written and the caller must stop.
func (s *Server) redirectRenamedOrgSPA(writer http.ResponseWriter, req *http.Request) bool {
	index, ok := spaOrgSegmentIndex(req.URL.EscapedPath())
	if !ok {
		return false
	}

	segments := strings.Split(req.URL.EscapedPath(), "/")

	canonical, ok := s.canonicalOrgSlug(req.Context(), segments[index])
	if !ok {
		return false
	}

	segments[index] = canonical

	rewritten, err := url.Parse(strings.Join(segments, "/"))
	if err != nil {
		return false
	}

	rewritten.RawQuery = req.URL.RawQuery

	http.Redirect(writer, req, rewritten.RequestURI(), http.StatusMovedPermanently)

	return true
}

// spaOrgSegmentIndex locates the organization slug inside a dash0 or status0
// SPA path, or reports that there is none worth resolving.
func spaOrgSegmentIndex(path string) (int, bool) {
	segments := strings.Split(path, "/")

	// A segment with a dot is a file (assets/index-abc.js, favicon.ico), never
	// an org slug — and asset requests are the overwhelming majority here.
	for _, segment := range segments {
		if strings.Contains(segment, ".") {
			return 0, false
		}
	}

	switch {
	case len(segments) > dash0OrgSegmentIndex &&
		segments[1] == "dash0" && segments[dash0OrgsMarkerIndex] == "orgs":
		return dash0OrgSegmentIndex, spaOrgSlugRegex.MatchString(segments[dash0OrgSegmentIndex])
	case len(segments) > status0OrgSegmentIndex && len(segments) <= status0MaxSegments &&
		segments[1] == "status0":
		return status0OrgSegmentIndex, spaOrgSlugRegex.MatchString(segments[status0OrgSegmentIndex])
	default:
		return 0, false
	}
}

// canonicalOrgSlug returns the current slug of the organization that used to
// answer on slug, and false when slug belongs to a live org (nothing to do), is
// unknown, or only aliases a deleted org.
func (s *Server) canonicalOrgSlug(ctx context.Context, slug string) (string, bool) {
	if s.dbService == nil {
		return "", false
	}

	// A live organization always wins over an alias.
	if org, err := s.dbService.GetOrganizationBySlug(ctx, slug); err == nil && org != nil {
		return "", false
	}

	org, err := s.dbService.GetOrganizationByPreviousSlug(ctx, slug)
	if err != nil || org == nil || org.Slug == slug {
		return "", false
	}

	return org.Slug, true
}
