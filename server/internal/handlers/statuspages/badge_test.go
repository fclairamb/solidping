package statuspages

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/handlers/badges"
)

// --- Public status page SVG badge (spec 2026-08-08-07) ---
//
// GET /api/v1/status-pages/:org/:slug/badge is the static, script-free sibling
// of the per-check badges and the JS embed widget: a shields.io-style SVG for
// READMEs/wikis/email footers. These tests pin: one badge per rollup state
// (text + color), the same visibility gate as the summary endpoint (404,
// existence not leaked), the cache header, and that operator-authored text
// (the page name used as the default label) is XML-escaped.

// newBadgeRequest builds an httptest.Request with chi route params set the
// way the real router would for GET /status-pages/{org}/{slug}/badge, with
// optional query parameters.
func newBadgeRequest(orgSlug, slug, rawQuery string) (*http.Request, *httptest.ResponseRecorder) {
	target := "/api/v1/status-pages/" + orgSlug + "/" + slug + "/badge"
	if rawQuery != "" {
		target += "?" + rawQuery
	}

	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, target, nil)

	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("org", orgSlug)
	rctx.URLParams.Add("slug", slug)
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	return req, httptest.NewRecorder()
}

// TestGetBadge_RollupStates pins the right-side text and fill color the badge
// renders for each page-level rollup state, from the exact same seeding
// TestViewStatusPage_OverallStatus uses for the other rollup-consuming
// surfaces (summary, full page view).
func TestGetBadge_RollupStates(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name      string
		resources []resourceSpec
		wantValue string
		wantColor string
	}{
		{
			name:      "no resources -> unknown/gray",
			resources: nil,
			wantValue: "unknown",
			wantColor: badges.ColorGray,
		},
		{
			name:      "all up -> operational/green",
			resources: []resourceSpec{{slug: "res-a", status: models.CheckStatusUp}},
			wantValue: "operational",
			wantColor: badges.ColorGreen,
		},
		{
			name: "one degraded -> degraded/yellow",
			resources: []resourceSpec{
				{slug: "res-a", status: models.CheckStatusUp},
				{slug: "res-b", status: models.CheckStatusDegraded},
			},
			wantValue: "degraded",
			wantColor: badges.ColorYellow,
		},
		{
			name: "one down -> down/red",
			resources: []resourceSpec{
				{slug: "res-a", status: models.CheckStatusUp},
				{slug: "res-b", status: models.CheckStatusDown},
			},
			wantValue: "down",
			wantColor: badges.ColorRed,
		},
		{
			name: "maintenance, otherwise clean -> maintenance/blue",
			resources: []resourceSpec{
				{slug: "res-a", status: models.CheckStatusUp, inMaintenance: true},
			},
			wantValue: "maintenance",
			wantColor: badges.ColorBlue,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			r := require.New(t)
			ctx, svc, org := setupStatusPagesTest(t)
			page, section := seedPageWithSection(ctx, t, svc, org.Slug, "")

			for _, spec := range tc.resources {
				seedResourceWithStatus(ctx, t, svc, org.UID, org.Slug, page, section, spec)
			}

			h := NewHandler(svc, &config.Config{})

			req, rec := newBadgeRequest(org.Slug, page.Slug, "")
			r.NoError(h.GetBadge(rec, req))
			r.Equal(http.StatusOK, rec.Code)
			r.Equal("image/svg+xml", rec.Header().Get("Content-Type"))

			body := rec.Body.String()
			r.Contains(body, ">"+tc.wantValue+"<")
			r.Contains(body, tc.wantColor)
			// Default label is the page name.
			r.Contains(body, ">"+page.Name+"<")
		})
	}
}

// TestGetBadge_CacheControl pins the 60s public cache header, matching the
// per-check badges and the summary endpoint.
func TestGetBadge_CacheControl(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, svc, org := setupStatusPagesTest(t)
	page, _ := seedPageWithSection(ctx, t, svc, org.Slug, "")

	h := NewHandler(svc, &config.Config{})

	req, rec := newBadgeRequest(org.Slug, page.Slug, "")
	r.NoError(h.GetBadge(rec, req))
	r.Equal("public, max-age=60", rec.Header().Get("Cache-Control"))
}

