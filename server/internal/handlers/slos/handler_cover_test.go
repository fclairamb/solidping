//nolint:lll // table-driven cases read better on one line
package slos_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/entitlements"
	"github.com/fclairamb/solidping/server/internal/handlers/slos"
	"github.com/fclairamb/solidping/server/internal/httpx"
)

func newFullSLORouter(t *testing.T) (*httpx.Router, string) {
	t.Helper()

	r := require.New(t)
	ctx := t.Context()

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	org := models.NewOrganization("acme", "acme")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	check := models.NewCheck(org.UID, "api", "http")
	r.NoError(dbSvc.CreateCheck(ctx, check))

	entSvc := entitlements.NewService(dbSvc, entitlements.DefaultsFor(config.DeploymentModeSelfHosted), 0)
	cfg := &config.Config{}
	h := slos.NewHandler(slos.NewService(dbSvc, cfg, entSvc), cfg)

	router := httpx.New()
	router.GET("/orgs/:org/slos", h.List)
	router.POST("/orgs/:org/slos", h.Create)
	router.GET("/orgs/:org/slos/:uid", h.Get)
	router.PATCH("/orgs/:org/slos/:uid", h.Update)
	router.DELETE("/orgs/:org/slos/:uid", h.Delete)
	router.GET("/orgs/:org/slos/:uid/status", h.Status)
	router.GET("/orgs/:org/slos/:uid/burndown", h.Burndown)
	router.GET("/orgs/:org/slos/:uid/history", h.History)

	return router, check.UID
}

func sloCall(t *testing.T, router *httpx.Router, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()

	req := httptest.NewRequestWithContext(t.Context(), method, path, strings.NewReader(body))
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	return rec
}

func TestSLOHandlerLifecycle(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	router, checkUID := newFullSLORouter(t)

	rec := sloCall(t, router, http.MethodPost, "/orgs/acme/slos",
		`{"name":"API availability","slug":"api-avail","checkUid":"`+checkUID+`","targetPct":99.5,"timezone":"Europe/Paris"}`)
	r.Equal(http.StatusCreated, rec.Code, rec.Body.String())

	var created struct {
		UID string `json:"uid"`
	}
	r.NoError(json.Unmarshal(rec.Body.Bytes(), &created))
	r.NotEmpty(created.UID)

	base := "/orgs/acme/slos/" + created.UID

	steps := []struct {
		name, method, path, body string
		want                     int
	}{
		{"list", http.MethodGet, "/orgs/acme/slos", "", http.StatusOK},
		{"list by check", http.MethodGet, "/orgs/acme/slos?checkUid=" + checkUID + "&limit=10", "", http.StatusOK},
		{"list bad limit", http.MethodGet, "/orgs/acme/slos?limit=abc", "", http.StatusBadRequest},
		{"list zero limit", http.MethodGet, "/orgs/acme/slos?limit=0", "", http.StatusBadRequest},
		{"list big limit", http.MethodGet, "/orgs/acme/slos?limit=999", "", http.StatusBadRequest},
		{"list ghost org", http.MethodGet, "/orgs/ghost/slos", "", http.StatusNotFound},
		{"create bad json", http.MethodPost, "/orgs/acme/slos", "{", http.StatusUnprocessableEntity},
		{"create no name", http.MethodPost, "/orgs/acme/slos", `{"checkUid":"` + checkUID + `"}`, http.StatusUnprocessableEntity},
		{
			"create bad slug", http.MethodPost, "/orgs/acme/slos",
			`{"name":"x","slug":"A B","checkUid":"` + checkUID + `"}`, http.StatusUnprocessableEntity,
		},
		{
			"create dup slug", http.MethodPost, "/orgs/acme/slos",
			`{"name":"x","slug":"api-avail","checkUid":"` + checkUID + `"}`, http.StatusConflict,
		},
		{
			"create bad target", http.MethodPost, "/orgs/acme/slos",
			`{"name":"x","checkUid":"` + checkUID + `","targetPct":150}`, http.StatusUnprocessableEntity,
		},
		{
			"create bad tz", http.MethodPost, "/orgs/acme/slos",
			`{"name":"x","checkUid":"` + checkUID + `","timezone":"Mars/Base"}`, http.StatusUnprocessableEntity,
		},
		{
			"create unknown check", http.MethodPost, "/orgs/acme/slos", `{"name":"x","checkUid":"nope"}`,
			http.StatusUnprocessableEntity,
		},
		{"create ghost org", http.MethodPost, "/orgs/ghost/slos", `{"name":"x"}`, http.StatusNotFound},
		{"get", http.MethodGet, base, "", http.StatusOK},
		{"get missing", http.MethodGet, "/orgs/acme/slos/nope", "", http.StatusNotFound},
		{"update bad json", http.MethodPatch, base, "{", http.StatusUnprocessableEntity},
		{
			"update ok", http.MethodPatch, base, `{"name":"Renamed","targetPct":99.9,"enabled":true,"excludeMaintenance":true}`,
			http.StatusOK,
		},
		{"update slug", http.MethodPatch, base, `{"slug":"renamed"}`, http.StatusOK},
		{"update bad slug", http.MethodPatch, base, `{"slug":"A B"}`, http.StatusUnprocessableEntity},
		{"update bad target", http.MethodPatch, base, `{"targetPct":0}`, http.StatusUnprocessableEntity},
		{"update bad tz", http.MethodPatch, base, `{"timezone":"Mars/Base"}`, http.StatusUnprocessableEntity},
		{"update missing", http.MethodPatch, "/orgs/acme/slos/nope", `{"name":"x"}`, http.StatusNotFound},
		{"status", http.MethodGet, base + "/status", "", http.StatusOK},
		{"status missing", http.MethodGet, "/orgs/acme/slos/nope/status", "", http.StatusNotFound},
		{"burndown", http.MethodGet, base + "/burndown", "", http.StatusOK},
		{"burndown missing", http.MethodGet, "/orgs/acme/slos/nope/burndown", "", http.StatusNotFound},
		{"history", http.MethodGet, base + "/history", "", http.StatusOK},
		{"history months", http.MethodGet, base + "/history?months=3", "", http.StatusOK},
		{"history bad months", http.MethodGet, base + "/history?months=abc", "", http.StatusBadRequest},
		{"history too many months", http.MethodGet, base + "/history?months=37", "", http.StatusBadRequest},
		{"history missing", http.MethodGet, "/orgs/acme/slos/nope/history", "", http.StatusNotFound},
		{"delete", http.MethodDelete, base, "", http.StatusNoContent},
		{"delete again", http.MethodDelete, base, "", http.StatusNotFound},
	}

	for _, step := range steps {
		rec = sloCall(t, router, step.method, step.path, step.body)
		r.Equal(step.want, rec.Code, "%s: %s", step.name, rec.Body.String())
	}
}
