package app

import (
	"context"
	"crypto/tls"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/handlers/statuspages"
)

func TestStatusPagePathParts(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		reqPath  string
		wantOrg  string
		wantSlug string
		wantOK   bool
	}{
		{name: "root", reqPath: "/", wantOK: false},
		{name: "empty", reqPath: "", wantOK: false},
		{name: "org only", reqPath: "/acme", wantOrg: "acme", wantSlug: "", wantOK: true},
		{name: "org trailing slash", reqPath: "/acme/", wantOrg: "acme", wantSlug: "", wantOK: true},
		{name: "org and slug", reqPath: "/acme/api", wantOrg: "acme", wantSlug: "api", wantOK: true},
		{name: "org and slug trailing slash", reqPath: "/acme/api/", wantOrg: "acme", wantSlug: "api", wantOK: true},
		{name: "three segments", reqPath: "/acme/api/extra", wantOK: false},
		{name: "empty middle segment", reqPath: "/acme//api", wantOK: false},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)

			org, slug, ok := statusPagePathParts(testCase.reqPath)
			r.Equal(testCase.wantOK, ok)

			if testCase.wantOK {
				r.Equal(testCase.wantOrg, org)
				r.Equal(testCase.wantSlug, slug)
			}
		})
	}
}

func TestRequestScheme(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		forwardedProt string
		tls           bool
		want          string
	}{
		{name: "forwarded https", forwardedProt: "https", want: "https"},
		{name: "forwarded http", forwardedProt: "http", want: "http"},
		{name: "forwarded list takes first", forwardedProt: "https, http", want: "https"},
		{name: "forwarded blank falls through to tls", forwardedProt: "", tls: true, want: "https"},
		{name: "no forwarded no tls defaults http", want: "http"},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)

			req, err := http.NewRequestWithContext(
				context.Background(), http.MethodGet, "http://example.com/status0/acme", nil)
			r.NoError(err)

			if testCase.forwardedProt != "" {
				req.Header.Set("X-Forwarded-Proto", testCase.forwardedProt)
			}

			if testCase.tls {
				req.TLS = &tls.ConnectionState{}
			}

			r.Equal(testCase.want, requestScheme(req))
		})
	}
}

func TestRequestOrigin(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	req, err := http.NewRequestWithContext(
		context.Background(), http.MethodGet, "http://status.example.com/status0/acme", nil)
	r.NoError(err)
	req.Header.Set("X-Forwarded-Proto", "https")

	r.Equal("https://status.example.com", requestOrigin(req))
}

func TestBuildStatus0MetaTags(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	block := buildStatus0MetaTags(&ogMetadata{
		Title:       "Acme API — Status",
		Description: "Our public API status",
		URL:         "https://status.example.com/status0/acme/api",
		Image:       "https://status.example.com/status0/og-default.png",
	})

	r.Contains(block, "<title>Acme API — Status</title>")
	r.Contains(block, `<meta property="og:title" content="Acme API — Status" />`)
	r.Contains(block, `<meta property="og:description" content="Our public API status" />`)
	r.Contains(block, `<meta property="og:type" content="website" />`)
	r.Contains(block, `<meta property="og:site_name" content="SolidPing" />`)
	r.Contains(block, `<meta property="og:url" content="https://status.example.com/status0/acme/api" />`)
	r.Contains(block, `<meta property="og:image" content="https://status.example.com/status0/og-default.png" />`)
	r.Contains(block, `<meta name="twitter:card" content="summary_large_image" />`)
	r.Contains(block, `<meta name="twitter:image" content="https://status.example.com/status0/og-default.png" />`)
	r.Contains(block, `<meta name="description" content="Our public API status" />`)
}

func TestBuildStatus0MetaTagsSpPage(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	// With Page set (custom-host serving) the sp-page bootstrap tag appears.
	withPage := buildStatus0MetaTags(&ogMetadata{
		Title: "Acme — Status",
		URL:   "https://status.acme.com/",
		Page:  "acme/main",
	})
	r.Contains(withPage, `<meta name="sp-page" content="acme/main" />`)

	// Without Page (path-based serving) the tag must not appear.
	withoutPage := buildStatus0MetaTags(&ogMetadata{
		Title: "Acme — Status",
		URL:   "https://solidping.io/status0/acme/main",
	})
	r.NotContains(withoutPage, "sp-page")
}

