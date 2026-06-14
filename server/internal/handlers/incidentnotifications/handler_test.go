package incidentnotifications_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/uptrace/bunrouter"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/handlers/base"
	"github.com/fclairamb/solidping/server/internal/handlers/incidentnotifications"
)

// handlerHarness builds an org/check/incident/notification fixture and a
// bunrouter wired to the notification handlers. ServeHTTP resolves the path
// params exactly as in production.
type handlerHarness struct {
	router   *bunrouter.Router
	org      *models.Organization
	incident *models.Incident
	notif    *models.IncidentNotification
	conn     *models.Integration
}

func newHandlerHarness(t *testing.T) *handlerHarness {
	t.Helper()
	ctx := t.Context()
	r := require.New(t)

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	org := models.NewOrganization("notif-h", "Notif Handler Org")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	check := models.NewCheck(org.UID, "api", "http")
	r.NoError(dbSvc.CreateCheck(ctx, check))

	inc := models.NewIncident(org.UID, check.UID, time.Now().Add(-5*time.Minute), "api is down")
	r.NoError(dbSvc.CreateIncident(ctx, inc))

	conn := models.NewIntegration(org.UID, models.ConnectionTypeWebhook, "ops")
	conn.Enabled = true
	r.NoError(dbSvc.CreateChannel(ctx, conn))

	notif := models.NewIncidentNotificationForJob(
		org.UID, inc.UID, "incident.created", models.IncidentNotificationSourceCheckConnection,
		conn.UID, "job-xyz", "webhook", nil, nil,
	)
	failedAt := time.Now()
	notif.Status = models.IncidentNotificationStatusFailed
	notif.FailedAt = &failedAt
	errMsg := "webhook request failed: status 429"
	notif.Error = &errMsg
	r.NoError(dbSvc.CreateIncidentNotification(ctx, notif))

	svc := incidentnotifications.NewService(dbSvc)
	handler := incidentnotifications.NewHandler(svc, &config.Config{})

	router := bunrouter.New()
	router.NewGroup("/api/v1/orgs/:org/incidents").
		GET("/:uid/notifications/:notifUid", handler.GetForIncident)
	orgNotifs := router.NewGroup("/api/v1/orgs/:org/notifications")
	orgNotifs.GET("", handler.ListByOrg)
	orgNotifs.GET("/:notifUid", handler.GetByOrg)

	return &handlerHarness{router: router, org: org, incident: inc, notif: notif, conn: conn}
}

func (h *handlerHarness) get(t *testing.T, incidentUID, notifUID string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequestWithContext(
		t.Context(), http.MethodGet,
		"/api/v1/orgs/"+h.org.Slug+"/incidents/"+incidentUID+"/notifications/"+notifUID, nil)
	rec := httptest.NewRecorder()
	h.router.ServeHTTP(rec, req)

	return rec
}

// TestGetForIncidentHandlerFound returns 200 with the full detail, including
// the failedAt timestamp and untruncated error the list endpoint hides.
func TestGetForIncidentHandlerFound(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	h := newHandlerHarness(t)

	rec := h.get(t, h.incident.UID, h.notif.UID)
	r.Equal(http.StatusOK, rec.Code, rec.Body.String())

	var body map[string]any
	r.NoError(json.Unmarshal(rec.Body.Bytes(), &body))
	r.Equal(h.notif.UID, body["uid"])
	r.Equal("failed", body["status"])
	r.Equal("webhook request failed: status 429", body["error"])
	r.NotEmpty(body["failedAt"])
	// A failed notification was never sent or canceled.
	r.Nil(body["sentAt"])
	r.Nil(body["cancelledAt"])
}

// TestGetForIncidentHandlerNotFound maps an unknown notification UID to 404
// with code NOT_FOUND.
func TestGetForIncidentHandlerNotFound(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	h := newHandlerHarness(t)

	rec := h.get(t, h.incident.UID, "00000000-0000-0000-0000-000000000000")
	r.Equal(http.StatusNotFound, rec.Code, rec.Body.String())

	var body map[string]any
	r.NoError(json.Unmarshal(rec.Body.Bytes(), &body))
	r.Equal(string(base.ErrorCodeNotFound), body["code"])
}

// TestGetForIncidentHandlerUnknownIncident returns 404 for an unknown incident
// — never a server error, never a leak of the notification.
func TestGetForIncidentHandlerUnknownIncident(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	h := newHandlerHarness(t)

	rec := h.get(t, "no-such-incident", h.notif.UID)
	r.Equal(http.StatusNotFound, rec.Code, rec.Body.String())

	var body map[string]any
	r.NoError(json.Unmarshal(rec.Body.Bytes(), &body))
	r.Equal(string(base.ErrorCodeNotFound), body["code"])
}

