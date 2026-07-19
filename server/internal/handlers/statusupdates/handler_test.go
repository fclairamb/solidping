package statusupdates_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/handlers/base"
	"github.com/fclairamb/solidping/server/internal/handlers/statusupdates"
	"github.com/fclairamb/solidping/server/internal/httpx"
)

// handlerSetup wraps testSetup with an HTTP handler and router.
type handlerSetup struct {
	*testSetup
	handler *statusupdates.Handler
	router  *httpx.Router
}

func newHandlerSetup(t *testing.T) *handlerSetup {
	t.Helper()

	ts := newTestSetup(t)
	handler := statusupdates.NewHandler(ts.svc, &config.Config{})

	router := httpx.New()
	group := router.NewGroup("/api/v1/orgs/:org/status-updates")
	group.GET("", handler.ListStatusUpdates)
	group.POST("", handler.CreateStatusUpdate)
	group.GET("/:uid", handler.GetStatusUpdate)
	group.PATCH("/:uid", handler.UpdateStatusUpdate)
	group.DELETE("/:uid", handler.DeleteStatusUpdate)

	return &handlerSetup{testSetup: ts, handler: handler, router: router}
}

func (h *handlerSetup) do(t *testing.T, method, path string, body interface{}) *httptest.ResponseRecorder {
	t.Helper()

	var bodyBytes []byte
	if body != nil {
		var err error
		bodyBytes, err = json.Marshal(body)
		require.NoError(t, err)
	}

	// Inject the test user into context so actorUID() returns a valid UID.
	ctx := context.WithValue(t.Context(), base.ContextKeyUser, h.user)
	req := httptest.NewRequestWithContext(ctx, method, path, bytes.NewBuffer(bodyBytes))
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	rec := httptest.NewRecorder()
	h.router.ServeHTTP(rec, req)

	return rec
}

// TestHandlerList checks that the list endpoint returns 200 with data key.
func TestHandlerList(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	h := newHandlerSetup(t)

	// Create one update first
	_, err := h.svc.CreateStatusUpdate(t.Context(), h.org.Slug, h.user.UID,
		makeCreate(h.pageID, "info", "Test notice", "Nothing to see"))
	r.NoError(err)

	rec := h.do(t, http.MethodGet,
		"/api/v1/orgs/"+h.org.Slug+"/status-updates?statusPage="+h.pageID, nil)
	r.Equal(http.StatusOK, rec.Code)

	var resp map[string]interface{}
	r.NoError(json.Unmarshal(rec.Body.Bytes(), &resp))
	r.Contains(resp, "data")
}

// TestHandlerCreate checks the create endpoint happy path.
func TestHandlerCreate(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	h := newHandlerSetup(t)

	body := map[string]interface{}{
		"statusPageUid": h.pageID,
		"kind":          "maintenance",
		"title":         "Scheduled maintenance",
		"bodyMarkdown":  "We will upgrade the database at midnight UTC.",
	}

	rec := h.do(t, http.MethodPost, "/api/v1/orgs/"+h.org.Slug+"/status-updates", body)
	r.Equal(http.StatusCreated, rec.Code)

	var resp map[string]interface{}
	r.NoError(json.Unmarshal(rec.Body.Bytes(), &resp))
	r.Equal("maintenance", resp["kind"])
	r.Equal("Scheduled maintenance", resp["title"])
}

// TestHandlerCreateInvalidKind checks validation error on bad kind.
func TestHandlerCreateInvalidKind(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	h := newHandlerSetup(t)

	body := map[string]interface{}{
		"statusPageUid": h.pageID,
		"kind":          "badkind",
		"title":         "Test",
		"bodyMarkdown":  "Test body",
	}

	rec := h.do(t, http.MethodPost, "/api/v1/orgs/"+h.org.Slug+"/status-updates", body)
	r.Equal(http.StatusUnprocessableEntity, rec.Code)
}

// TestHandlerCreateBadLinkURL checks that a non-http link URL is rejected.
func TestHandlerCreateBadLinkURL(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	h := newHandlerSetup(t)

	badURL := "ftp://not-valid.example.com"
	body := map[string]interface{}{
		"statusPageUid": h.pageID,
		"kind":          "info",
		"title":         "Test",
		"bodyMarkdown":  "Test body",
		"linkUrl":       badURL,
	}

	rec := h.do(t, http.MethodPost, "/api/v1/orgs/"+h.org.Slug+"/status-updates", body)
	r.Equal(http.StatusUnprocessableEntity, rec.Code)
}

// TestHandlerGetAndPatch checks get + patch round trip.
func TestHandlerGetAndPatch(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	h := newHandlerSetup(t)

	created, err := h.svc.CreateStatusUpdate(t.Context(), h.org.Slug, h.user.UID,
		makeCreate(h.pageID, "investigating", "Investigating", "We are looking"))
	r.NoError(err)

	// GET
	rec := h.do(t, http.MethodGet,
		"/api/v1/orgs/"+h.org.Slug+"/status-updates/"+created.UID, nil)
	r.Equal(http.StatusOK, rec.Code)

	// PATCH
	patch := map[string]interface{}{"title": "Resolved now"}
	rec = h.do(t, http.MethodPatch,
		"/api/v1/orgs/"+h.org.Slug+"/status-updates/"+created.UID, patch)
	r.Equal(http.StatusOK, rec.Code)

	var resp map[string]interface{}
	r.NoError(json.Unmarshal(rec.Body.Bytes(), &resp))
	r.Equal("Resolved now", resp["title"])
}

// TestHandlerDelete checks that delete returns 204.
func TestHandlerDelete(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	h := newHandlerSetup(t)

	created, err := h.svc.CreateStatusUpdate(t.Context(), h.org.Slug, h.user.UID,
		makeCreate(h.pageID, "resolved", "All good", "Services back"))
	r.NoError(err)

	rec := h.do(t, http.MethodDelete,
		"/api/v1/orgs/"+h.org.Slug+"/status-updates/"+created.UID, nil)
	r.Equal(http.StatusNoContent, rec.Code)

	// GET after delete → 404
	rec = h.do(t, http.MethodGet,
		"/api/v1/orgs/"+h.org.Slug+"/status-updates/"+created.UID, nil)
	r.Equal(http.StatusNotFound, rec.Code)
}

// TestHandlerCrossOrgGet verifies cross-org access returns 404 (not 403 to avoid info leak).
func TestHandlerCrossOrgGet(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	h := newHandlerSetup(t)

	// Create in org1
	created, err := h.svc.CreateStatusUpdate(t.Context(), h.org.Slug, h.user.UID,
		makeCreate(h.pageID, "info", "Title", "Body"))
	r.NoError(err)

	// Try to get from org2 (different slug → different org)
	rec := h.do(t, http.MethodGet,
		"/api/v1/orgs/"+h.org2.Slug+"/status-updates/"+created.UID, nil)
	r.Equal(http.StatusNotFound, rec.Code)
}
