package statussubscribers_test

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/statuspagelock"
	"github.com/fclairamb/solidping/server/internal/utils/passwords"
)

// The Atom feed is the fourth public read of a status page, and its body is
// the most quotable one: it carries the update titles and bodies verbatim. It
// shipped `public, max-age=300` unconditionally, which authorized a shared
// cache to keep a password-protected page's incident narrative and hand it to
// anyone. It now follows the same visibility rule as the page itself
// (spec 2026-08-22-06).

// feedRequest fetches the feed, optionally presenting an unlock cookie.
func feedRequest(t *testing.T, h *handlerSetup, slug string, cookie *http.Cookie) *httptest.ResponseRecorder {
	t.Helper()

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet,
		"/api/v1/status-pages/"+h.org.Slug+"/"+slug+"/feed.xml", nil)
	if cookie != nil {
		req.AddCookie(cookie)
	}

	rec := httptest.NewRecorder()
	h.router.ServeHTTP(rec, req)

	return rec
}

func TestFeedCacheControlFollowsVisibility(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	h := newHandlerSetup(t)
	ctx := t.Context()

	// Positive control first, on the page as created: world-readable, and a
	// shared cache is welcome to hold it for the feed's own five minutes.
	rec := feedRequest(t, h, h.page.Slug, nil)
	r.Equal(http.StatusOK, rec.Code)
	r.Equal("public, max-age=300", rec.Header().Get("Cache-Control"))
	r.Equal("Cookie, X-Forwarded-Proto", rec.Header().Get("Vary"))

	// Same page, now behind a password.
	hash, err := passwords.Hash("correct-horse")
	r.NoError(err)

	visibility := models.StatusPageVisibilityPassword
	r.NoError(h.dbSvc.UpdateStatusPage(ctx, h.page.UID,
		&models.StatusPageUpdate{Visibility: &visibility, PasswordHash: &hash}))

	locked := feedRequest(t, h, h.page.Slug, nil)
	r.Equal(http.StatusUnauthorized, locked.Code)
	r.Equal("private, no-store", locked.Header().Get("Cache-Control"))
	r.NotContains(locked.Header().Get("Cache-Control"), "public")

	// And unlocked: a 200 with the real feed, still not storable by a shared
	// cache. The cookie authorizes this reader, not every reader behind the
	// same proxy.
	token := statuspagelock.Issue(hash, h.page.UID, time.Now(), statuspagelock.TTL)
	unlocked := feedRequest(t, h, h.page.Slug,
		&http.Cookie{Name: statuspagelock.CookieName(h.page.UID), Value: token})

	r.Equal(http.StatusOK, unlocked.Code, "the cookie must actually unlock the feed")
	r.Contains(unlocked.Body.String(), "<feed")
	r.Equal("private, no-store", unlocked.Header().Get("Cache-Control"))
	r.NotContains(unlocked.Header().Get("Cache-Control"), "public")

	// A feed for a page that does not exist must not be cacheable either.
	missing := feedRequest(t, h, "no-such-page", nil)
	r.Equal(http.StatusNotFound, missing.Code)
	r.Equal("private, no-store", missing.Header().Get("Cache-Control"))
}
