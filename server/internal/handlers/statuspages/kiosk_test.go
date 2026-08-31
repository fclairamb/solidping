package statuspages

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/statuspagekiosk"
	"github.com/fclairamb/solidping/server/internal/statuspagelock"
)

// --- Kiosk tokens (spec 2026-08-29-08) -------------------------------------
//
// The kiosk token is the only credential in the product that is meant to sit
// unattended on a screen in a room strangers walk through. Two properties are
// therefore load-bearing and get the bulk of the assertions here:
//
//  1. A VALID token really opens the two gated visibilities — otherwise the
//     feature does not exist.
//  2. An INVALID, REVOKED or REGENERATED-AWAY token is INDISTINGUISHABLE from
//     presenting nothing at all. Not "also denied" — identical. A distinct
//     answer would turn `?kiosk=` into an oracle confirming that a `private`
//     page exists, which is the one thing `private` is bought to prevent.
//
// Every negative below is paired with a positive control, so a fixture that
// silently stopped producing pages could not make the file pass.

// kioskCtx simulates a request that arrived carrying `?kiosk=<token>`. It
// installs the REAL grant (statuspagekiosk.FromRequest) rather than a stub, so
// the token→hash comparison under test is the production one.
func kioskCtx(ctx context.Context, token string) context.Context {
	req := httptest.NewRequestWithContext(
		ctx, http.MethodGet, "/api/v1/status-pages/acme/"+testPublicSlug+"?kiosk="+token, nil)

	return statuspagekiosk.WithGrant(ctx, statuspagekiosk.FromRequest(req))
}

// noKioskCtx is the reference behavior every negative case must match: a
// request that carried no `kiosk` parameter at all.
func noKioskCtx(ctx context.Context) context.Context {
	req := httptest.NewRequestWithContext(
		ctx, http.MethodGet, "/api/v1/status-pages/acme/"+testPublicSlug, nil)

	return statuspagekiosk.WithGrant(ctx, statuspagekiosk.FromRequest(req))
}

// mintKioskToken mints through the real service call, so the tests exercise
// the same generate → hash → store path the dashboard drives.
func mintKioskToken(ctx context.Context, t *testing.T, svc *Service) string {
	t.Helper()

	result, err := svc.GenerateKioskToken(ctx, "acme", testPublicSlug)
	require.NoError(t, err)
	require.NotEmpty(t, result.Token)
	require.True(t, result.HasKioskToken)

	return result.Token
}

// TestKioskTokenOpensAPasswordPage — a wallboard must not need somebody to
// re-type the password every morning, which is the whole reason the 12 h
// unlock cookie is not enough.
func TestKioskTokenOpensAPasswordPage(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	ctx, _, svc, _ := passwordSetup(t)
	createProtectedPage(ctx, t, svc)

	// Negative control first: without a token, locked.
	_, err := svc.ViewStatusPage(noKioskCtx(ctx), "acme", testPublicSlug)
	r.ErrorIs(err, statuspagelock.ErrLocked)

	token := mintKioskToken(ctx, t, svc)

	view, err := svc.ViewStatusPage(kioskCtx(ctx, token), "acme", testPublicSlug)
	r.NoError(err)
	r.Equal(testPublicSlug, view.Slug)
}

// TestKioskTokenOpensAPrivatePage — the token is what turns `private` into
// "unlisted for this one screen". Without it a private page 404s everywhere,
// which no TV can work around.
func TestKioskTokenOpensAPrivatePage(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	ctx, dbService, svc, org := passwordSetup(t)
	seedPageAtPublicSlug(ctx, t, dbService, svc, org.UID, models.StatusPageVisibilityPrivate)

	_, err := svc.ViewStatusPage(noKioskCtx(ctx), "acme", testPublicSlug)
	r.ErrorIs(err, ErrStatusPageNotFound, "negative control: private 404s without a token")

	token := mintKioskToken(ctx, t, svc)

	view, err := svc.ViewStatusPage(kioskCtx(ctx, token), "acme", testPublicSlug)
	r.NoError(err)
	r.Equal(testPublicSlug, view.Slug)

	// The JSON surfaces the board actually polls open too — a token that let
	// the page render but not its incident history would leave the TV blank
	// where it matters most.
	_, err = svc.ViewStatusPageSummary(kioskCtx(ctx, token), "acme", testPublicSlug)
	r.NoError(err, "summary")

	_, err = svc.ViewDefaultStatusPage(kioskCtx(ctx, token), "acme")
	r.NoError(err, "default page view")
}

