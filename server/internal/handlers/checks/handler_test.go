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
