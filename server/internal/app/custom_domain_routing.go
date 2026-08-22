package app

import (
	"context"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"path"
	"strings"
	"sync"
	"time"

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
}

// customDomainCacheEntry holds a resolution and its expiry. A nil page is the
// negative sentinel (host is not a live custom domain). A dedicated map+mutex
// cache is used instead of utils/cache.Cache[T] because that cache's
// weak.Pointer semantics make negative sentinels (and short-lived positive
// entries) unreliable — the spec's open-question escape hatch.
type customDomainCacheEntry struct {
	page      *resolvedCustomDomain
	expiresAt time.Time
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
// A cached hit with a nil page is a negative result.
func (c *customDomainCache) get(host string) (*resolvedCustomDomain, bool) {
	c.mu.RLock()
	entry, ok := c.entries[host]
	c.mu.RUnlock()

	if !ok || time.Now().After(entry.expiresAt) {
		return nil, false
	}

	return entry.page, true
}

func (c *customDomainCache) set(host string, page *resolvedCustomDomain) {
	c.mu.Lock()
	c.entries[host] = customDomainCacheEntry{page: page, expiresAt: time.Now().Add(c.ttl)}
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

		page := s.resolveCustomDomain(req.Context(), host)
		if page == nil {
			next.ServeHTTP(writer, req)

			return
		}

		s.serveCustomHost(writer, req, page)
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

// resolveCustomDomain resolves a host to a servable page (verified + enabled +
// public), using the TTL cache. A nil return means "not a servable custom
// domain" (negative-cached).
func (s *Server) resolveCustomDomain(ctx context.Context, host string) *resolvedCustomDomain {
	if cached, ok := s.customDomainCache.get(host); ok {
		return cached
	}

	page := s.lookupCustomDomain(ctx, host)
	s.customDomainCache.set(host, page)

	return page
}

// lookupCustomDomain hits the DB and enforces the verified+enabled+public gate.
func (s *Server) lookupCustomDomain(ctx context.Context, host string) *resolvedCustomDomain {
	statusPage, err := s.dbService.GetStatusPageByCustomDomain(ctx, host)
	if err != nil || statusPage == nil {
		return nil
	}

	// A password-protected page IS served on its custom domain — that is where
	// the unlock form has to appear. The API behind it stays gated; only the
	// SPA shell is routed here.
	if statusPage.CustomDomainVerifiedAt == nil || !statuspagelock.Visible(statusPage) {
		return nil
	}

	org, err := s.dbService.GetOrganization(ctx, statusPage.OrganizationUID)
	if err != nil || org == nil {
		return nil
	}

	return &resolvedCustomDomain{
		OrgSlug:     org.Slug,
		Slug:        statusPage.Slug,
		Name:        statusPage.Name,
		Description: statusPage.Description,
	}
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

	writer.Header().Set("Cache-Control", "public, max-age=60")
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
