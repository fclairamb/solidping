package app

import (
	"context"
	"io/fs"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/handlers/auth"
	"github.com/fclairamb/solidping/server/internal/securityheaders"
)

// This file wires internal/securityheaders into the HTTP surfaces (spec
// 2026-09-25-28). The headers are applied where the embedded bytes are
// written — serveDash0Static, serveStatus0Static, the custom-domain shell,
// the docs and the /openapi explorer — and never as a path-based middleware,
// for two reasons:
//
//   - the JSON API must stay exactly as it is, and a middleware keyed on path
//     is one missed prefix away from stamping a policy on it;
//   - the dev loop proxies /d and /s to Vite (server.redirects). Vite's dev
//     shell injects inline scripts and opens an HMR socket to another port, so
//     a policy on a proxied response would break `make dev`. Proxied
//     responses never pass through the static writers, so they carry none.

// embedOriginsCacheTTL bounds how long an org's embed allowlist is cached. The
// allowlist is read on every status-page shell, which is public and
// unauthenticated, so an uncached lookup would put two queries on every hit;
// a minute is what "takes effect within a minute" in the docs refers to.
const embedOriginsCacheTTL = 60 * time.Second

// embedOriginsCache is a small TTL cache of org slug -> embed allowlist,
// negative entries included (an unknown slug costs one lookup per TTL).
type embedOriginsCache struct {
	mu      sync.Mutex
	entries map[string]embedOriginsEntry
	ttl     time.Duration
}

type embedOriginsEntry struct {
	origins   []string
	expiresAt time.Time
}

func newEmbedOriginsCache(ttl time.Duration) *embedOriginsCache {
	return &embedOriginsCache{entries: make(map[string]embedOriginsEntry), ttl: ttl}
}

