package app

import (
	"context"
	"html"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/fclairamb/solidping/server/internal/statuspagecache"
	"github.com/fclairamb/solidping/server/internal/statuspagelock"
)

// customDomainCacheTTL bounds how long a host->page resolution (positive OR
// negative) is cached. It keeps random-host scans off the DB while ensuring a
// de-verified/disabled page stops being served within the TTL.
const customDomainCacheTTL = 60 * time.Second

// Path prefixes used by the custom-host allowlist routing.
const (
	routeStatus0 = "/status0"
	routeDash0   = "/dash0"
	routeDocs    = "/docs"
	routeMetrics = "/metrics"

	// Unauthenticated management endpoints the status0 SPA needs on a custom
	// host (the footer renders the build it is talking to).
	routeMgmtVersion = "/api/mgmt/version"
	routeMgmtHealth  = "/api/mgmt/health"
)

// resolvedCustomDomain is the cached resolution of a custom host to a servable
// status page (org slug + page slug for the SPA bootstrap, plus name/description
// for OG-meta injection).
type resolvedCustomDomain struct {
	OrgSlug     string
	Slug        string
	Name        string
	Description *string
	// Visibility is the page's stored visibility. A `password` page IS routed
	// here (the unlock form has to appear somewhere), and the shell served for
	// it embeds the page name and description as OG metadata — so the shell
	// inherits the same who-may-cache-this rule as the API behind it
	// (spec 2026-08-22-06).
	Visibility string
}

// customDomainResolution is what a host resolves to. The three cases are
// deliberately distinct:
//
//   - page != nil                 servable: render the status page
//   - page == nil, known == true  a status page HOLDS this domain but is not
//     currently servable (demoted, disabled, private). This must NOT fall
//     through to the instance's own-host routing — that served the operator
//     dashboard on a customer's hostname. It gets a legible error page instead.
//   - page == nil, known == false not ours at all; fall through unchanged.
type customDomainResolution struct {
	page  *resolvedCustomDomain
	known bool
	// reason is a machine-ish token explaining why a known domain is not
	// servable, used to pick the wording of the error page.
	reason string
}

// Reasons a known custom domain is not currently servable.
const (
	// reasonUnverified means the domain's DNS verification is not (or no
	// longer) in place — the demoted state the re-verification sweep produces.
	reasonUnverified = "unverified"
	// reasonUnavailable means the page itself is disabled or not public.
	reasonUnavailable = "unavailable"
)

// customDomainCacheEntry holds a resolution and its expiry. A dedicated
// map+mutex cache is used instead of utils/cache.Cache[T] because that cache's
// weak.Pointer semantics make negative sentinels (and short-lived positive
// entries) unreliable — the spec's open-question escape hatch.
type customDomainCacheEntry struct {
	resolution customDomainResolution
	expiresAt  time.Time
}

// customDomainCache is a small TTL cache for host -> page resolutions.
type customDomainCache struct {
	mu      sync.RWMutex
	entries map[string]customDomainCacheEntry
	ttl     time.Duration
}

func newCustomDomainCache(ttl time.Duration) *customDomainCache {
	return &customDomainCache{
		entries: make(map[string]customDomainCacheEntry),
		ttl:     ttl,
	}
}

// get returns the cached resolution and whether the host was cached at all.
func (c *customDomainCache) get(host string) (customDomainResolution, bool) {
	c.mu.RLock()
	entry, ok := c.entries[host]
	c.mu.RUnlock()

	if !ok || time.Now().After(entry.expiresAt) {
		return customDomainResolution{}, false
	}

	return entry.resolution, true
}

func (c *customDomainCache) set(host string, resolution customDomainResolution) {
	c.mu.Lock()
	c.entries[host] = customDomainCacheEntry{resolution: resolution, expiresAt: time.Now().Add(c.ttl)}
	c.mu.Unlock()
}

// handlerWithCustomDomains wraps the main handler chain so a request whose Host
// is a verified custom domain is served the status page (rewrite, not redirect —
// the browser stays on the custom host). Reserved and unknown hosts fall through
// to next unchanged.
func (s *Server) handlerWithCustomDomains(next http.Handler) http.Handler {
	if s.customDomainCache == nil {
		return next
	}

	return http.HandlerFunc(func(writer http.ResponseWriter, req *http.Request) {
		host := hostOnly(req.Host)
		if host == "" || s.isReservedCustomHost(host) {
			next.ServeHTTP(writer, req)

			return
		}

		resolved := s.resolveCustomDomain(req.Context(), host)

		switch {
		case resolved.page != nil:
			s.serveCustomHost(writer, req, resolved.page)
		case resolved.known:
			serveCustomDomainUnavailable(writer, req, host, resolved.reason)
		default:
			next.ServeHTTP(writer, req)
		}
	})
}