// TestBadKioskTokenIsIndistinguishableFromNone is the no-oracle property,
// asserted the only way that means anything: the outcome with a bad token is
// compared against the outcome with NO token, for every shape of "bad" and
// both gated visibilities. A test that merely asserted "still denied" would
// pass even if the code answered 401 for a private page — which would already
// be the leak.
func TestBadKioskTokenIsIndistinguishableFromNone(t *testing.T) {
	t.Parallel()

	visibilities := []struct {
		name       string
		visibility string
		wantErr    error
	}{
		{"password page stays locked", models.StatusPageVisibilityPassword, statuspagelock.ErrLocked},
		{"private page stays missing", models.StatusPageVisibilityPrivate, ErrStatusPageNotFound},
	}

	for _, visibility := range visibilities {
		t.Run(visibility.name, func(t *testing.T) {
			t.Parallel()

			r := require.New(t)
			ctx, dbService, svc, org := passwordSetup(t)
			seedPageAtPublicSlug(ctx, t, dbService, svc, org.UID, visibility.visibility)

			// A real token exists on the page — so a wrong guess is being
			// compared against a live secret, not against an empty column.
			good := mintKioskToken(ctx, t, svc)

			_, baseline := svc.ViewStatusPage(noKioskCtx(ctx), "acme", testPublicSlug)
			r.ErrorIs(baseline, visibility.wantErr, "baseline: no token at all")

			bad := []struct {
				name  string
				token string
			}{
				{"empty value", ""},
				{"random string", "not-the-token"},
				{"the right length, wrong bytes", strings.Repeat("A", len(good))},
				{"the token's own hash", statuspagekiosk.Hash(good)},
				{"the token with one character removed", good[:len(good)-1]},
			}

			for _, attempt := range bad {
				_, err := svc.ViewStatusPage(kioskCtx(ctx, attempt.token), "acme", testPublicSlug)
				r.ErrorIs(err, visibility.wantErr, attempt.name)

				_, summaryErr := svc.ViewStatusPageSummary(kioskCtx(ctx, attempt.token), "acme", testPublicSlug)
				r.ErrorIs(summaryErr, visibility.wantErr, attempt.name+" (summary)")
			}

			// Positive control: the real token still works, so the loop above
			// was rejecting tokens rather than a broken page.
			_, err := svc.ViewStatusPage(kioskCtx(ctx, good), "acme", testPublicSlug)
			r.NoError(err)
		})
	}
}

// TestRegeneratingInvalidatesTheOldToken — "regenerate" has to mean the screen
// with the old URL goes dark immediately, or it is not a revocation control.
func TestRegeneratingInvalidatesTheOldToken(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	ctx, dbService, svc, org := passwordSetup(t)
	seedPageAtPublicSlug(ctx, t, dbService, svc, org.UID, models.StatusPageVisibilityPrivate)

	first := mintKioskToken(ctx, t, svc)

	_, err := svc.ViewStatusPage(kioskCtx(ctx, first), "acme", testPublicSlug)
	r.NoError(err, "positive control before the rotation")

	second := mintKioskToken(ctx, t, svc)
	r.NotEqual(first, second, "each mint must produce fresh entropy")

	_, err = svc.ViewStatusPage(kioskCtx(ctx, first), "acme", testPublicSlug)
	r.ErrorIs(err, ErrStatusPageNotFound, "the old token must stop working, with no distinct answer")

	_, err = svc.ViewStatusPage(kioskCtx(ctx, second), "acme", testPublicSlug)
	r.NoError(err, "the new token works")
}

