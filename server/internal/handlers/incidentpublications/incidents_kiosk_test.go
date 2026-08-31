package incidentpublications_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/handlers/incidentpublications"
	"github.com/fclairamb/solidping/server/internal/httpx"
	"github.com/fclairamb/solidping/server/internal/statuspagekiosk"
	"github.com/fclairamb/solidping/server/internal/statuspagelock"
	"github.com/fclairamb/solidping/server/internal/utils/passwords"
)

// The TV board renders "days since last incident" and the last three resolved
// incidents from THIS endpoint, so a kiosk token that opened the page payload
// but not the incident history would leave the wallboard silently wrong about
// the very thing it exists to announce (spec 2026-08-29-08).

// kioskIncidentsRouter mounts the endpoint behind BOTH grants, the way the real
// public API surface does.
func kioskIncidentsRouter(svc *incidentpublications.Service) *httpx.Router {
	handler := incidentpublications.NewHandler(svc, &config.Config{})
	router := httpx.New()
	router.GET("/api/v1/status-pages/:org/:slug/incidents",
		func(writer http.ResponseWriter, req *http.Request) error {
			return handler.ViewPublicIncidents(
				writer, statuspagekiosk.WithRequestGrant(statuspagelock.WithRequestGrant(req)))
		})

	return router
}

func TestKioskTokenOpensThePublicIncidentHistory(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	s := newPubSetup(t, setupOptions{autoPublish: true, delaySeconds: 0})
	router := kioskIncidentsRouter(s.pubs)

	get := func(token string) *httptest.ResponseRecorder {
		target := "/api/v1/status-pages/" + s.org.Slug + "/" + s.page.Slug + "/incidents"
		if token != "" {
			target += "?kiosk=" + token
		}

		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, target, nil)
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

	// Positive control while the page is public: there is a narrative worth
	// protecting, so the negatives below cannot be satisfied by an empty body.
	open := get("")
	r.Equal(http.StatusOK, open.Code)
	r.Positive(incidentCount(open))

	token, hash, err := statuspagekiosk.Generate()
	r.NoError(err)

	passwordHash, err := passwords.Hash("correct-horse")
	r.NoError(err)

	visibility := models.StatusPageVisibilityPassword
	r.NoError(s.dbSvc.UpdateStatusPage(t.Context(), s.page.UID, &models.StatusPageUpdate{
		Visibility:     &visibility,
		PasswordHash:   &passwordHash,
		KioskTokenHash: &hash,
	}))

	locked := get("")
	r.Equal(http.StatusUnauthorized, locked.Code, "no token: still locked")

	wrong := get("not-the-token")
	r.Equal(locked.Code, wrong.Code, "a wrong token must answer exactly like no token")
	r.Equal(locked.Header().Get("Cache-Control"), wrong.Header().Get("Cache-Control"))

	unlocked := get(token)
	r.Equal(http.StatusOK, unlocked.Code, "the kiosk token must open the history")
	r.Equal(incidentCount(open), incidentCount(unlocked), "same narrative as the public control")
	r.Equal("private, no-store", unlocked.Header().Get("Cache-Control"),
		"a CDN must never store a kiosk-unlocked body")
	r.NotContains(unlocked.Header().Get("Cache-Control"), "public")

	// Private is the harder half: without the token the endpoint must not even
	// admit the page exists.
	private := models.StatusPageVisibilityPrivate
	r.NoError(s.dbSvc.UpdateStatusPage(t.Context(), s.page.UID, &models.StatusPageUpdate{
		Visibility: &private,
	}))

	hidden := get("")
	r.Equal(http.StatusNotFound, hidden.Code)
	r.Equal(hidden.Code, get("not-the-token").Code, "a wrong token is not an existence oracle")
	r.Equal(http.StatusOK, get(token).Code, "the token still opens it")

	// Revoking is immediate and, again, indistinguishable from never having had
	// a token.
	empty := ""
	r.NoError(s.dbSvc.UpdateStatusPage(t.Context(), s.page.UID, &models.StatusPageUpdate{
		KioskTokenHash: &empty,
	}))
	r.Equal(http.StatusNotFound, get(token).Code, "revoked reads exactly like absent")
}