// hostOnly strips any port and lowercases a request Host.
func hostOnly(reqHost string) string {
	host := reqHost
	if h, _, err := net.SplitHostPort(reqHost); err == nil {
		host = h
	}

	return strings.ToLower(host)
}

// isReservedCustomHost reports whether a host is one of the instance's own
// hostnames (base-url host, docs host, CNAME target), which must never be
// treated as a customer custom domain.
func (s *Server) isReservedCustomHost(host string) bool {
	for _, reserved := range s.reservedCustomHosts() {
		if host == reserved {
			return true
		}
	}

	return false
}

// reservedCustomHosts returns the instance's own hostnames.
func (s *Server) reservedCustomHosts() []string {
	hosts := make([]string, 0, 3)

	if target := s.config.CustomDomainCNAMETarget(); target != "" {
		hosts = append(hosts, target)
	}

	if docs := strings.ToLower(strings.TrimSpace(s.config.Server.DocsHost)); docs != "" {
		hosts = append(hosts, docs)
	}

	if parsed, err := url.Parse(s.config.Server.BaseURL); err == nil {
		if base := strings.ToLower(parsed.Hostname()); base != "" {
			hosts = append(hosts, base)
		}
	}

	return hosts
}

// resolveCustomDomain resolves a host, using the TTL cache. Both the servable
// and the known-but-unservable answers are cached, so a demoted domain under
// load costs one lookup per TTL rather than one per request.
func (s *Server) resolveCustomDomain(ctx context.Context, host string) customDomainResolution {
	if cached, ok := s.customDomainCache.get(host); ok {
		return cached
	}

	resolved := s.lookupCustomDomain(ctx, host)
	s.customDomainCache.set(host, resolved)

	return resolved
}

// lookupCustomDomain hits the DB and enforces the verified+enabled+public gate,
// distinguishing "not ours" from "ours but not currently servable".
func (s *Server) lookupCustomDomain(ctx context.Context, host string) customDomainResolution {
	statusPage, err := s.dbService.GetStatusPageByCustomDomain(ctx, host)
	if err != nil || statusPage == nil {
		return customDomainResolution{}
	}

	// From here on the host IS one of ours: a live status page holds it. Even
	// when it cannot be served, the request must not fall through to the
	// instance's own-host routing.
	if statusPage.CustomDomainVerifiedAt == nil {
		return customDomainResolution{known: true, reason: reasonUnverified}
	}

	// A password-protected page IS served on its custom domain — that is where
	// the unlock form has to appear. The API behind it stays gated; only the
	// SPA shell is routed here.
	if !statuspagelock.Visible(statusPage) {
		return customDomainResolution{known: true, reason: reasonUnavailable}
	}

	org, err := s.dbService.GetOrganization(ctx, statusPage.OrganizationUID)
	if err != nil || org == nil {
		return customDomainResolution{known: true, reason: reasonUnavailable}
	}

	return customDomainResolution{page: &resolvedCustomDomain{
		OrgSlug:     org.Slug,
		Slug:        statusPage.Slug,
		Name:        statusPage.Name,
		Description: statusPage.Description,
		Visibility:  statusPage.Visibility,
	}}
}

// customDomainUnavailableRetryAfter is the Retry-After hint on the
// not-currently-served page. It matches the re-verification sweep's cadence:
// telling a crawler to come back sooner than the state can possibly change is
// noise.
const customDomainUnavailableRetryAfter = "21600"