// TestRevokeClearsTheToken covers the delete half, including its idempotence:
// an operator hitting revoke twice must not see an error for a state that is
// exactly what they asked for.
func TestRevokeClearsTheToken(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	ctx, dbService, svc, org := passwordSetup(t)
	seedPageAtPublicSlug(ctx, t, dbService, svc, org.UID, models.StatusPageVisibilityPassword)

	token := mintKioskToken(ctx, t, svc)

	_, err := svc.ViewStatusPage(kioskCtx(ctx, token), "acme", testPublicSlug)
	r.NoError(err, "positive control before the revoke")

	r.NoError(svc.RevokeKioskToken(ctx, "acme", testPublicSlug))

	_, err = svc.ViewStatusPage(kioskCtx(ctx, token), "acme", testPublicSlug)
	r.ErrorIs(err, statuspagelock.ErrLocked, "revoked reads exactly like no token")

	r.NoError(svc.RevokeKioskToken(ctx, "acme", testPublicSlug), "revoke is idempotent")

	// The stored column really is empty, not an empty-string hash that would
	// read back as "this page has a token".
	page, err := dbService.GetStatusPageBySlug(ctx, org.UID, testPublicSlug)
	r.NoError(err)
	r.True(page.KioskTokenHash == nil || *page.KioskTokenHash == "")
	r.False(statuspagekiosk.Holds(page, ""), "an empty token must never open a page")
}

// TestDisabledPageStaysHiddenFromKiosk — disabling a page is an operator
// saying "stop serving this", and a wallboard in a public lobby is precisely
// the audience that must stop seeing it. The token does not outrank that.
func TestDisabledPageStaysHiddenFromKiosk(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	ctx, dbService, svc, org := passwordSetup(t)
	seedPageAtPublicSlug(ctx, t, dbService, svc, org.UID, models.StatusPageVisibilityPublic)

	token := mintKioskToken(ctx, t, svc)

	_, err := svc.ViewStatusPage(kioskCtx(ctx, token), "acme", testPublicSlug)
	r.NoError(err, "positive control while enabled")

	page, err := dbService.GetStatusPageBySlug(ctx, org.UID, testPublicSlug)
	r.NoError(err)

	disabled := false
	r.NoError(dbService.UpdateStatusPage(ctx, page.UID, &models.StatusPageUpdate{Enabled: &disabled}))

	_, err = svc.ViewStatusPage(kioskCtx(ctx, token), "acme", testPublicSlug)
	r.ErrorIs(err, ErrStatusPageNotFound)
}

// TestKioskTokenIsScopedToOnePage — a token minted for one page must not open
// its neighbor, or "per-page revocation" would be a fiction.
func TestKioskTokenIsScopedToOnePage(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	ctx, dbService, svc, org := passwordSetup(t)
	seedPageAtPublicSlug(ctx, t, dbService, svc, org.UID, models.StatusPageVisibilityPrivate)

	private := models.StatusPageVisibilityPrivate
	_, err := svc.CreateStatusPage(ctx, "acme", &CreateStatusPageRequest{
		Name: "Other", Slug: "other-page", Visibility: &private,
	})
	r.NoError(err)

	token := mintKioskToken(ctx, t, svc)

	req := httptest.NewRequestWithContext(
		ctx, http.MethodGet, "/api/v1/status-pages/acme/other-page?kiosk="+token, nil)
	otherCtx := statuspagekiosk.WithGrant(ctx, statuspagekiosk.FromRequest(req))

	_, err = svc.ViewStatusPage(otherCtx, "acme", "other-page")
	r.ErrorIs(err, ErrStatusPageNotFound, "one page's token must not open another")

	_, err = svc.ViewStatusPage(kioskCtx(ctx, token), "acme", testPublicSlug)
	r.NoError(err, "positive control: it does open its own page")

	_ = dbService
}