// TestGetBadge_NotFound pins that a private page and a disabled page both
// 404 with the same status-page-not-found JSON shape as the summary/full page
// view — leaking nothing, not even existence — with a public page as a
// positive control that the handler otherwise works.
func TestGetBadge_NotFound(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, svc, org := setupStatusPagesTest(t)

	publicPage, _ := seedPageWithSection(ctx, t, svc, org.Slug, "")

	privateVisibility := "private"
	privateReq := &CreateStatusPageRequest{Name: "Private", Slug: "private-page", Visibility: &privateVisibility}
	privatePage, err := svc.CreateStatusPage(ctx, org.Slug, privateReq)
	r.NoError(err)

	disabledReq := &CreateStatusPageRequest{Name: "Disabled", Slug: "disabled-page"}
	disabledPage, err := svc.CreateStatusPage(ctx, org.Slug, disabledReq)
	r.NoError(err)
	falseVal := false
	r.NoError(svc.db.UpdateStatusPage(ctx, disabledPage.UID, &models.StatusPageUpdate{Enabled: &falseVal}))

	h := NewHandler(svc, &config.Config{})

	// Positive control: the public page succeeds.
	req, rec := newBadgeRequest(org.Slug, publicPage.Slug, "")
	r.NoError(h.GetBadge(rec, req))
	r.Equal(http.StatusOK, rec.Code)

	// Private page: 404, same shape as the summary endpoint's not-found.
	req, rec = newBadgeRequest(org.Slug, privatePage.Slug, "")
	r.NoError(h.GetBadge(rec, req))
	r.Equal(http.StatusNotFound, rec.Code)

	// Disabled page: same 404.
	req, rec = newBadgeRequest(org.Slug, disabledPage.Slug, "")
	r.NoError(h.GetBadge(rec, req))
	r.Equal(http.StatusNotFound, rec.Code)

	// Non-existent slug: identical 404 (existence is not leaked).
	req, rec = newBadgeRequest(org.Slug, "does-not-exist", "")
	r.NoError(h.GetBadge(rec, req))
	r.Equal(http.StatusNotFound, rec.Code)
}

// TestGetBadge_LabelIsXMLEscaped pins that operator-authored text — the page
// name, used as the default left-side label — is XML-escaped before being
// interpolated into the SVG. The badge is unauthenticated and commonly
// embedded raw (e.g. an <img> in a third-party README), so unescaped
// '<', '&', or '"' in a page name would corrupt or inject into the SVG markup.
func TestGetBadge_LabelIsXMLEscaped(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, svc, org := setupStatusPagesTest(t)

	name := `<script>&"evil"</script>`
	createReq := &CreateStatusPageRequest{Name: name, Slug: "escape-me"}
	page, err := svc.CreateStatusPage(ctx, org.Slug, createReq)
	r.NoError(err)

	h := NewHandler(svc, &config.Config{})

	req, rec := newBadgeRequest(org.Slug, page.Slug, "")
	r.NoError(h.GetBadge(rec, req))
	r.Equal(http.StatusOK, rec.Code)

	body := rec.Body.String()
	r.NotContains(body, "<script>")
	r.Contains(body, "&lt;script&gt;&amp;&quot;evil&quot;&lt;/script&gt;")
}

// TestGetBadge_QueryParams pins that label/style/minWidth/width query
// parameters are honored, mirroring the per-check badge's contract.
func TestGetBadge_QueryParams(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, svc, org := setupStatusPagesTest(t)
	page, _ := seedPageWithSection(ctx, t, svc, org.Slug, "")

	h := NewHandler(svc, &config.Config{})

	req, rec := newBadgeRequest(org.Slug, page.Slug, "label=Custom+Label&style=flat-square&minWidth=400")
	r.NoError(h.GetBadge(rec, req))
	r.Equal(http.StatusOK, rec.Code)

	body := rec.Body.String()
	r.Contains(body, ">Custom Label<")
	r.Contains(body, `width="400"`)
	// flat-square style renders a square (rx="0") badge.
	r.Contains(body, `rx="0"`)
}