// serveCustomDomainUnavailable answers a request on a custom domain that this
// installation knows about but is not currently serving.
//
// This is the HTTP half of "never fail at the TLS handshake for a domain we
// hold a certificate for" (spec 2026-08-23-03) and of the older acceptance
// criterion it makes satisfiable at all: an unverified, removed or expired
// domain must degrade to a CLEAR MESSAGE, not to a browser security
// interstitial. It is also what stops the operator dashboard from being served
// on a customer's hostname once the mapping stops being servable.
//
// 503 rather than 404: the domain is configured, the page exists, and this is a
// temporary state that fixing DNS resolves. A 404 would invite search engines
// to drop the page permanently.
func serveCustomDomainUnavailable(writer http.ResponseWriter, req *http.Request, host, reason string) {
	detail := "This domain is not currently verified for this status page."
	if reason == reasonUnavailable {
		detail = "The status page for this domain is not currently available."
	}

	writer.Header().Set("Retry-After", customDomainUnavailableRetryAfter)
	// Never cached: the state flips the moment DNS is fixed or an operator
	// clicks Verify, and a cached error page would outlive the fix.
	writer.Header().Set("Cache-Control", "no-store")
	writer.Header().Set("X-Robots-Tag", "noindex")
	writer.WriteHeader(http.StatusServiceUnavailable)

	if req.Method == http.MethodHead {
		return
	}

	if !strings.Contains(req.Header.Get("Accept"), "text/html") {
		writer.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = io.WriteString(writer, "Status page unavailable\n\n"+detail+"\n")

		return
	}

	writer.Header().Set("Content-Type", contentTypeHTML)
	_, _ = io.WriteString(writer, customDomainUnavailableHTML(host, detail))
}

// customDomainUnavailableHTML renders the message. Self-contained on purpose:
// the page has to render on a hostname whose SPA assets we are deliberately not
// serving, so it can depend on nothing.
func customDomainUnavailableHTML(host, detail string) string {
	return `<!doctype html>
<html lang="en"><head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<meta name="robots" content="noindex">
<title>Status page unavailable</title>
<style>
:root{color-scheme:light dark}
body{margin:0;min-height:100vh;display:flex;align-items:center;justify-content:center;
font:16px/1.6 system-ui,-apple-system,"Segoe UI",Roboto,sans-serif;background:#fafafa;color:#18181b}
@media (prefers-color-scheme:dark){body{background:#09090b;color:#fafafa}}
main{max-width:34rem;padding:2rem;text-align:center}
h1{font-size:1.25rem;margin:0 0 .75rem}
code{font-family:ui-monospace,SFMono-Regular,Menlo,monospace;font-size:.9em}
p{margin:.5rem 0;opacity:.85}
</style></head>
<body><main>
<h1>Status page unavailable</h1>
<p>` + html.EscapeString(detail) + `</p>
<p><code>` + html.EscapeString(host) + `</code></p>
<p>If you administer this page, check that the domain's DNS record still points here, then re-verify it.</p>
</main></body></html>
`
}

// serveCustomHost allowlist-routes a request on a resolved custom host. Only the
// status0 SPA, its assets, and the public API endpoints it needs are served;
// everything else (dash0, docs, auth, the rest of /api) is 404.
func (s *Server) serveCustomHost(writer http.ResponseWriter, req *http.Request, page *resolvedCustomDomain) {
	reqPath := req.URL.Path

	switch {
	case reqPath == routeStatus0 || strings.HasPrefix(reqPath, routeStatus0+"/"):
		// The SPA is built with the /status0 base, so its asset URLs keep working
		// on the custom host. Real files are served as normal static assets.
		//
		// Anything under /status0 that is NOT a file is an SPA route, and must go
		// through the index handler below so it gets the sp-page bootstrap tag.
		// Serving it statically returns the bare shell, and the SPA — finding no
		// tag to tell it which page it is on — falls back to its generic
		// "Visit a specific status page" landing. That is what made
		// https://<custom-host>/status0/ render an empty page while "/" worked.
		if s.status0StaticAssetExists(reqPath) {
			_ = s.serveStatus0Static(writer, req)

			return
		}

		s.serveStatus0IndexForCustomHost(writer, req, page)
	case strings.HasPrefix(reqPath, "/api/"):
		if isCustomHostAPIAllowed(reqPath) {
			s.router.ServeHTTP(writer, req)
		} else {
			http.NotFound(writer, req)
		}
	case isCustomHostForbidden(reqPath):
		http.NotFound(writer, req)
	default:
		// "/" and any other non-asset SPA path render the status page in place
		// with host-resolved metadata (address bar stays on the custom host).
		s.serveStatus0IndexForCustomHost(writer, req, page)
	}
}

