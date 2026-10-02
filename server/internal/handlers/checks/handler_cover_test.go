//nolint:lll // table-driven cases read better on one line
package checks_test

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
	entcore "github.com/fclairamb/solidping/server/internal/entitlements"
	"github.com/fclairamb/solidping/server/internal/handlers/checks"
	"github.com/fclairamb/solidping/server/internal/httpx"
	"github.com/fclairamb/solidping/server/internal/notifier"
)

func newFullChecksRouter(t *testing.T) *httpx.Router {
	t.Helper()
	r := require.New(t)
	ctx := t.Context()

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	org := models.NewOrganization("acme", "Acme")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	entSvc := entcore.NewService(dbSvc, entcore.DefaultsFor(config.DeploymentModeSelfHosted), 0)
	svc := checks.NewService(dbSvc, notifier.NewLocalEventNotifier(), disabledCreds(t), entSvc)
	h := checks.NewHandler(svc, &config.Config{})

	router := httpx.New()
	g := router.NewGroup("/orgs/:org/checks")
	g.GET("", h.ListChecks)
	g.POST("", h.CreateCheck)
	g.GET("/stats", h.GetCheckStats)
	g.POST("/validate", h.ValidateCheck)
	g.POST("/auto-placement", h.SwitchToAutoPlacement)
	g.GET("/export", h.ExportChecks)
	g.POST("/import", h.ImportChecks)
	g.POST("/apply", h.ApplyChecks)
	g.PUT("/by-slug/:slug", h.UpsertCheck)
	g.GET("/:checkUid", h.GetCheck)
	g.PATCH("/:checkUid", h.UpdateCheck)
	g.DELETE("/:checkUid", h.DeleteCheck)
	g.POST("/:checkUid/clone", h.CloneCheck)
	g.POST("/:checkUid/rotate-token", h.RotateHeartbeatToken)

	return router
}

func checksCall(t *testing.T, router *httpx.Router, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()

	req := httptest.NewRequestWithContext(t.Context(), method, path, strings.NewReader(body))
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	return rec
}

