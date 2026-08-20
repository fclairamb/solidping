package slos_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/entitlements"
	"github.com/fclairamb/solidping/server/internal/handlers/slos"
	"github.com/fclairamb/solidping/server/internal/httpx"
)

func newSLORouter(t *testing.T, maxSlos *int) (*httpx.Router, *sqlite.Service, *models.Organization) {
	t.Helper()

	r := require.New(t)
	ctx := t.Context()

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	org := models.NewOrganization("acme", "acme")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	entSvc := entitlements.NewService(
		dbSvc, entitlements.DefaultsFor(config.DeploymentModeSelfHosted), 0,
	)

	if maxSlos != nil {
		r.NoError(entSvc.Set(ctx, org.UID, entitlements.Entitlements{
			Limits: entitlements.Limits{MaxSlos: maxSlos},
			Source: models.EntitlementSourceAdmin,
		}, "user:tester", ""))
	}

	cfg := &config.Config{}
	handler := slos.NewHandler(slos.NewService(dbSvc, cfg, entSvc), cfg)

	router := httpx.New()
	router.POST("/api/v1/orgs/:org/slos", handler.Create)

	return router, dbSvc, org
}

func seedCheckFor(t *testing.T, dbSvc *sqlite.Service, orgUID, slug string) string {
	t.Helper()

	check := models.NewCheck(orgUID, slug, "http")
	require.NoError(t, dbSvc.CreateCheck(t.Context(), check))

	return check.UID
}

func postSLO(t *testing.T, router *httpx.Router, body map[string]any) *httptest.ResponseRecorder {
	t.Helper()

	raw, err := json.Marshal(body)
	require.NoError(t, err)

	req := httptest.NewRequestWithContext(
		t.Context(), http.MethodPost, "/api/v1/orgs/acme/slos", bytes.NewReader(raw),
	)
	req.Header.Set("Content-Type", "application/json")

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	return rec
}

// TestCreateSLOReturns402AtCap is the HTTP half of the maxSlos entitlement:
// the service returns a QuotaError, and the handler must surface it as a real
// 402 with the QUOTA_EXCEEDED code and the limit context the dashboard renders
// its upgrade prompt from — not a 400 or a 500.
func TestCreateSLOReturns402AtCap(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	limit := 1
	router, dbSvc, org := newSLORouter(t, &limit)
	checkUID := seedCheckFor(t, dbSvc, org.UID, "api")

	// Positive control: the first objective is accepted, so the 402 below is
	// the cap and not a broken request.
	rec := postSLO(t, router, map[string]any{"name": "First", "checkUid": checkUID})
	r.Equal(http.StatusCreated, rec.Code, rec.Body.String())

	rec = postSLO(t, router, map[string]any{"name": "Second", "checkUid": checkUID})
	r.Equal(http.StatusPaymentRequired, rec.Code, rec.Body.String())

	var body map[string]any
	r.NoError(json.Unmarshal(rec.Body.Bytes(), &body))
	r.Equal("QUOTA_EXCEEDED", body["code"])
	r.Equal("MaxSlos", body["limitName"])
	r.InDelta(float64(1), body["limit"], 0.0001)
	r.InDelta(float64(1), body["currentUsage"], 0.0001)

	count, err := dbSvc.CountSLOs(t.Context(), org.UID)
	r.NoError(err)
	r.Equal(1, count, "the refused create must not have landed")
}

// The scope XOR is a validation error at the API boundary (422, the repo's
// WriteValidationError status), not a 500 surfacing the database constraint —
// a caller sending both or neither must get a usable, field-scoped message.
func TestCreateSLOScopeXorIsAValidationError(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	router, dbSvc, org := newSLORouter(t, nil)
	checkUID := seedCheckFor(t, dbSvc, org.UID, "api")

	group := models.NewCheckGroup(org.UID, "edge", "edge")
	r.NoError(dbSvc.CreateCheckGroup(t.Context(), group))

	rec := postSLO(t, router, map[string]any{"name": "Both", "checkUid": checkUID, "checkGroupUid": group.UID})
	r.Equal(http.StatusUnprocessableEntity, rec.Code, rec.Body.String())
	r.Contains(rec.Body.String(), "VALIDATION_ERROR")

	rec = postSLO(t, router, map[string]any{"name": "Neither"})
	r.Equal(http.StatusUnprocessableEntity, rec.Code, rec.Body.String())

	// Positive control: exactly one scope is accepted.
	rec = postSLO(t, router, map[string]any{"name": "Just the check", "checkUid": checkUID})
	r.Equal(http.StatusCreated, rec.Code, rec.Body.String())
}

// A duplicate slug is a 409, not a 500 from the unique index.
func TestCreateSLODuplicateSlugIsAConflict(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	router, dbSvc, org := newSLORouter(t, nil)
	checkUID := seedCheckFor(t, dbSvc, org.UID, "api")

	rec := postSLO(t, router, map[string]any{"name": "API availability", "checkUid": checkUID})
	r.Equal(http.StatusCreated, rec.Code, rec.Body.String())

	rec = postSLO(t, router, map[string]any{
		"name": "API availability again", "slug": "api-availability", "checkUid": checkUID,
	})
	r.Equal(http.StatusConflict, rec.Code, rec.Body.String())
}
