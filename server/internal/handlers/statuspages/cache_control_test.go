package statuspages

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/statuspagecache"
	"github.com/fclairamb/solidping/server/internal/statuspagelock"
)

// --- Cache directives on the public surface (spec 2026-08-22-06) ---
//
// The page view, the summary and the badge share one visibility gate and one
// caching helper. What these tests defend is the direction that costs
// something when it breaks: a `password` or `private` page must never carry
// the `public` cache token, because a CDN or corporate proxy holding that
// token will happily re-serve the body to a visitor who never presented the
// password. Every such negative assertion here is paired with a public-page
// positive control, so a fixture that silently stopped producing responses
// could not make the suite pass.

// newPageViewRequest builds an httptest.Request with chi route params set the
// way the real router would for GET /status-pages/{org}/{slug}.
func newPageViewRequest(orgSlug, slug string) (*http.Request, *httptest.ResponseRecorder) {
	req := httptest.NewRequestWithContext(
		context.Background(), http.MethodGet, "/api/v1/status-pages/"+orgSlug+"/"+slug, nil)

	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("org", orgSlug)
	rctx.URLParams.Add("slug", slug)
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	return req, httptest.NewRecorder()
}

// publicEndpoint is one of the three public reads under test, named and
// invoked uniformly so a table can walk all of them per visibility.
type publicEndpoint struct {
	name    string
	request func(orgSlug, slug string) (*http.Request, *httptest.ResponseRecorder)
	call    func(h *Handler, w http.ResponseWriter, r *http.Request) error
}

func publicEndpoints() []publicEndpoint {
	return []publicEndpoint{
		{
			name:    "page",
			request: newPageViewRequest,
			call: func(h *Handler, w http.ResponseWriter, r *http.Request) error {
				return h.ViewStatusPage(w, r)
			},
		},
		{
			name:    "summary",
			request: newSummaryRequest,
			call: func(h *Handler, w http.ResponseWriter, r *http.Request) error {
				return h.ViewStatusPageSummary(w, r)
			},
		},
		{
			name: "badge",
			request: func(orgSlug, slug string) (*http.Request, *httptest.ResponseRecorder) {
				return newBadgeRequest(orgSlug, slug, "")
			},
			call: func(h *Handler, w http.ResponseWriter, r *http.Request) error {
				return h.GetBadge(w, r)
			},
		},
	}
}

// seedPageAtPublicSlug creates a live page at testPublicSlug with the given
// visibility and returns the stored row (needed for its UID and password
// hash). It goes through CreateStatusPage rather than writing the row so the
// fixture exercises the same validation the API does.
func seedPageAtPublicSlug(
	ctx context.Context, t *testing.T, dbService db.Service, svc *Service,
	orgUID, visibility string,
) *models.StatusPage {
	t.Helper()

	r := require.New(t)

	req := &CreateStatusPageRequest{
		Name:       "Acme Status",
		Slug:       testPublicSlug,
		Visibility: &visibility,
	}

	if visibility == models.StatusPageVisibilityPassword {
		password := testPagePassword
		req.Password = &password
	}

	_, err := svc.CreateStatusPage(ctx, "acme", req)
	r.NoError(err)

	page, err := dbService.GetStatusPageBySlug(ctx, orgUID, testPublicSlug)
	r.NoError(err)

	return page
}

// TestPublicCacheControlFollowsVisibility is the spec's core table: for each
// visibility, every one of the three public endpoints must answer with the
// same directive, and the two gated visibilities must not carry the `public`
// token at all — asserted by absence, not merely by "something else is set".
func TestPublicCacheControlFollowsVisibility(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name         string
		visibility   string
		wantStatus   int
		wantCache    string
		wantPublicOK bool
	}{
		{
			name:         "public page is shared-cacheable for 60s",
			visibility:   models.StatusPageVisibilityPublic,
			wantStatus:   http.StatusOK,
			wantCache:    "public, max-age=60",
			wantPublicOK: true,
		},
		{
			name:       "locked password page is never stored",
			visibility: models.StatusPageVisibilityPassword,
			wantStatus: http.StatusUnauthorized,
			wantCache:  "private, no-store",
		},
		{
			name:       "private page 404s and the 404 is never stored",
			visibility: models.StatusPageVisibilityPrivate,
			wantStatus: http.StatusNotFound,
			wantCache:  "private, no-store",
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			r := require.New(t)
			ctx, dbService, svc, org := passwordSetup(t)
			seedPageAtPublicSlug(ctx, t, dbService, svc, org.UID, testCase.visibility)

			h := NewHandler(svc, &config.Config{})

			for _, endpoint := range publicEndpoints() {
				req, rec := endpoint.request("acme", testPublicSlug)
				// The real router installs the request's unlock grant on every
				// public status-page read; without it a `password` page would
				// be denied for the wrong reason.
				req = statuspagelock.WithRequestGrant(req)

				r.NoError(endpoint.call(h, rec, req), endpoint.name)
				r.Equal(testCase.wantStatus, rec.Code, endpoint.name)

				cacheControl := rec.Header().Get("Cache-Control")
				r.Equal(testCase.wantCache, cacheControl, endpoint.name)

				if !testCase.wantPublicOK {
					r.NotContains(cacheControl, "public",
						"%s: a shared cache must not be authorized to store this", endpoint.name)
				}
			}
		})
	}
}

