package app

import (
	"html"
	"net/http"
	"regexp"
	"strings"

	"github.com/uptrace/bunrouter"

	"github.com/fclairamb/solidping/server/internal/handlers/statuspages"
)

// Open Graph / Twitter Card constants for status0 link previews.
const (
	// ogSiteName is the og:site_name value for every status page.
	ogSiteName = "SolidPing"
	// ogFallbackDescription is used when a status page has no description set.
	ogFallbackDescription = "Live status, uptime and incident history."
	// ogTitleSuffix is appended to the page name to build the <title>/og:title.
	ogTitleSuffix = " — Status"
	// ogDefaultImagePath is the request-relative path of the branded fallback
	// preview image shipped in the status0 assets (1200×630 PNG).
	ogDefaultImagePath = "/status0/og-default.png"
	// ogTwitterCard is "summary_large_image" because a 1200×630 image ships.
	ogTwitterCard = "summary_large_image"
)

// status0TitleTagRegexp matches the static <title>…</title> in the status0
// index.html so it can be replaced by the per-page title. Case-insensitive and
// non-greedy; the head only ever contains a single title tag.
var status0TitleTagRegexp = regexp.MustCompile(`(?is)<title>.*?</title>`)

// ogMetadata holds the resolved Open Graph / Twitter Card values injected into
// a status page's served index.html.
type ogMetadata struct {
	Title       string
	Description string
	URL         string
	Image       string
}

// statusPagePathParts extracts (org, slug) from a status0 request path whose
// "/status0" prefix has already been stripped. It matches only the two
// status-page route shapes:
//
//	/:org        -> (org, "",   true)  // organization default page
//	/:org/:slug  -> (org, slug, true)  // specific page
//
// The root, empty segments, and any path with three or more segments return
// ok=false so their served head stays generic.
func statusPagePathParts(reqPath string) (org, slug string, ok bool) {
	trimmed := strings.Trim(reqPath, "/")
	if trimmed == "" {
		return "", "", false
	}

	segments := strings.Split(trimmed, "/")
	for _, segment := range segments {
		if segment == "" {
			return "", "", false
		}
	}

	switch len(segments) {
	case 1:
		return segments[0], "", true
	case 2:
		return segments[0], segments[1], true
	default:
		return "", "", false
	}
}

// requestScheme resolves the request scheme, preferring the first token of a
// proxy-set X-Forwarded-Proto, then the TLS state, defaulting to http.
func requestScheme(req *http.Request) string {
	if proto := req.Header.Get("X-Forwarded-Proto"); proto != "" {
		if idx := strings.IndexByte(proto, ','); idx != -1 {
			proto = proto[:idx]
		}

		if proto = strings.TrimSpace(proto); proto != "" {
			return proto
		}
	}

	if req.TLS != nil {
		return "https"
	}

	return "http"
}

// requestOrigin builds the "scheme://host" origin for a request, used to make
// og:url and og:image absolute (crawlers generally require absolute URLs).
func requestOrigin(req *http.Request) string {
	return requestScheme(req) + "://" + req.Host
}

// buildStatus0MetaTags renders the escaped <title> plus Open Graph / Twitter
// Card / description meta tags for a status page. Every dynamic value is
// HTML-escaped.
func buildStatus0MetaTags(meta ogMetadata) string {
	var b strings.Builder

	writeTag := func(tag string) {
		b.WriteString("    ")
		b.WriteString(tag)
		b.WriteString("\n")
	}

	title := html.EscapeString(meta.Title)
	description := html.EscapeString(meta.Description)

	writeTag("<title>" + title + "</title>")
	writeTag(`<meta name="description" content="` + description + `" />`)
	writeTag(`<meta property="og:title" content="` + title + `" />`)
	writeTag(`<meta property="og:description" content="` + description + `" />`)
	writeTag(`<meta property="og:type" content="website" />`)
	writeTag(`<meta property="og:site_name" content="` + ogSiteName + `" />`)

	if meta.URL != "" {
		writeTag(`<meta property="og:url" content="` + html.EscapeString(meta.URL) + `" />`)
	}

	if meta.Image != "" {
		writeTag(`<meta property="og:image" content="` + html.EscapeString(meta.Image) + `" />`)
	}

	writeTag(`<meta name="twitter:card" content="` + ogTwitterCard + `" />`)
	writeTag(`<meta name="twitter:title" content="` + title + `" />`)
	writeTag(`<meta name="twitter:description" content="` + description + `" />`)

	if meta.Image != "" {
		writeTag(`<meta name="twitter:image" content="` + html.EscapeString(meta.Image) + `" />`)
	}

	return b.String()
}

// injectStatus0Meta rewrites a status0 index.html head with per-page metadata:
// it drops the static <title> and splices the rendered meta block in just
// before </head>. If there is no </head> the document is returned unchanged.
func injectStatus0Meta(htmlDoc string, meta ogMetadata) string {
	block := buildStatus0MetaTags(meta)

	htmlDoc = status0TitleTagRegexp.ReplaceAllString(htmlDoc, "")

	idx := strings.LastIndex(htmlDoc, "</head>")
	if idx == -1 {
		return htmlDoc
	}

	return htmlDoc[:idx] + block + htmlDoc[idx:]
}

// status0MetaForPath resolves the Open Graph metadata for a status0 request
// whose index.html fallback is about to be served. reqPath is the request path
// with its "/status0" prefix already stripped.
//
// It returns ok=false — and the caller then serves the unmodified generic head
// — for the root, non-status-page paths, and any org/page that does not resolve
// to an enabled, public status page. The public lookups (ViewStatusPage /
// ViewDefaultStatusPage) already return ErrStatusPageNotFound for disabled and
// private pages, so a missing page and a private page produce byte-for-byte
// identical responses and metadata cannot be used to probe page existence.
func (s *Server) status0MetaForPath(req bunrouter.Request, reqPath string) (ogMetadata, bool) {
	if s.statusPagesService == nil {
		return ogMetadata{}, false
	}

	org, slug, ok := statusPagePathParts(reqPath)
	if !ok {
		return ogMetadata{}, false
	}

	page, err := s.lookupPublicStatusPage(req, org, slug)
	if err != nil {
		return ogMetadata{}, false
	}

	description := ogFallbackDescription
	if page.Description != nil && strings.TrimSpace(*page.Description) != "" {
		description = *page.Description
	}

	origin := requestOrigin(req.Request)

	return ogMetadata{
		Title:       page.Name + ogTitleSuffix,
		Description: description,
		URL:         origin + req.URL.Path,
		Image:       origin + ogDefaultImagePath,
	}, true
}

// lookupPublicStatusPage resolves a status page by org (+ optional slug) via the
// same public lookups the API uses. An empty slug resolves the org's default
// page. Both paths enforce enabled + public visibility and return
// ErrStatusPageNotFound otherwise.
func (s *Server) lookupPublicStatusPage(
	req bunrouter.Request, org, slug string,
) (statuspages.StatusPageResponse, error) {
	if slug == "" {
		return s.statusPagesService.ViewDefaultStatusPage(req.Context(), org)
	}

	return s.statusPagesService.ViewStatusPage(req.Context(), org, slug)
}
