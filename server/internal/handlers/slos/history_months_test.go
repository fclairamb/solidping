package slos_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/entitlements"
	"github.com/fclairamb/solidping/server/internal/handlers/slos"
	"github.com/fclairamb/solidping/server/internal/httpx"
)

// newSLOHistoryRouter wires both the create and the history route, so the
// months bound below can be exercised against a real objective rather than a
// 404 that would pass whatever the handler did with the parameter.
func newSLOHistoryRouter(t *testing.T) (*httpx.Router, *sqlite.Service, *models.Organization) {
	t.Helper()

	r := require.New(t)
	ctx := t.Context()

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	org := models.NewOrganization("acme", "acme")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	cfg := &config.Config{}
	entSvc := entitlements.NewService(dbSvc, entitlements.DefaultsFor(config.DeploymentModeSelfHosted), 0)
	handler := slos.NewHandler(slos.NewService(dbSvc, cfg, entSvc), cfg)

	router := httpx.New()
	router.POST("/api/v1/orgs/:org/slos", handler.Create)
	router.GET("/api/v1/orgs/:org/slos/:uid/history", handler.History)

	return router, dbSvc, org
}

// `months` drives an allocation of one window per month and used to have no
// upper bound at all. The ceiling is a 400, not a clamp — and the exact
// ceiling must still be accepted, or a future tightening silently breaks the
// dashboard.
func TestSLOHistoryMonthsCeiling(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	router, dbSvc, org := newSLOHistoryRouter(t)
	checkUID := seedCheckFor(t, dbSvc, org.UID, "history-api")

	rec := postSLO(t, router, map[string]any{"name": "API uptime", "checkUid": checkUID})
	r.Equal(http.StatusCreated, rec.Code, rec.Body.String())

	var created map[string]any
	r.NoError(json.Unmarshal(rec.Body.Bytes(), &created))

	uid, ok := created["uid"].(string)
	r.True(ok, "the create response must carry the objective uid: %s", rec.Body.String())

	history := func(months string) *httptest.ResponseRecorder {
		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet,
			fmt.Sprintf("/api/v1/orgs/acme/slos/%s/history?months=%s", uid, months), nil)
		out := httptest.NewRecorder()
		router.ServeHTTP(out, req)

		return out
	}

	// Positive controls: the dashboard's 12 and the exact ceiling both work.
	r.Equal(http.StatusOK, history("12").Code)

	atCeiling := history(strconv.Itoa(slos.MaxHistoryMonths))
	r.Equal(http.StatusOK, atCeiling.Code, atCeiling.Body.String())

	for _, over := range []string{
		strconv.Itoa(slos.MaxHistoryMonths + 1),
		"1000",
		"2147483647",
	} {
		refused := history(over)
		r.Equal(http.StatusBadRequest, refused.Code, "months=%s must be refused", over)
		r.Contains(refused.Body.String(), "VALIDATION_ERROR")
	}

	// The pre-existing lower bound is untouched.
	r.Equal(http.StatusBadRequest, history("0").Code)
	r.Equal(http.StatusBadRequest, history("-1").Code)
	r.Equal(http.StatusBadRequest, history("abc").Code)
}
