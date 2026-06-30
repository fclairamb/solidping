package checks_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/uptrace/bunrouter"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	entcore "github.com/fclairamb/solidping/server/internal/entitlements"
	"github.com/fclairamb/solidping/server/internal/handlers/base"
	"github.com/fclairamb/solidping/server/internal/handlers/checks"
	"github.com/fclairamb/solidping/server/internal/notifier"
)

// TestCreateCheckHandlerReturns402OverCap verifies the HTTP layer translates a
// MaxChecks quota breach into 402 with code QUOTA_EXCEEDED and quota fields.
func TestCreateCheckHandlerReturns402OverCap(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := t.Context()

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	org := models.NewOrganization("quota-h", "Quota Handler Org")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	entSvc := entcore.NewService(dbSvc, entcore.DefaultsFor(config.DeploymentModeSelfHosted), 0)
	r.NoError(entSvc.Set(ctx, org.UID, entcore.Entitlements{
		Limits: entcore.Limits{MaxChecks: entcore.Int(1)},
		Source: models.EntitlementSourceAdmin,
	}, "user:test", ""))

	svc := checks.NewService(dbSvc, notifier.NewLocalEventNotifier(), disabledCreds(t), entSvc)
	handler := checks.NewHandler(svc, &config.Config{})

	router := bunrouter.New()
	router.NewGroup("/api/v1/orgs/:org/checks").POST("", handler.CreateCheck)

	post := func() *httptest.ResponseRecorder {
		body, marshalErr := json.Marshal(map[string]any{
			"type":   "http",
			"config": map[string]any{"url": "https://example.com"},
		})
		r.NoError(marshalErr)
		req := httptest.NewRequestWithContext(
			t.Context(), http.MethodPost, "/api/v1/orgs/"+org.Slug+"/checks", bytes.NewBuffer(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)

		return rec
	}

	// First create fits under the cap of 1.
	rec := post()
	r.Equal(http.StatusCreated, rec.Code, rec.Body.String())

	// Second create exceeds the cap → 402 Payment Required.
	rec = post()
	r.Equal(http.StatusPaymentRequired, rec.Code, rec.Body.String())

	var body map[string]any
	r.NoError(json.Unmarshal(rec.Body.Bytes(), &body))
	r.Equal(string(base.ErrorCodeQuotaExceeded), body["code"])
	r.Equal("MaxChecks", body["limitName"])
	r.InDelta(float64(1), body["limit"], 0.0001)
	r.InDelta(float64(1), body["currentUsage"], 0.0001)
}

// newCheckHandlerRouter builds a checks handler over a fresh in-memory db with
// a single org and POST/GET/PATCH routes wired. Returns the router and org slug.
func newCheckHandlerRouter(t *testing.T) (*bunrouter.Router, string) {
	t.Helper()
	r := require.New(t)
	ctx := t.Context()

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	org := models.NewOrganization("flap-h", "Flap Handler Org")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	entSvc := entcore.NewService(dbSvc, entcore.DefaultsFor(config.DeploymentModeSelfHosted), 0)
	svc := checks.NewService(dbSvc, notifier.NewLocalEventNotifier(), disabledCreds(t), entSvc)
	handler := checks.NewHandler(svc, &config.Config{})

	router := bunrouter.New()
	group := router.NewGroup("/api/v1/orgs/:org/checks")
	group.POST("", handler.CreateCheck)
	group.GET("/:checkUid", handler.GetCheck)
	group.PATCH("/:checkUid", handler.UpdateCheck)

	return router, org.Slug
}

// TestCreateCheckFlappingFieldsRoundTrip verifies the three flapping
// (adaptive-recovery) knobs are accepted on create and echoed back on GET.
func TestCreateCheckFlappingFieldsRoundTrip(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	router, orgSlug := newCheckHandlerRouter(t)

	body, err := json.Marshal(map[string]any{
		"type":                  "http",
		"config":                map[string]any{"url": "https://example.com"},
		"flappingWindowSeconds": 7200,
		"flapBackoffFactor":     3,
		"maxRecoveryMultiplier": 5,
	})
	r.NoError(err)

	req := httptest.NewRequestWithContext(
		t.Context(), http.MethodPost, "/api/v1/orgs/"+orgSlug+"/checks", bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	r.Equal(http.StatusCreated, rec.Code, rec.Body.String())

	var created map[string]any
	r.NoError(json.Unmarshal(rec.Body.Bytes(), &created))
	r.InDelta(float64(7200), created["flappingWindowSeconds"], 0.0001)
	r.InDelta(float64(3), created["flapBackoffFactor"], 0.0001)
	r.InDelta(float64(5), created["maxRecoveryMultiplier"], 0.0001)

	uid, _ := created["uid"].(string)
	r.NotEmpty(uid)

	getReq := httptest.NewRequestWithContext(
		t.Context(), http.MethodGet, "/api/v1/orgs/"+orgSlug+"/checks/"+uid, http.NoBody)
	getRec := httptest.NewRecorder()
	router.ServeHTTP(getRec, getReq)
	r.Equal(http.StatusOK, getRec.Code, getRec.Body.String())

	var fetched map[string]any
	r.NoError(json.Unmarshal(getRec.Body.Bytes(), &fetched))
	r.InDelta(float64(7200), fetched["flappingWindowSeconds"], 0.0001)
	r.InDelta(float64(3), fetched["flapBackoffFactor"], 0.0001)
	r.InDelta(float64(5), fetched["maxRecoveryMultiplier"], 0.0001)
}

// TestCreateCheckFlappingValidationRejectsBadFactor pins that an out-of-range
// flapBackoffFactor (< 1) is a 400 VALIDATION_ERROR, not a 500.
func TestCreateCheckFlappingValidationRejectsBadFactor(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	router, orgSlug := newCheckHandlerRouter(t)

	body, err := json.Marshal(map[string]any{
		"type":              "http",
		"config":            map[string]any{"url": "https://example.com"},
		"flapBackoffFactor": 0, // invalid: must be >= 1
	})
	r.NoError(err)

	req := httptest.NewRequestWithContext(
		t.Context(), http.MethodPost, "/api/v1/orgs/"+orgSlug+"/checks", bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	r.Equal(http.StatusBadRequest, rec.Code, rec.Body.String())

	var errBody map[string]any
	r.NoError(json.Unmarshal(rec.Body.Bytes(), &errBody))
	r.Equal(string(base.ErrorCodeValidationError), errBody["code"])
}
