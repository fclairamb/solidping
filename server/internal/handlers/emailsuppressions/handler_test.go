package emailsuppressions_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/handlers/emailsuppressions"
	"github.com/fclairamb/solidping/server/internal/httpx"
)

func newTestRouter(t *testing.T, f *testFixture) *httpx.Router {
	t.Helper()

	svc := emailsuppressions.NewService(f.dbSvc)
	handler := emailsuppressions.NewHandler(svc, &config.Config{})

	router := httpx.New()
	group := router.NewGroup("/api/v1/orgs/:org/email-suppressions")
	group.GET("", handler.ListSuppressions)
	group.DELETE("/:uid", handler.DeleteSuppression)

	return router
}

func doRequest(t *testing.T, router *httpx.Router, method, path string) *httptest.ResponseRecorder {
	t.Helper()

	req := httptest.NewRequestWithContext(t.Context(), method, path, nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	return rec
}

func TestHandler_ListSuppressions_WrapsInDataKey(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	f := newTestFixture(t)
	router := newTestRouter(t, f)

	sup := models.NewEmailSuppression(f.org.UID, "alice@x.test", &f.check.UID, models.EmailSuppressionSourceLink)
	r.NoError(f.dbSvc.CreateEmailSuppression(t.Context(), sup))

	rec := doRequest(t, router, http.MethodGet, "/api/v1/orgs/"+f.org.Slug+"/email-suppressions")
	r.Equal(http.StatusOK, rec.Code)
	r.Contains(rec.Body.String(), `"data"`, "list responses must wrap in {\"data\": [...]}")
	r.Contains(rec.Body.String(), "alice@x.test")
}

func TestHandler_ListSuppressions_UnknownOrg404s(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	f := newTestFixture(t)
	router := newTestRouter(t, f)

	rec := doRequest(t, router, http.MethodGet, "/api/v1/orgs/does-not-exist/email-suppressions")
	r.Equal(http.StatusNotFound, rec.Code)
}

func TestHandler_DeleteSuppression(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	f := newTestFixture(t)
	router := newTestRouter(t, f)

	sup := models.NewEmailSuppression(f.org.UID, "delete-via-api@x.test", &f.check.UID, models.EmailSuppressionSourceLink)
	r.NoError(f.dbSvc.CreateEmailSuppression(t.Context(), sup))

	rec := doRequest(t, router, http.MethodDelete, "/api/v1/orgs/"+f.org.Slug+"/email-suppressions/"+sup.UID)
	r.Equal(http.StatusNoContent, rec.Code)

	listRec := doRequest(t, router, http.MethodGet, "/api/v1/orgs/"+f.org.Slug+"/email-suppressions")
	r.Equal(http.StatusOK, listRec.Code)
	r.NotContains(listRec.Body.String(), "delete-via-api@x.test")
}

func TestHandler_DeleteSuppression_UnknownUID404s(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	f := newTestFixture(t)
	router := newTestRouter(t, f)

	rec := doRequest(t, router, http.MethodDelete, "/api/v1/orgs/"+f.org.Slug+"/email-suppressions/does-not-exist")
	r.Equal(http.StatusNotFound, rec.Code)
}