func (c *embedOriginsCache) get(key string) ([]string, bool) {
	if c == nil {
		return nil, false
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	entry, ok := c.entries[key]
	if !ok || time.Now().After(entry.expiresAt) {
		return nil, false
	}

	return entry.origins, true
}

// embedOriginsCacheMaxEntries bounds the cache: status-page paths are
// attacker-chosen, so an unbounded map keyed on them would be a memory leak
// on demand. Past the cap the map is simply reset.
const embedOriginsCacheMaxEntries = 10000

func (c *embedOriginsCache) set(key string, origins []string) {
	if c == nil {
		return
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	if len(c.entries) >= embedOriginsCacheMaxEntries {
		c.entries = make(map[string]embedOriginsEntry)
	}

	c.entries[key] = embedOriginsEntry{origins: origins, expiresAt: time.Now().Add(c.ttl)}
}

// setupSecurityHeaders builds the policy builder from the post-overlay
// config. Called from SetupRoutes, after InitializeSystemConfig, so a
// headers.csp_extra_sources set in the database is honored.
func (s *Server) setupSecurityHeaders(ctx context.Context) {
	builder, errs := securityheaders.New(securityheaders.Options{
		ExtraSources:        s.config.Headers.CSPExtraSources,
		DashboardThirdParty: dashboardThirdPartyOrigins(s.config.PostHog),
	})

	for _, err := range errs {
		slog.ErrorContext(ctx, "Ignoring invalid headers.csp_extra_sources entry", "error", err)
	}

	if s.config.Headers.CSPExtraSources != "" {
		slog.InfoContext(ctx, "Content-Security-Policy widened by headers.csp_extra_sources",
			"value", s.config.Headers.CSPExtraSources)
	}

	s.securityHeaders = builder
}

// dashboardThirdPartyOrigins returns the analytics origins the dashboard
// talks to directly. With the default configuration posthog-js goes through
// the first-party /ingest proxy and this is only the PostHog UI host (the
// toolbar); an operator-configured posthog.host is an external origin the
// dashboard loads scripts from and posts to, so it has to be allowed.
func dashboardThirdPartyOrigins(cfg config.PostHogConfig) []string {
	if !cfg.Active() {
		return nil
	}

	origins := make([]string, 0, 3)

	for _, raw := range []string{cfg.BrowserAPIHost(), cfg.BrowserUIHost()} {
		if origin := originOf(raw); origin != "" {
			origins = append(origins, origin)
		}
	}

	if assets := postHogCloudAssetsOrigin(originOf(cfg.BrowserAPIHost())); assets != "" {
		origins = append(origins, assets)
	}

	return origins
}

// postHogCloudIngestSuffix is the host suffix of PostHog Cloud's regional
// ingestion endpoints (eu.i.posthog.com, us.i.posthog.com).
const postHogCloudIngestSuffix = ".i.posthog.com"

// postHogCloudAssetsOrigin returns the static-assets origin posthog-js loads
// its lazy bundles from (session recorder, exception autocapture, surveys)
// when api_host is a PostHog Cloud ingestion host: posthog-js rewrites
// "<region>.i.posthog.com" to "<region>-assets.i.posthog.com" for /static/.
// config.DefaultPostHogAssetsHost is the EU instance of the same rule. Any
// other host (self-hosted PostHog, an external reverse proxy, the first-party
// /ingest path) serves /static/ itself, so it gets nothing extra.
func postHogCloudAssetsOrigin(apiOrigin string) string {
	host, ok := strings.CutPrefix(apiOrigin, "https://")
	if !ok {
		return ""
	}

	region, ok := strings.CutSuffix(host, postHogCloudIngestSuffix)
	if !ok || region == "" || strings.ContainsAny(region, ".:") || strings.HasSuffix(region, "-assets") {
		return ""
	}

	return "https://" + region + "-assets" + postHogCloudIngestSuffix
}

// originOf returns scheme://host[:port] for an absolute http(s) URL, and ""
// for anything else (a relative path such as /ingest is already 'self').
func originOf(raw string) string {
	raw = strings.TrimSpace(raw)

	scheme, rest, ok := strings.Cut(raw, "://")
	if !ok || (scheme != "https" && scheme != "http") {
		return ""
	}

	host, _, _ := strings.Cut(rest, "/")
	if host == "" {
		return ""
	}

	return strings.ToLower(scheme + "://" + host)
}

// dash0ShellScriptHashes hashes the inline scripts of the embedded dash0
// shell once: the embedded file never changes for the life of the process.
//
//nolint:gochecknoglobals // sync.OnceValue memo over an immutable embed.FS.
var dash0ShellScriptHashes = sync.OnceValue(func() []string {
	data, err := fs.ReadFile(dash0Files, "dash0res/index.html")
	if err != nil {
		return nil
	}

	return securityheaders.InlineScriptHashes(data)
})

// applyDashboardHeaders stamps the dash0 policy on a response.
func (s *Server) applyDashboardHeaders(writer http.ResponseWriter, req *http.Request) {
	s.securityHeaders.Dashboard(securityheaders.Params{
		ScriptHashes: dash0ShellScriptHashes(),
		SelfOrigins:  securityheaders.RequestOrigins(req),
	}).Apply(writer.Header())
}

// status0ShellScriptHashes hashes the inline scripts of the UNTOUCHED
// embedded status0 shell, once per Server (status0FS is fixed for its life).
//
// Deliberately not the per-request bytes: the served shell is rewritten with
// page metadata (injectStatus0Meta), and hashing after that rewrite would
// hash-allow any <script> an injection bug ever smuggled into it. Only what
// the build emitted is allowed.
func (s *Server) status0ShellScriptHashes() []string {
	s.status0HashesOnce.Do(func() {
		data, err := fs.ReadFile(s.status0FSOrDefault(), status0ShellPath)
		if err != nil {
			return
		}

		s.status0Hashes = securityheaders.InlineScriptHashes(data)
	})

	return s.status0Hashes
}

// status0ShellPath is the embedded status0 SPA shell.
const status0ShellPath = "status0res/index.html"

// applyStatusPageHeaders stamps the status-page policy on a response. shell
// says whether the response is the SPA shell (which needs its inline-script
// hashes) rather than an asset, and embedOrigins is the owning org's allowlist
// (nil when there is none or the org is unknown).
func (s *Server) applyStatusPageHeaders(writer http.ResponseWriter, shell bool, embedOrigins []string) {
	var hashes []string
	if shell {
		hashes = s.status0ShellScriptHashes()
	}

	s.securityHeaders.StatusPage(securityheaders.Params{
		ScriptHashes: hashes,
		EmbedOrigins: embedOrigins,
	}).Apply(writer.Header())
}

// applyBaselineHeaders stamps the framing-only policy on a response.
func (s *Server) applyBaselineHeaders(writer http.ResponseWriter) {
	s.securityHeaders.Baseline().Apply(writer.Header())
}

// statusPathOrgSlug returns the org slug a /s/... path belongs to: its first
// segment after the base path, whatever the depth (/s/acme/api/tv is still
// acme's page). "" for the bare base path.
func statusPathOrgSlug(reqPath string) string {
	rel := strings.Trim(strings.TrimPrefix(reqPath, config.StatusBasePath), "/")
	slug, _, _ := strings.Cut(rel, "/")

	return strings.ToLower(slug)
}

// statusPageEmbedOrigins resolves an org's statuspage.allowed_embed_origins,
// cached for embedOriginsCacheTTL. Any failure answers "no extra origins",
// which is the safe direction: the page is still served, just not frameable
// by third parties.
func (s *Server) statusPageEmbedOrigins(ctx context.Context, orgSlug string) []string {
	if orgSlug == "" || s.dbService == nil {
		return nil
	}

	if origins, ok := s.embedOrigins.get(orgSlug); ok {
		return origins
	}

	var origins []string

	if org, err := s.dbService.GetOrganizationBySlug(ctx, orgSlug); err == nil && org != nil {
		origins = s.orgEmbedOrigins(ctx, org.UID)
	}

	s.embedOrigins.set(orgSlug, origins)

	return origins
}

// orgEmbedOrigins reads the allowlist of an org already resolved by UID.
func (s *Server) orgEmbedOrigins(ctx context.Context, orgUID string) []string {
	if s.dbService == nil {
		return nil
	}

	param, err := s.dbService.GetOrgParameter(ctx, orgUID, models.ParamKeyStatusPageAllowedEmbedOrigins)
	if err != nil {
		slog.WarnContext(ctx, "Reading status page embed origins failed", "orgUid", orgUID, "error", err)

		return nil
	}

	origins := auth.EmbedOriginsFromParam(param)
	if len(origins) == 0 {
		return nil
	}

	return origins
}
