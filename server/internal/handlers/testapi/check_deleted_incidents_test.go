package testapi

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/httpx"
)

// Both test-API delete endpoints call the DB layer directly. Spec 2026-10-08-02
// moved incident resolution into that layer, so neither may leave an active
// incident behind on the checks it deletes.

func seedCheckWithIncident(t *testing.T, handler *Handler, slug string) (*models.Organization, *models.Incident) {
	t.Helper()

	ctx := t.Context()
	r := require.New(t)

	org, err := handler.dbService.GetOrganizationBySlug(ctx, "test")
	r.NoError(err)

	check := models.NewCheck(org.UID, slug, "http")
	r.NoError(handler.dbService.CreateCheck(ctx, check))

	incident := models.NewIncident(org.UID, check.UID, time.Now().Add(-time.Minute), slug+" is down")
	r.NoError(handler.dbService.CreateIncident(ctx, incident))

	return org, incident
}

func requireCheckDeletedResolution(
	t *testing.T, handler *Handler, org *models.Organization, incident *models.Incident,
) {
	t.Helper()

	got, err := handler.dbService.GetIncident(t.Context(), org.UID, incident.UID)
	require.NoError(t, err)
	require.Equal(t, models.IncidentStateResolved, got.State)
	require.NotNil(t, got.ResolvedAt)
	require.NotNil(t, got.ResolutionType)
	require.Equal(t, models.ResolutionTypeCheckDeleted, *got.ResolutionType)
}

func TestDeleteAllChecksResolvesIncidents(t *testing.T) {
	t.Parallel()

	handler := setupBulkTestHandler(t)
	org, incident := seedCheckWithIncident(t, handler, "reset-me")

	req := httptest.NewRequestWithContext(t.Context(), http.MethodDelete, "/api/v1/test/checks/all?org=test", nil)
	w := httptest.NewRecorder()

	router := httpx.New()
	router.DELETE("/api/v1/test/checks/all", handler.DeleteAllChecks)
	router.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	requireCheckDeletedResolution(t, handler, org, incident)
}

func TestBulkDeleteChecksResolvesIncidents(t *testing.T) {
	t.Parallel()

	handler := setupBulkTestHandler(t)
	org, incident := seedCheckWithIncident(t, handler, "bulk-0")

	req := httptest.NewRequestWithContext(t.Context(), http.MethodDelete,
		"/api/v1/test/checks/bulk?slug=bulk-{nb}&count=1&org=test", nil)
	w := httptest.NewRecorder()

	router := httpx.New()
	router.DELETE("/api/v1/test/checks/bulk", handler.BulkDeleteChecks)
	router.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	requireCheckDeletedResolution(t, handler, org, incident)
}