func TestBuildStatus0MetaTagsEscaping(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	block := buildStatus0MetaTags(&ogMetadata{
		Title:       `A & B <script> "x"` + ogTitleSuffix,
		Description: `desc & "quoted" <b>`,
		URL:         "https://status.example.com/status0/a%26b",
		Image:       "https://status.example.com/status0/og-default.png",
	})

	// No raw special characters leak into the markup.
	r.NotContains(block, "<script>")
	r.NotContains(block, `"x"</title>`)
	r.Contains(block, "&amp;")
	r.Contains(block, "&lt;script&gt;")
	r.Contains(block, "&#34;")
}

func TestInjectStatus0Meta(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	const doc = `<!DOCTYPE html>
<html lang="en">
  <head>
    <meta charset="UTF-8" />
    <title>SolidPing - Status Page</title>
    <meta name="google" content="notranslate" />
  </head>
  <body><div id="root"></div></body>
</html>`

	out := injectStatus0Meta(doc, &ogMetadata{
		Title:       "Acme API — Status",
		Description: "Our public API status",
		URL:         "https://status.example.com/status0/acme/api",
		Image:       "https://status.example.com/status0/og-default.png",
	})

	// The static default title is gone, the per-page one is present.
	r.NotContains(out, "SolidPing - Status Page")
	r.Contains(out, "<title>Acme API — Status</title>")
	// The injected block sits before </head>.
	r.Contains(out, `og:title`)
	r.Less(strings.Index(out, "og:title"), strings.Index(out, "</head>"))
	// Untouched markup survives.
	r.Contains(out, `<meta name="google" content="notranslate" />`)
	r.Contains(out, `<div id="root"></div>`)
}

func TestInjectStatus0MetaNoHead(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	const doc = `<title>x</title><p>no head here</p>`

	out := injectStatus0Meta(doc, &ogMetadata{Title: "T", Description: "D"})
	// Without a </head> the document is returned with the title stripped and
	// no partial block spliced in.
	r.NotContains(out, "og:title")
}

// --- No-existence-leak guardrail (spec 2026-07-10-13) ---

// status0MetaGenericDoc is a minimal stand-in for the served status0
// index.html head. It carries the static default title only, so a page that
// does not resolve must produce a byte-for-byte identical response.
const status0MetaGenericDoc = `<!DOCTYPE html>
<html lang="en">
  <head>
    <meta charset="UTF-8" />
    <title>SolidPing - Status Page</title>
  </head>
  <body><div id="root"></div></body>
</html>`

// status0MetaPublicPageName is the name of the seeded enabled+public page. It
// is distinctive so the assertions can prove it surfaces for the positive
// control and never leaks for the missing/disabled/private cases.
const status0MetaPublicPageName = "Acme Public Status"

func strPtr(s string) *string { return &s }

func boolPtr(b bool) *bool { return &b }

// setupStatus0MetaServer wires a Server whose statusPagesService is backed by an
// in-memory SQLite DB seeded with the three page states the anti-leak guardrail
// must all map to the generic head — a disabled page and a private (non-public)
// page — plus an enabled public page as the positive control (created first, so
// it is also the org's default page).
func setupStatus0MetaServer(t *testing.T) (context.Context, *Server) {
	t.Helper()
	r := require.New(t)
	ctx := t.Context()

	dbService, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbService.Initialize(ctx))
	t.Cleanup(func() { _ = dbService.Close() })

	org := models.NewOrganization("acme", "Acme")
	r.NoError(dbService.CreateOrganization(ctx, org))

	svc := statuspages.NewService(dbService, &config.Config{}, nil)

	// Enabled + public — the positive control (first page → org default too).
	_, err = svc.CreateStatusPage(ctx, org.Slug, &statuspages.CreateStatusPageRequest{
		Name: status0MetaPublicPageName, Slug: "public",
	})
	r.NoError(err)

	// Disabled page: must serve the generic head, no name leak.
	_, err = svc.CreateStatusPage(ctx, org.Slug, &statuspages.CreateStatusPageRequest{
		Name: "Acme Disabled Secret", Slug: "disabled",
	})
	r.NoError(err)
	_, err = svc.UpdateStatusPage(ctx, org.Slug, "disabled", &statuspages.UpdateStatusPageRequest{
		Enabled: boolPtr(false),
	})
	r.NoError(err)

	// Private (non-public visibility) page: must serve the generic head too.
	_, err = svc.CreateStatusPage(ctx, org.Slug, &statuspages.CreateStatusPageRequest{
		Name: "Acme Private Secret", Slug: "private", Visibility: strPtr("private"),
	})
	r.NoError(err)

	// Password-protected page: must ALSO serve the generic head. This one is
	// load-bearing for caching (spec 2026-08-22-06) — status0MetaForPath runs
	// without the request's unlock grant, so statuspagelock.Allows denies by
	// default and no gated page's name reaches the shell. That is precisely why
	// the path-based shell stays a single shared-cacheable artifact while its
	// custom-domain twin, which DOES inject unconditionally, must not.
	_, err = svc.CreateStatusPage(ctx, org.Slug, &statuspages.CreateStatusPageRequest{
		Name: "Acme Password Secret", Slug: "locked",
		Visibility: strPtr("password"), Password: strPtr("correct-horse"),
	})
	r.NoError(err)

	return ctx, &Server{statusPagesService: svc}
}