func TestChecksHandlerEndpoints(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	router := newFullChecksRouter(t)
	base := "/orgs/acme/checks"

	rec := checksCall(t, router, http.MethodPost, base,
		`{"name":"Site","slug":"site","type":"http","config":{"url":"https://example.com"}}`)
	r.Equal(http.StatusCreated, rec.Code, rec.Body.String())

	var created struct {
		UID string `json:"uid"`
	}
	r.NoError(json.Unmarshal(rec.Body.Bytes(), &created))

	hb := checksCall(t, router, http.MethodPost, base, `{"name":"Beat","slug":"beat","type":"heartbeat","config":{}}`)
	r.Equal(http.StatusCreated, hb.Code, hb.Body.String())

	steps := []struct {
		name, method, path, body string
		want                     int
	}{
		{"list", http.MethodGet, base, "", http.StatusOK},
		{"list filters", http.MethodGet, base +
			"?with=last_result,last_status_change&labels=env:prod,bad&limit=500&q=site&type=http,,tcp" +
			"&internal=false&status=up,down&sort=group&checkGroupUid=g1", "", http.StatusOK},
		{"list target host sort", http.MethodGet, base + "?sort=targetHost", "", http.StatusOK},
		{"list bad limit", http.MethodGet, base + "?limit=abc", "", http.StatusBadRequest},
		{"list zero limit", http.MethodGet, base + "?limit=0", "", http.StatusBadRequest},
		{"list bad status", http.MethodGet, base + "?status=bogus", "", http.StatusBadRequest},
		{"list bad sort", http.MethodGet, base + "?sort=bogus", "", http.StatusBadRequest},
		{"list bad cursor", http.MethodGet, base + "?cursor=!!!", "", http.StatusBadRequest},
		{"list ghost org", http.MethodGet, "/orgs/ghost/checks", "", http.StatusNotFound},
		{"stats", http.MethodGet, base + "/stats", "", http.StatusOK},
		{"stats ghost org", http.MethodGet, "/orgs/ghost/checks/stats", "", http.StatusNotFound},
		{"create bad json", http.MethodPost, base, "{", http.StatusUnprocessableEntity},
		{"create no config", http.MethodPost, base, `{"name":"x"}`, http.StatusUnprocessableEntity},
		{"create no type", http.MethodPost, base, `{"name":"x","config":{"foo":"bar"}}`, http.StatusUnprocessableEntity},
		{"create bad type", http.MethodPost, base, `{"name":"x","type":"nope","config":{"a":1}}`, http.StatusBadRequest},
		{
			"create bad config", http.MethodPost, base, `{"name":"x","type":"http","config":{"url":""}}`,
			http.StatusUnprocessableEntity,
		},
		{
			"create dup slug", http.MethodPost, base, `{"name":"x","slug":"site","type":"http","config":{"url":"https://a.io"}}`,
			http.StatusUnprocessableEntity,
		},
		{
			"create bad slug", http.MethodPost, base, `{"name":"x","slug":"A B","type":"http","config":{"url":"https://a.io"}}`,
			http.StatusUnprocessableEntity,
		},
		{
			"create internal", http.MethodPost, base,
			`{"name":"x","type":"http","internal":true,"config":{"url":"https://a.io"}}`, http.StatusUnprocessableEntity,
		},
		{
			"create inferred name", http.MethodPost, base, `{"config":{"url":"https://inferred.example.org"}}`,
			http.StatusCreated,
		},
		{"create ghost org", http.MethodPost, "/orgs/ghost/checks", `{"config":{"url":"https://a.io"}}`, http.StatusNotFound},
		{"get", http.MethodGet, base + "/site?with=last_result,last_status_change,region_freshness", "", http.StatusOK},
		{"get missing", http.MethodGet, base + "/nope", "", http.StatusNotFound},
		{"get ghost org", http.MethodGet, "/orgs/ghost/checks/x", "", http.StatusNotFound},
		{"update bad json", http.MethodPatch, base + "/site", "{", http.StatusUnprocessableEntity},
		{"update ok", http.MethodPatch, base + "/site", `{"name":"Site 2"}`, http.StatusOK},
		{"update missing", http.MethodPatch, base + "/nope", `{"name":"x"}`, http.StatusNotFound},
		{"update bad slug", http.MethodPatch, base + "/site", `{"slug":"A B"}`, http.StatusUnprocessableEntity},
		{"update dup slug", http.MethodPatch, base + "/site", `{"slug":"beat"}`, http.StatusUnprocessableEntity},
		{"update empty name", http.MethodPatch, base + "/site", `{"name":""}`, http.StatusUnprocessableEntity},
		{"update internal", http.MethodPatch, base + "/site", `{"internal":true}`, http.StatusUnprocessableEntity},
		{"update bad flapping", http.MethodPatch, base + "/site", `{"flappingWindowSeconds":-1}`, http.StatusBadRequest},
		{"update ghost org", http.MethodPatch, "/orgs/ghost/checks/x", `{"name":"x"}`, http.StatusNotFound},
		{"rotate not heartbeat", http.MethodPost, base + "/site/rotate-token", "", http.StatusBadRequest},
		{"rotate missing", http.MethodPost, base + "/nope/rotate-token", "", http.StatusNotFound},
		{"rotate ghost org", http.MethodPost, "/orgs/ghost/checks/x/rotate-token", "", http.StatusNotFound},
		{"rotate ok", http.MethodPost, base + "/beat/rotate-token", "", http.StatusOK},
		{"upsert bad json", http.MethodPut, base + "/by-slug/up-site", "{", http.StatusUnprocessableEntity},
		{"upsert no config", http.MethodPut, base + "/by-slug/up-site", `{}`, http.StatusUnprocessableEntity},
		{"upsert no type", http.MethodPut, base + "/by-slug/up-site", `{"config":{"foo":1}}`, http.StatusUnprocessableEntity},
		{"upsert bad type", http.MethodPut, base + "/by-slug/up-site", `{"type":"nope","config":{"a":1}}`, http.StatusUnprocessableEntity},
		{
			"upsert create", http.MethodPut, base + "/by-slug/up-site", `{"config":{"url":"https://up.example.org"}}`,
			http.StatusCreated,
		},
		{
			"upsert update", http.MethodPut, base + "/by-slug/up-site", `{"config":{"url":"https://up2.example.org"}}`,
			http.StatusOK,
		},
		{
			"upsert domain", http.MethodPut, base + "/by-slug/up-dom", `{"type":"domain","config":{"domain":"example.org"}}`,
			http.StatusCreated,
		},
		{
			"upsert ghost org", http.MethodPut, "/orgs/ghost/checks/by-slug/x", `{"config":{"url":"https://a.io"}}`,
			http.StatusNotFound,
		},
		{"clone bad json", http.MethodPost, base + "/site/clone", "{", http.StatusUnprocessableEntity},
		{"clone empty body", http.MethodPost, base + "/site/clone", "", http.StatusCreated},
		{"clone missing", http.MethodPost, base + "/nope/clone", "", http.StatusNotFound},
		{"clone ghost org", http.MethodPost, "/orgs/ghost/checks/x/clone", "", http.StatusNotFound},
		{"auto-placement", http.MethodPost, base + "/auto-placement", `{}`, http.StatusOK},
		{"auto-placement empty", http.MethodPost, base + "/auto-placement", "", http.StatusOK},
		{"auto-placement bad json", http.MethodPost, base + "/auto-placement", "{", http.StatusUnprocessableEntity},
		{"auto-placement ghost", http.MethodPost, "/orgs/ghost/checks/auto-placement", `{}`, http.StatusNotFound},
		{"export", http.MethodGet, base + "/export?type=x&labels=a:b,c&checkGroupUid=g", "", http.StatusOK},
		{"export ghost", http.MethodGet, "/orgs/ghost/checks/export", "", http.StatusNotFound},
		{"import bad doc", http.MethodPost, base + "/import", "{", http.StatusUnprocessableEntity},
		{
			"import dry run", http.MethodPost, base + "/import?dryRun=true",
			`{"version":2,"checks":[{"name":"Imp","slug":"imp","type":"http","config":{"url":"https://imp.example.org"}}]}`,
			http.StatusOK,
		},
		{"import ghost", http.MethodPost, "/orgs/ghost/checks/import", `{"version":2,"checks":[{"name":"G","slug":"g","type":"http","config":{"url":"https://g.example.org"}}]}`, http.StatusNotFound},
		{"apply bad doc", http.MethodPost, base + "/apply", "{", http.StatusUnprocessableEntity},
		{
			"apply dry run", http.MethodPost, base + "/apply?dryRun=true&prune=true&force=true&deletionCap=5",
			`{"version":2,"checks":[{"name":"App","slug":"app","type":"http","config":{"url":"https://app.example.org"}}]}`,
			http.StatusOK,
		},
		{"apply ghost", http.MethodPost, "/orgs/ghost/checks/apply", `{"version":2,"checks":[{"name":"G","slug":"g","type":"http","config":{"url":"https://g.example.org"}}]}`, http.StatusNotFound},
		{"validate bad json", http.MethodPost, base + "/validate", "{", http.StatusUnprocessableEntity},
		{
			"validate single", http.MethodPost, base + "/validate", `{"type":"http","config":{"url":"https://v.example.org"}}`,
			http.StatusOK,
		},
		{
			"validate document", http.MethodPost, base + "/validate",
			`{"version":2,"checks":[{"name":"V","slug":"v","type":"http","config":{"url":"https://v.example.org"}}]}`,
			http.StatusOK,
		},
		{
			"validate document plan needs admin", http.MethodPost, base + "/validate?plan=true",
			`{"version":2,"checks":[]}`, http.StatusForbidden,
		},
		{
			"validate document ghost", http.MethodPost, "/orgs/ghost/checks/validate", `{"version":2,"checks":[]}`,
			http.StatusNotFound,
		},
		{"delete", http.MethodDelete, base + "/site", "", http.StatusNoContent},
		{"delete again", http.MethodDelete, base + "/site", "", http.StatusNotFound},
		{"delete ghost", http.MethodDelete, "/orgs/ghost/checks/x", "", http.StatusNotFound},
	}

	for _, step := range steps {
		rec = checksCall(t, router, step.method, step.path, step.body)
		if rec.Code != step.want {
			t.Errorf("%s: want %d got %d: %s", step.name, step.want, rec.Code, rec.Body.String())
		}
	}
}
