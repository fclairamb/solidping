package incidentpublications_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/handlers/incidentpublications"
	"github.com/fclairamb/solidping/server/internal/httpx"
	"github.com/fclairamb/solidping/server/internal/statuspagelock"
	"github.com/fclairamb/solidping/server/internal/utils/passwords"
)

// The public incident history is the sixth read of a status page, and the one
// with the most quotable body: PublicIncident.Title and Updates[].BodyMarkdown
// are the operator's own narrative, verbatim. It shipped with NO Cache-Control
// at all, which per RFC 9111 §4.2.2 leaves a shared cache free to store it
// heuristically — and the route sits on the custom-host allowlist, so the cache
// in question is the CDN in front of a customer's status domain
// (spec 2026-08-22-06).

// incidentsRouter mounts the public history endpoint the way the real server
// does: behind the middleware that installs the request's own unlock grant.
// Without it statuspagelock.Allows denies by default and a valid cookie would
// never be consulted.
func incidentsRouter(svc *incidentpublications.Service) *httpx.Router {
	handler := incidentpublications.NewHandler(svc, &config.Config{})
	router := httpx.New()
	router.GET("/api/v1/status-pages/:org/:slug/incidents",
		func(writer http.ResponseWriter, req *http.Request) error {
			return handler.ViewPublicIncidents(writer, statuspagelock.WithRequestGrant(req))
		})

	return router
}

func TestPublicIncidentsCacheControlFollowsVisibility(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	s := newPubSetup(t, setupOptions{autoPublish: true, delaySeconds: 0})
	router := incidentsRouter(s.pubs)

	get := func(slug string, cookie *http.Cookie) *httptest.ResponseRecorder {
		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet,
			"/api/v1/status-pages/"+s.org.Slug+"/"+slug+"/incidents", nil)
		if cookie != nil {
			req.AddCookie(cookie)
		}

		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)

		return rec
	}

	incidentCount := func(rec *httptest.ResponseRecorder) int {
		var body struct {
			Data []incidentpublications.PublicIncident `json:"data"`
		}

		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))

		return len(body.Data)
	}

	s.submit(models.ResultStatusDown)
	r.Len(s.publications(), 1)

	// Positive control: while the page is public the endpoint really does
	// return the narrative, and a shared cache is welcome to hold it. Without
	// this, every "must not be cacheable" assertion below could be satisfied by
	// an endpoint that returns nothing at all.
	open := get(s.page.Slug, nil)
	r.Equal(http.StatusOK, open.Code)
	r.Positive(incidentCount(open), "there must be something worth protecting")
	r.Equal("public, max-age=60", open.Header().Get("Cache-Control"))
	r.Equal("X-Forwarded-Proto", open.Header().Get("Vary"))

	// Same page, now behind a password.
	hash, err := passwords.Hash("correct-horse")
	r.NoError(err)

	visibility := models.StatusPageVisibilityPassword
	r.NoError(s.dbSvc.UpdateStatusPage(t.Context(), s.page.UID,
		&models.StatusPageUpdate{Visibility: &visibility, PasswordHash: &hash}))

	locked := get(s.page.Slug, nil)
	r.Equal(http.StatusUnauthorized, locked.Code)
	r.Equal("private, no-store", locked.Header().Get("Cache-Control"))
	r.NotContains(locked.Header().Get("Cache-Control"), "public")

	// The assertion that matters: unlocked, the caller gets a real 200 with the
	// full narrative — and it is still not storable by a shared cache. The
	// cookie authorizes this reader, not the proxy that serves everybody.
	token := statuspagelock.Issue(hash, s.page.UID, time.Now(), statuspagelock.TTL)
	unlocked := get(s.page.Slug,
		&http.Cookie{Name: statuspagelock.CookieName(s.page.UID), Value: token})

	r.Equal(http.StatusOK, unlocked.Code, "the cookie must actually unlock the history")
	r.Equal(incidentCount(open), incidentCount(unlocked), "same narrative as the public control")
	r.Equal("private, no-store", unlocked.Header().Get("Cache-Control"))
	r.NotContains(unlocked.Header().Get("Cache-Control"), "public")
	r.Equal("Cookie, X-Forwarded-Proto", unlocked.Header().Get("Vary"))

	// And the 404 for a page that does not exist is not cacheable either.
	missing := get("no-such-page", nil)
	r.Equal(http.StatusNotFound, missing.Code)
	r.Equal("private, no-store", missing.Header().Get("Cache-Control"))
}