// TestGetForIncidentHandlerWrongOrg returns 404 (not 403): a notification
// queried under an org it does not belong to must not leak its existence.
func TestGetForIncidentHandlerWrongOrg(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	h := newHandlerHarness(t)

	req := httptest.NewRequestWithContext(
		t.Context(), http.MethodGet,
		"/api/v1/orgs/unknown-org/incidents/"+h.incident.UID+"/notifications/"+h.notif.UID, nil)
	rec := httptest.NewRecorder()
	h.router.ServeHTTP(rec, req)

	r.Equal(http.StatusNotFound, rec.Code, rec.Body.String())
}

// ─── GetByOrg tests ───────────────────────────────────────────────────────────

func (h *handlerHarness) getByOrg(t *testing.T, notifUID string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequestWithContext(
		t.Context(), http.MethodGet,
		"/api/v1/orgs/"+h.org.Slug+"/notifications/"+notifUID, nil)
	rec := httptest.NewRecorder()
	h.router.ServeHTTP(rec, req)

	return rec
}

// TestGetByOrgFound returns 200 with the full detail for an existing notification.
func TestGetByOrgFound(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	h := newHandlerHarness(t)

	rec := h.getByOrg(t, h.notif.UID)
	r.Equal(http.StatusOK, rec.Code, rec.Body.String())

	var body map[string]any
	r.NoError(json.Unmarshal(rec.Body.Bytes(), &body))
	r.Equal(h.notif.UID, body["uid"])
	r.Equal("failed", body["status"])
	r.NotEmpty(body["failedAt"])
}

// TestGetByOrgNotFound returns 404 for an unknown notification UID.
func TestGetByOrgNotFound(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	h := newHandlerHarness(t)

	rec := h.getByOrg(t, "00000000-0000-0000-0000-000000000000")
	r.Equal(http.StatusNotFound, rec.Code, rec.Body.String())

	var body map[string]any
	r.NoError(json.Unmarshal(rec.Body.Bytes(), &body))
	r.Equal(string(base.ErrorCodeNotFound), body["code"])
}

// TestGetByOrgWrongOrg returns 404 for a notification queried under a different org.
func TestGetByOrgWrongOrg(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	h := newHandlerHarness(t)

	req := httptest.NewRequestWithContext(
		t.Context(), http.MethodGet,
		"/api/v1/orgs/unknown-org/notifications/"+h.notif.UID, nil)
	rec := httptest.NewRecorder()
	h.router.ServeHTTP(rec, req)

	r.Equal(http.StatusNotFound, rec.Code, rec.Body.String())
}

// ─── ListByOrg tests ──────────────────────────────────────────────────────────

func (h *handlerHarness) listByOrg(t *testing.T, query string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequestWithContext(
		t.Context(), http.MethodGet,
		"/api/v1/orgs/"+h.org.Slug+"/notifications"+query, nil)
	rec := httptest.NewRecorder()
	h.router.ServeHTTP(rec, req)

	return rec
}

// TestListByOrgRequiresConnectionUID returns 400 when connectionUid is absent.
func TestListByOrgRequiresConnectionUID(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	h := newHandlerHarness(t)

	rec := h.listByOrg(t, "")
	r.Equal(http.StatusBadRequest, rec.Code, rec.Body.String())

	var body map[string]any
	r.NoError(json.Unmarshal(rec.Body.Bytes(), &body))
	r.Equal(string(base.ErrorCodeValidationError), body["code"])
}

// TestListByOrgReturnsRows returns the notification that belongs to the connection.
func TestListByOrgReturnsRows(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	h := newHandlerHarness(t)

	rec := h.listByOrg(t, "?connectionUid="+h.conn.UID)
	r.Equal(http.StatusOK, rec.Code, rec.Body.String())

	var body map[string]any
	r.NoError(json.Unmarshal(rec.Body.Bytes(), &body))
	data, ok := body["data"].([]any)
	r.True(ok)
	r.Len(data, 1)
}

// TestListByOrgUnknownConnection returns an empty data array (not 404).
func TestListByOrgUnknownConnection(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	h := newHandlerHarness(t)

	rec := h.listByOrg(t, "?connectionUid=00000000-0000-0000-0000-000000000000")
	r.Equal(http.StatusOK, rec.Code, rec.Body.String())

	var body map[string]any
	r.NoError(json.Unmarshal(rec.Body.Bytes(), &body))
	data, ok := body["data"].([]any)
	r.True(ok)
	r.Empty(data)
}