// TestKioskTokenIsNeverSerializedPublicly — the hash must not travel, and even
// the mere FACT that a wallboard token exists is operator information: telling
// an anonymous reader there is a second way in is an invitation.
func TestKioskTokenIsNeverSerializedPublicly(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	ctx, dbService, svc, org := passwordSetup(t)
	seedPageAtPublicSlug(ctx, t, dbService, svc, org.UID, models.StatusPageVisibilityPublic)

	mintKioskToken(ctx, t, svc)

	admin, err := svc.GetStatusPage(ctx, "acme", testPublicSlug, GetStatusPageOptions{})
	r.NoError(err)
	r.True(admin.HasKioskToken, "the operator console needs to know a token exists")

	public, err := svc.ViewStatusPage(noKioskCtx(ctx), "acme", testPublicSlug)
	r.NoError(err)
	r.False(public.HasKioskToken, "the public payload must not advertise it")

	_ = dbService
	_ = org
}

// TestKioskResponsesAreNeverSharedCacheable — a CDN holding a kiosk-unlocked
// body would re-serve a gated page to visitors who presented nothing. The
// directive is keyed on the page, never on how this caller got in, so it must
// match the locked answer exactly.
func TestKioskResponsesAreNeverSharedCacheable(t *testing.T) {
	t.Parallel()

	visibilities := []string{
		models.StatusPageVisibilityPassword,
		models.StatusPageVisibilityPrivate,
	}

	for _, visibility := range visibilities {
		t.Run(visibility, func(t *testing.T) {
			t.Parallel()

			r := require.New(t)
			ctx, dbService, svc, org := passwordSetup(t)
			seedPageAtPublicSlug(ctx, t, dbService, svc, org.UID, visibility)

			token := mintKioskToken(ctx, t, svc)
			h := NewHandler(svc, &config.Config{})

			req, rec := newKioskPageViewRequest("acme", testPublicSlug, token)
			r.NoError(h.ViewStatusPage(rec, req))
			r.Equal(http.StatusOK, rec.Code, "the token must actually open the page")

			cacheControl := rec.Header().Get("Cache-Control")
			r.Equal(statuspagecacheGated, cacheControl)
			r.NotContains(cacheControl, "public",
				"a shared cache must not be authorized to store a kiosk-unlocked body")
		})
	}
}

// statuspagecacheGated is the expected directive, spelled out rather than
// imported, so a change to the constant has to be re-affirmed here.
const statuspagecacheGated = "private, no-store"

// newKioskPageViewRequest is newPageViewRequest with a kiosk token and the two
// grants the real router mounts.
func newKioskPageViewRequest(
	orgSlug, slug, token string,
) (*http.Request, *httptest.ResponseRecorder) {
	req := httptest.NewRequestWithContext(
		context.Background(), http.MethodGet,
		"/api/v1/status-pages/"+orgSlug+"/"+slug+"?kiosk="+token, nil)

	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("org", orgSlug)
	rctx.URLParams.Add("slug", slug)
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	req = statuspagelock.WithRequestGrant(req)
	req = statuspagekiosk.WithRequestGrant(req)

	return req, httptest.NewRecorder()
}

// TestNoGrantMeansNoAccess pins the deny-by-default: a caller that never went
// through the middleware — an MCP tool, a background job — holds no grant, and
// that must read as "no token" rather than as "allowed".
func TestNoGrantMeansNoAccess(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	ctx, dbService, svc, org := passwordSetup(t)
	seedPageAtPublicSlug(ctx, t, dbService, svc, org.UID, models.StatusPageVisibilityPrivate)
	mintKioskToken(ctx, t, svc)

	// Bare context: no kiosk grant installed at all.
	_, err := svc.ViewStatusPage(ctx, "acme", testPublicSlug)
	r.ErrorIs(err, ErrStatusPageNotFound)
}
