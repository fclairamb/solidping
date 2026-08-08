package statuspages

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db/models"
)

// --- Public status page summary endpoint (spec 2026-08-08-06) ---
//
// GET /api/v1/status-pages/:org/:slug/summary is the cheap "is it up?"
// companion to the full public page view. These tests pin: it reuses the
// exact same rollup (models.RollupPageStatus) and visibility gate as
// ViewStatusPage, sets Cache-Control, and derives page.url from either the
// verified custom domain or the request's own host.

// newSummaryRequest builds an httptest.Request with chi route params set the
// way the real router would for GET /status-pages/{org}/{slug}/summary.
func newSummaryRequest(orgSlug, slug string) (*http.Request, *httptest.ResponseRecorder) {
	req := httptest.NewRequestWithContext(
		context.Background(), http.MethodGet, "/api/v1/status-pages/"+orgSlug+"/"+slug+"/summary", nil)

	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("org", orgSlug)
	rctx.URLParams.Add("slug", slug)
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	return req, httptest.NewRecorder()
}

// TestViewStatusPageSummary_ResponseShapeAndCounts pins the wire shape and
// that the rollup/counts agree with the same fixture ViewStatusPage uses.
func TestViewStatusPageSummary_ResponseShapeAndCounts(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, svc, org := setupStatusPagesTest(t)
	page, section := seedPageWithSection(ctx, t, svc, org.Slug, "")

	seedResourceWithStatus(ctx, t, svc, org.UID, org.Slug, page, section,
		resourceSpec{slug: "res-a", status: models.CheckStatusUp})
	seedResourceWithStatus(ctx, t, svc, org.UID, org.Slug, page, section,
		resourceSpec{slug: "res-b", status: models.CheckStatusDegraded})

	h := NewHandler(svc, &config.Config{})

	req, rec := newSummaryRequest(org.Slug, page.Slug)
	r.NoError(h.ViewStatusPageSummary(rec, req))
	r.Equal(http.StatusOK, rec.Code)

	var resp StatusPageSummaryResponse
	r.NoError(json.Unmarshal(rec.Body.Bytes(), &resp))

	r.Equal("degraded", resp.Status)
	r.Equal(StatusCountsResponse{Operational: 1, Degraded: 1}, resp.Counts)
	r.Equal(page.Name, resp.Page.Name)
	r.Equal(page.Slug, resp.Page.Slug)
	r.False(resp.GeneratedAt.IsZero())

	// Same rollup the full page view returns, from the exact same live data.
	view, err := svc.ViewStatusPage(ctx, org.Slug, page.Slug)
	r.NoError(err)
	r.Equal(view.OverallStatus, resp.Status)
	r.Equal(*view.StatusCounts, resp.Counts)
}

// TestViewStatusPageSummary_CacheControl pins the 60s public cache header the
// full page view deliberately does not set.
func TestViewStatusPageSummary_CacheControl(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, svc, org := setupStatusPagesTest(t)
	page, _ := seedPageWithSection(ctx, t, svc, org.Slug, "")

	h := NewHandler(svc, &config.Config{})

	req, rec := newSummaryRequest(org.Slug, page.Slug)
	r.NoError(h.ViewStatusPageSummary(rec, req))
	r.Equal("public, max-age=60", rec.Header().Get("Cache-Control"))
}

// TestViewStatusPageSummary_NotFound pins that a private page and a disabled
// page both 404 with the same status-page-not-found shape as ViewStatusPage —
// leaking nothing, not even existence — with a public page as a positive
// control that the handler otherwise works.
func TestViewStatusPageSummary_NotFound(t *testing.T) {
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
	req, rec := newSummaryRequest(org.Slug, publicPage.Slug)
	r.NoError(h.ViewStatusPageSummary(rec, req))
	r.Equal(http.StatusOK, rec.Code)

	// Private page: 404, not-found shape.
	req, rec = newSummaryRequest(org.Slug, privatePage.Slug)
	r.NoError(h.ViewStatusPageSummary(rec, req))
	r.Equal(http.StatusNotFound, rec.Code)
	var privBody map[string]any
	r.NoError(json.Unmarshal(rec.Body.Bytes(), &privBody))
	r.Equal("STATUS_PAGE_NOT_FOUND", privBody["code"])

	// Disabled page: same 404 shape.
	req, rec = newSummaryRequest(org.Slug, disabledPage.Slug)
	r.NoError(h.ViewStatusPageSummary(rec, req))
	r.Equal(http.StatusNotFound, rec.Code)
	var disBody map[string]any
	r.NoError(json.Unmarshal(rec.Body.Bytes(), &disBody))
	r.Equal(privBody["code"], disBody["code"])

	// Non-existent slug: identical 404 shape (existence is not leaked).
	req, rec = newSummaryRequest(org.Slug, "does-not-exist")
	r.NoError(h.ViewStatusPageSummary(rec, req))
	r.Equal(http.StatusNotFound, rec.Code)
	var missingBody map[string]any
	r.NoError(json.Unmarshal(rec.Body.Bytes(), &missingBody))
	r.Equal(privBody["code"], missingBody["code"])
}

// TestViewStatusPageSummary_PageURL pins that page.url is the path-based
// /status0/{org}/{slug} URL derived from the request host when there is no
// verified custom domain, and the custom-domain URL when one is verified.
func TestViewStatusPageSummary_PageURL(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, svc, org := setupStatusPagesTest(t)
	page, _ := seedPageWithSection(ctx, t, svc, org.Slug, "")

	h := NewHandler(svc, &config.Config{})

	req, rec := newSummaryRequest(org.Slug, page.Slug)
	req.Host = "app.example.com"
	r.NoError(h.ViewStatusPageSummary(rec, req))

	var resp StatusPageSummaryResponse
	r.NoError(json.Unmarshal(rec.Body.Bytes(), &resp))
	r.Equal("http://app.example.com/status0/"+org.Slug+"/"+page.Slug, resp.Page.URL)

	// Verify a custom domain directly via the DB (VerifyCustomDomain requires
	// live DNS resolution, out of scope for this unit test).
	customDomain := "status.example.com"
	token := "tok"
	now := time.Now()
	r.NoError(svc.db.UpdateStatusPageCustomDomain(ctx, page.UID, &models.StatusPageCustomDomainUpdate{
		Domain: &customDomain, Token: &token, VerifiedAt: &now, CheckedAt: &now,
	}))

	req, rec = newSummaryRequest(org.Slug, page.Slug)
	req.Host = "app.example.com"
	r.NoError(h.ViewStatusPageSummary(rec, req))
	r.NoError(json.Unmarshal(rec.Body.Bytes(), &resp))
	r.Equal("https://"+customDomain+"/", resp.Page.URL)
}