// isCustomHostAPIAllowed reports whether an /api path is one of the public
// endpoints the status0 SPA needs on a custom host (public status-page view +
// feed, subscribe/confirm/unsubscribe, badges). Everything else under /api is
// denied. Authed admin routes that happen to match still enforce their own auth
// middleware, so they answer 401 rather than leaking data.
func isCustomHostAPIAllowed(reqPath string) bool {
	switch {
	// The status page footer renders the build it is talking to
	// (status-page-view.tsx -> useVersion), so the SPA needs these two on a
	// custom host as much as on the installation's own. Both are already
	// unauthenticated on every host and carry no per-org data.
	//
	// Matched exactly rather than by an /api/mgmt/ prefix: that group also
	// holds /limits, POST /report and the super-admin /memory and
	// /scheduling/* endpoints, and a prefix would quietly expose whatever is
	// added there next.
	case reqPath == routeMgmtVersion, reqPath == routeMgmtHealth:
		return true
	case strings.HasPrefix(reqPath, "/api/v1/status-pages/"):
		return true
	case strings.HasPrefix(reqPath, "/api/v1/public/status-subscribers/"):
		return true
	case strings.HasPrefix(reqPath, "/api/v1/orgs/") && strings.HasSuffix(reqPath, "/subscribers"):
		return true
	case strings.HasPrefix(reqPath, "/api/v1/orgs/") &&
		strings.Contains(reqPath, "/checks/") && strings.Contains(reqPath, "/badges/"):
		return true
	default:
		return false
	}
}

// status0StaticAssetExists reports whether a /status0 path maps to a real file
// in the embedded SPA bundle. It is how an asset request is told apart from an
// SPA route, without guessing from file extensions.
//
// index.html is deliberately excluded: requesting it directly is an SPA entry
// point, not an asset fetch, so it needs the sp-page tag injected like any
// other route.
func (s *Server) status0StaticAssetExists(reqPath string) bool {
	rel := strings.TrimPrefix(reqPath, routeStatus0)
	rel = strings.TrimPrefix(rel, "/")

	if rel == "" || rel == "index.html" {
		return false
	}

	info, err := fs.Stat(s.status0FSOrDefault(), path.Join("status0res", rel))
	if err != nil {
		return false
	}

	return !info.IsDir()
}

// isCustomHostForbidden reports whether a path must always 404 on a custom host
// (the operator dashboard, docs, and the OpenAPI/metrics surfaces).
func isCustomHostForbidden(reqPath string) bool {
	return reqPath == routeDash0 || strings.HasPrefix(reqPath, routeDash0+"/") ||
		reqPath == routeDocs || strings.HasPrefix(reqPath, routeDocs+"/") ||
		reqPath == "/openapi" || reqPath == "/openapi.yaml" ||
		reqPath == routeMetrics
}

// serveStatus0IndexForCustomHost serves the status0 SPA index with per-page
// OG/Twitter metadata and the sp-page bootstrap tag resolved from the host.
func (s *Server) serveStatus0IndexForCustomHost(
	writer http.ResponseWriter, req *http.Request, page *resolvedCustomDomain,
) {
	data, err := fs.ReadFile(s.status0FSOrDefault(), path.Join("status0res", "index.html"))
	if err != nil {
		http.Error(writer, "File not found", http.StatusNotFound)

		return
	}

	meta := s.status0MetaForCustomHost(req, page)
	data = []byte(injectStatus0Meta(string(data), &meta))

	statuspagecache.Apply(writer.Header(), page.Visibility, statuspagecache.PageMaxAge)
	writer.Header().Set("Content-Type", contentTypeHTML)
	_, _ = writer.Write(data)
}

// status0MetaForCustomHost builds the OG metadata for a custom-host page,
// including the sp-page tag the SPA reads to render the right page without
// navigating. OG URLs use the custom-host origin.
func (s *Server) status0MetaForCustomHost(req *http.Request, page *resolvedCustomDomain) ogMetadata {
	description := ogFallbackDescription
	if page.Description != nil && strings.TrimSpace(*page.Description) != "" {
		description = *page.Description
	}

	origin := requestOrigin(req)

	return ogMetadata{
		Title:       page.Name + ogTitleSuffix,
		Description: description,
		URL:         origin + "/",
		Image:       origin + ogDefaultImagePath,
		Page:        page.OrgSlug + "/" + page.Slug,
	}
}