// TestUnlockedPasswordPageIsStillNotSharedCacheable is the assertion most
// likely to be missed: holding a valid unlock cookie authorizes THIS visitor,
// not the CDN in front of them. The response is now a 200 carrying the real
// page, and it must still be `private, no-store`.
func TestUnlockedPasswordPageIsStillNotSharedCacheable(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, dbService, svc, org := passwordSetup(t)
	page := seedPageAtPublicSlug(ctx, t, dbService, svc, org.UID, models.StatusPageVisibilityPassword)

	// Positive control: an ordinary public page in the same org, served by the
	// same handler, IS publicly cacheable. If this stopped being true the
	// assertions below would be proving nothing.
	openPage, err := svc.CreateStatusPage(ctx, "acme", &CreateStatusPageRequest{
		Name: "Acme Open", Slug: "open",
	})
	r.NoError(err)

	unlock, err := svc.Unlock(ctx, "acme", testPublicSlug, "203.0.113.7", testPagePassword)
	r.NoError(err)

	h := NewHandler(svc, &config.Config{})

	for _, endpoint := range publicEndpoints() {
		req, rec := endpoint.request("acme", testPublicSlug)
		req.AddCookie(&http.Cookie{Name: statuspagelock.CookieName(page.UID), Value: unlock.Token})
		req = statuspagelock.WithRequestGrant(req)

		r.NoError(endpoint.call(h, rec, req), endpoint.name)
		r.Equal(http.StatusOK, rec.Code, "%s: the cookie must actually unlock the page", endpoint.name)
		r.Equal("private, no-store", rec.Header().Get("Cache-Control"), endpoint.name)
		r.NotContains(rec.Header().Get("Cache-Control"), "public", endpoint.name)

		controlReq, controlRec := endpoint.request("acme", openPage.Slug)
		controlReq = statuspagelock.WithRequestGrant(controlReq)

		r.NoError(endpoint.call(h, controlRec, controlReq), endpoint.name)
		r.Equal(http.StatusOK, controlRec.Code, endpoint.name)
		r.Equal("public, max-age=60", controlRec.Header().Get("Cache-Control"),
			"%s: positive control — an ordinary public page stays shared-cacheable", endpoint.name)
	}
}

// TestPublicResponsesPinTheVaryHeader nails the exact Vary value. It is the
// tripwire for the failure mode the spec names: someone starts deriving part
// of a public payload from a request header, does not extend Vary, and ships a
// shared cache that hands one visitor's variant to another.
func TestPublicResponsesPinTheVaryHeader(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, dbService, svc, org := passwordSetup(t)
	seedPageAtPublicSlug(ctx, t, dbService, svc, org.UID, models.StatusPageVisibilityPublic)

	h := NewHandler(svc, &config.Config{})

	for _, endpoint := range publicEndpoints() {
		req, rec := endpoint.request("acme", testPublicSlug)
		req = statuspagelock.WithRequestGrant(req)

		r.NoError(endpoint.call(h, rec, req), endpoint.name)
		r.Equal(http.StatusOK, rec.Code, endpoint.name)
		r.Equal("X-Forwarded-Proto", rec.Header().Get("Vary"), endpoint.name)
		r.NotContains(rec.Header().Get("Vary"), "Cookie",
			"%s: Vary: Cookie on a public page is what stops CDNs caching it at all", endpoint.name)
	}
}

// TestPrivatePage404Parity pins that a private page is still indistinguishable
// from a page that does not exist — same status, same body — and that neither
// answer may be stored by a shared cache, which would turn the difference in
// latency or the mere presence of a cached entry into an existence oracle.
func TestPrivatePage404Parity(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, dbService, svc, org := passwordSetup(t)
	seedPageAtPublicSlug(ctx, t, dbService, svc, org.UID, models.StatusPageVisibilityPrivate)

	h := NewHandler(svc, &config.Config{})

	for _, endpoint := range publicEndpoints() {
		hiddenReq, hiddenRec := endpoint.request("acme", testPublicSlug)
		hiddenReq = statuspagelock.WithRequestGrant(hiddenReq)
		r.NoError(endpoint.call(h, hiddenRec, hiddenReq), endpoint.name)

		missingReq, missingRec := endpoint.request("acme", "no-such-page")
		missingReq = statuspagelock.WithRequestGrant(missingReq)
		r.NoError(endpoint.call(h, missingRec, missingReq), endpoint.name)

		r.Equal(http.StatusNotFound, hiddenRec.Code, endpoint.name)
		r.Equal(missingRec.Code, hiddenRec.Code, endpoint.name)
		r.Equal(missingRec.Body.String(), hiddenRec.Body.String(),
			"%s: a private page must read exactly like a missing one", endpoint.name)
		r.Equal("private, no-store", hiddenRec.Header().Get("Cache-Control"), endpoint.name)
		r.Equal("private, no-store", missingRec.Header().Get("Cache-Control"), endpoint.name)
	}
}

// TestGatedDirectiveIsWhatTheHelperSays keeps the endpoint assertions above
// honest about WHERE the value comes from: they spell the strings out
// literally, so this pins that those literals are the helper's own answers. A
// change to the helper that the endpoint tests did not notice fails here.
func TestGatedDirectiveIsWhatTheHelperSays(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	r.Equal("private, no-store", statuspagecache.Gated)
	r.Equal("public, max-age=60",
		statuspagecache.Control(models.StatusPageVisibilityPublic, statuspagecache.PageMaxAge))
	r.Equal("X-Forwarded-Proto", statuspagecache.VaryPublic)
	r.Equal("Cookie, X-Forwarded-Proto", statuspagecache.VaryGated)
}