// TestStatus0MetaForPath_NoExistenceLeak is the guardrail the spec's "### Tests"
// section calls for: a missing, disabled, or private status page must serve the
// SAME generic head as before, so metadata cannot be used to probe page
// existence. It drives status0MetaForPath through the real public lookups
// (ViewStatusPage / ViewDefaultStatusPage) and, mirroring the serveStatus0Static
// caller, injects only when ok — then asserts the negative cases are byte-for-byte
// identical to the generic head while the positive control surfaces the page name.
func TestStatus0MetaForPath_NoExistenceLeak(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		reqPath string
		wantOK  bool
	}{
		{name: "enabled public page (positive control)", reqPath: "/acme/public", wantOK: true},
		{name: "org default page (positive control)", reqPath: "/acme", wantOK: true},
		{name: "unknown org", reqPath: "/ghostorg", wantOK: false},
		{name: "unknown slug", reqPath: "/acme/nope", wantOK: false},
		{name: "disabled page", reqPath: "/acme/disabled", wantOK: false},
		{name: "private page", reqPath: "/acme/private", wantOK: false},
		{name: "password page", reqPath: "/acme/locked", wantOK: false},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)

			ctx, srv := setupStatus0MetaServer(t)

			httpReq, err := http.NewRequestWithContext(
				ctx, http.MethodGet, "http://status.example.com/status0"+testCase.reqPath, nil)
			r.NoError(err)
			req := httpReq

			meta, ok := srv.status0MetaForPath(req, testCase.reqPath)
			r.Equal(testCase.wantOK, ok)

			// Mirror serveStatus0Static: only rewrite the served bytes when ok.
			served := status0MetaGenericDoc
			if ok {
				served = injectStatus0Meta(status0MetaGenericDoc, &meta)
			}

			if testCase.wantOK {
				// Positive control: the page name reaches both the metadata and
				// the served head, so the negative cases below can't false-pass.
				r.Contains(meta.Title, status0MetaPublicPageName)
				r.Contains(served, status0MetaPublicPageName)
				r.NotEqual(status0MetaGenericDoc, served)
			} else {
				// Anti-leak: byte-for-byte identical to the generic head, and no
				// seeded page's name leaks through for missing/disabled/private.
				r.Equal(status0MetaGenericDoc, served)
				r.NotContains(served, status0MetaPublicPageName)
				r.NotContains(served, "Secret")
				r.NotContains(served, "Password")
			}
		})
	}
}

// TestStatus0MetaForPath_NilServiceIsGeneric pins that a Server without a
// statusPagesService also falls back to the generic head instead of panicking —
// the nil-service guard in status0MetaForPath.
func TestStatus0MetaForPath_NilServiceIsGeneric(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	httpReq, err := http.NewRequestWithContext(
		context.Background(), http.MethodGet, "http://status.example.com/status0/acme/public", nil)
	r.NoError(err)

	_, ok := (&Server{}).status0MetaForPath(httpReq, "/acme/public")
	r.False(ok)
}
