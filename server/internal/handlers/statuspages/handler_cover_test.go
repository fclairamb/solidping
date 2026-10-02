//nolint:lll // table-driven cases read better on one line
package statuspages

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/httpx"
)

func newCoverRouter(h *Handler) *httpx.Router {
	router := httpx.New()
	router.GET("/orgs/:org/status-pages", h.ListStatusPages)
	router.POST("/orgs/:org/status-pages", h.CreateStatusPage)
	router.GET("/orgs/:org/status-pages/:statusPageUid", h.GetStatusPage)
	router.PATCH("/orgs/:org/status-pages/:statusPageUid", h.UpdateStatusPage)
	router.DELETE("/orgs/:org/status-pages/:statusPageUid", h.DeleteStatusPage)
	router.POST("/orgs/:org/status-pages/:statusPageUid/verify", h.VerifyCustomDomain)
	router.GET("/allowed", h.CustomDomainAllowed)
	router.GET("/orgs/:org/status-pages/:statusPageUid/sections", h.ListSections)
	router.POST("/orgs/:org/status-pages/:statusPageUid/sections", h.CreateSection)
	router.POST("/orgs/:org/status-pages/:statusPageUid/sections/reorder", h.ReorderSections)
	router.GET("/orgs/:org/status-pages/:statusPageUid/sections/:sectionUid", h.GetSection)
	router.PATCH("/orgs/:org/status-pages/:statusPageUid/sections/:sectionUid", h.UpdateSection)
	router.DELETE("/orgs/:org/status-pages/:statusPageUid/sections/:sectionUid", h.DeleteSection)
	router.GET("/orgs/:org/status-pages/:statusPageUid/sections/:sectionUid/resources", h.ListResources)
	router.POST("/orgs/:org/status-pages/:statusPageUid/sections/:sectionUid/resources", h.CreateResource)
	router.POST("/orgs/:org/status-pages/:statusPageUid/sections/:sectionUid/resources/reorder", h.ReorderResources)
	router.PATCH("/orgs/:org/status-pages/:statusPageUid/sections/:sectionUid/resources/:resourceUid", h.UpdateResource)
	router.DELETE("/orgs/:org/status-pages/:statusPageUid/sections/:sectionUid/resources/:resourceUid", h.DeleteResource)
	router.GET("/public/:org/default", h.ViewDefaultStatusPage)
	router.GET("/public/:org/:slug", h.ViewStatusPage)
	router.GET("/public/:org/:slug/summary", h.ViewStatusPageSummary)
	router.POST("/public/:org/:slug/unlock", h.Unlock)
	router.POST("/public/:org/unlock", h.UnlockDefault)

	return router
}

type coverStep struct {
	name   string
	method string
	path   string
	body   string
	want   int
}

func runCoverSteps(t *testing.T, router *httpx.Router, steps []coverStep) {
	t.Helper()

	for _, step := range steps {
		req := httptest.NewRequestWithContext(t.Context(), step.method, step.path, strings.NewReader(step.body))
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		require.Equal(t, step.want, rec.Code, "%s: %s", step.name, rec.Body.String())
	}
}

func TestStatusPageHandlerAdminFlow(t *testing.T) {
	t.Parallel()

	ctx, svc, org := setupStatusPagesTest(t)
	h := NewHandler(svc, &config.Config{})
	router := newCoverRouter(h)

	check := models.NewCheck(org.UID, "cov-check", "http")
	require.NoError(t, svc.db.CreateCheck(ctx, check))

	base := "/orgs/acme/status-pages"
	page := base + "/cov-page"
	sec := page + "/sections/api"

	runCoverSteps(t, router, []coverStep{
		{"list ghost org", http.MethodGet, "/orgs/ghost/status-pages", "", http.StatusNotFound},
		{"create bad json", http.MethodPost, base, "{", http.StatusUnprocessableEntity},
		{
			"create unknown settings key", http.MethodPost, base,
			`{"name":"P","slug":"cov-bad","settings":{"nope":1}}`, http.StatusUnprocessableEntity,
		},
		{"create bad slug", http.MethodPost, base, `{"name":"P","slug":"A B"}`, http.StatusUnprocessableEntity},
		{
			"create bad visibility", http.MethodPost, base,
			`{"name":"P","slug":"cov-v","visibility":"weird"}`, http.StatusUnprocessableEntity,
		},
		{
			"create password required", http.MethodPost, base,
			`{"name":"P","slug":"cov-v","visibility":"password"}`, http.StatusUnprocessableEntity,
		},
		{
			"create bad history period", http.MethodPost, base,
			`{"name":"P","slug":"cov-v","historyPeriod":"7y"}`, http.StatusUnprocessableEntity,
		},
		{
			"create bad auto resolve", http.MethodPost, base,
			`{"name":"P","slug":"cov-v","autoResolve":"sometimes"}`, http.StatusUnprocessableEntity,
		},
		{
			"create bad custom domain", http.MethodPost, base,
			`{"name":"P","slug":"cov-v","customDomain":"not a domain"}`, http.StatusUnprocessableEntity,
		},
		{
			"create ghost org", http.MethodPost, "/orgs/ghost/status-pages",
			`{"name":"P","slug":"cov-v"}`, http.StatusNotFound,
		},
		{"create ok", http.MethodPost, base, `{"name":"Cover","slug":"cov-page"}`, http.StatusCreated},
		{"create dup slug", http.MethodPost, base, `{"name":"Cover","slug":"cov-page"}`, http.StatusUnprocessableEntity},
		{"list", http.MethodGet, base, "", http.StatusOK},
		{"get", http.MethodGet, page + "?with=sections", "", http.StatusOK},
		{"get missing", http.MethodGet, base + "/nope", "", http.StatusNotFound},
		{"get ghost org", http.MethodGet, "/orgs/ghost/status-pages/x", "", http.StatusNotFound},
		{"update bad json", http.MethodPatch, page, "{", http.StatusUnprocessableEntity},
		{"update unknown settings key", http.MethodPatch, page, `{"settings":{"nope":1}}`, http.StatusUnprocessableEntity},
		{"update bad visibility", http.MethodPatch, page, `{"visibility":"weird"}`, http.StatusUnprocessableEntity},
		{"update missing", http.MethodPatch, base + "/nope", `{"name":"x"}`, http.StatusNotFound},
		{"update bad slug", http.MethodPatch, page, `{"slug":"A B"}`, http.StatusUnprocessableEntity},
		{"update bad history", http.MethodPatch, page, `{"historyPeriod":"7y"}`, http.StatusUnprocessableEntity},
		{"update bad domain", http.MethodPatch, page, `{"customDomain":"not a domain"}`, http.StatusUnprocessableEntity},
		{"update ok", http.MethodPatch, page, `{"name":"Cover 2","description":"d"}`, http.StatusOK},
		{"update clear domain", http.MethodPatch, page, `{"customDomain":null}`, http.StatusOK},
		{"verify no domain", http.MethodPost, page + "/verify", "", http.StatusUnprocessableEntity},
		{"verify missing", http.MethodPost, base + "/nope/verify", "", http.StatusNotFound},
		{"domain allowed no", http.MethodGet, "/allowed?domain=status.acme.com", "", http.StatusNotFound},

		{"sections list", http.MethodGet, page + "/sections", "", http.StatusOK},
		{"sections list missing page", http.MethodGet, base + "/nope/sections", "", http.StatusNotFound},
		{"section create bad json", http.MethodPost, page + "/sections", "{", http.StatusUnprocessableEntity},
		{
			"section create bad slug", http.MethodPost, page + "/sections", `{"name":"A","slug":"A B"}`,
			http.StatusUnprocessableEntity,
		},
		{
			"section create missing page", http.MethodPost, base + "/nope/sections", `{"name":"A","slug":"api"}`,
			http.StatusNotFound,
		},
		{"section create ok", http.MethodPost, page + "/sections", `{"name":"API","slug":"api"}`, http.StatusCreated},
		{
			"section create dup", http.MethodPost, page + "/sections", `{"name":"API","slug":"api"}`,
			http.StatusUnprocessableEntity,
		},
		{
			"section create bad selector", http.MethodPost, page + "/sections",
			`{"name":"Dyn","slug":"dyn","selector":{"bogus":true}}`, http.StatusUnprocessableEntity,
		},
		{"section get", http.MethodGet, sec, "", http.StatusOK},
		{"section get missing", http.MethodGet, page + "/sections/nope", "", http.StatusNotFound},
		{"section update bad json", http.MethodPatch, sec, "{", http.StatusUnprocessableEntity},
		{"section update ok", http.MethodPatch, sec, `{"name":"API 2"}`, http.StatusOK},
		{"section update missing", http.MethodPatch, page + "/sections/nope", `{"name":"x"}`, http.StatusNotFound},
		{"section update bad slug", http.MethodPatch, sec, `{"slug":"A B"}`, http.StatusUnprocessableEntity},
		{"section update bad selector", http.MethodPatch, sec, `{"selector":{"bogus":true}}`, http.StatusUnprocessableEntity},

		{"resources list", http.MethodGet, sec + "/resources", "", http.StatusOK},
		{"resources list missing section", http.MethodGet, page + "/sections/nope/resources", "", http.StatusNotFound},
		{"resource create bad json", http.MethodPost, sec + "/resources", "{", http.StatusUnprocessableEntity},
		{"resource create no target", http.MethodPost, sec + "/resources", `{}`, http.StatusUnprocessableEntity},
		{"resource create missing check", http.MethodPost, sec + "/resources", `{"checkUid":"nope"}`, http.StatusNotFound},
		{"resource create missing group", http.MethodPost, sec + "/resources", `{"checkGroupUid":"nope"}`, http.StatusNotFound},
		{
			"resource create missing section", http.MethodPost, page + "/sections/nope/resources",
			`{"checkUid":"x"}`, http.StatusNotFound,
		},
		{"resource create ok", http.MethodPost, sec + "/resources", `{"checkUid":"` + check.UID + `"}`, http.StatusCreated},
		{"resource reorder bad json", http.MethodPost, sec + "/resources/reorder", "{", http.StatusUnprocessableEntity},
		{
			"resource reorder mismatch", http.MethodPost, sec + "/resources/reorder", `{"uids":["x"]}`,
			http.StatusUnprocessableEntity,
		},
		{"resource update bad json", http.MethodPatch, sec + "/resources/x", "{", http.StatusUnprocessableEntity},

		{"sections reorder bad json", http.MethodPost, page + "/sections/reorder", "{", http.StatusUnprocessableEntity},
		{
			"sections reorder mismatch", http.MethodPost, page + "/sections/reorder", `{"uids":["x"]}`,
			http.StatusUnprocessableEntity,
		},
		{
			"sections reorder missing page", http.MethodPost, base + "/nope/sections/reorder", `{"uids":[]}`,
			http.StatusNotFound,
		},

		{"view", http.MethodGet, "/public/acme/cov-page", "", http.StatusOK},
		{"view bad include", http.MethodGet, "/public/acme/cov-page?include=bogus", "", http.StatusBadRequest},
		{"view missing", http.MethodGet, "/public/acme/nope", "", http.StatusNotFound},
		{"view default missing", http.MethodGet, "/public/ghost/default", "", http.StatusNotFound},
		{"summary", http.MethodGet, "/public/acme/cov-page/summary", "", http.StatusOK},
		{"summary missing", http.MethodGet, "/public/acme/nope/summary", "", http.StatusNotFound},
		{"unlock bad json", http.MethodPost, "/public/acme/cov-page/unlock", "{", http.StatusUnprocessableEntity},
		{"unlock missing", http.MethodPost, "/public/acme/nope/unlock", `{"password":"x"}`, http.StatusNotFound},
		{"unlock default missing", http.MethodPost, "/public/ghost/unlock", `{"password":"x"}`, http.StatusNotFound},
	})

	// Resource lifecycle needs the generated resource UID.
	resources, err := svc.ListResources(ctx, "acme", "cov-page", "api")
	require.NoError(t, err)
	require.NotEmpty(t, resources)

	resPath := sec + "/resources/" + resources[0].UID
	runCoverSteps(t, router, []coverStep{
		{"resource update ok", http.MethodPatch, resPath, `{"publicName":"Public API"}`, http.StatusOK},
		{
			"resource reorder ok", http.MethodPost, sec + "/resources/reorder",
			`{"uids":["` + resources[0].UID + `"]}`, http.StatusNoContent,
		},
		{"resource delete ok", http.MethodDelete, resPath, "", http.StatusNoContent},
		{"sections reorder ok", http.MethodPost, page + "/sections/reorder", reorderBody(t, svc), http.StatusNoContent},
		{"section delete", http.MethodDelete, sec, "", http.StatusNoContent},
		{"section delete missing", http.MethodDelete, sec, "", http.StatusNotFound},
		{"page delete", http.MethodDelete, page, "", http.StatusNoContent},
		{"page delete missing", http.MethodDelete, page, "", http.StatusNotFound},
	})
}

func reorderBody(t *testing.T, svc *Service) string {
	t.Helper()

	sections, err := svc.ListSections(t.Context(), "acme", "cov-page")
	require.NoError(t, err)

	uids := make([]string, 0, len(sections))
	for _, s := range sections {
		uids = append(uids, s.UID)
	}

	raw, err := json.Marshal(map[string][]string{"uids": uids})
	require.NoError(t, err)

	return string(raw)
}

func TestStatusPageHandlerPasswordUnlock(t *testing.T) {
	t.Parallel()

	ctx, svc, org := setupStatusPagesTest(t)
	h := NewHandler(svc, &config.Config{})
	router := newCoverRouter(h)

	_, err := svc.CreateStatusPage(ctx, org.Slug, &CreateStatusPageRequest{
		Name: "Locked", Slug: "locked", Visibility: strPtr("password"), Password: strPtr("correct-horse"),
		IsDefault: boolPtr(true),
	})
	require.NoError(t, err)

	runCoverSteps(t, router, []coverStep{
		{"view locked", http.MethodGet, "/public/acme/locked", "", http.StatusUnauthorized},
		{"view default locked", http.MethodGet, "/public/acme/default", "", http.StatusUnauthorized},
		{"unlock wrong", http.MethodPost, "/public/acme/locked/unlock", `{"password":"nope-nope"}`, http.StatusUnauthorized},
		{"unlock ok", http.MethodPost, "/public/acme/locked/unlock", `{"password":"correct-horse"}`, http.StatusNoContent},
		{"unlock default ok", http.MethodPost, "/public/acme/unlock", `{"password":"correct-horse"}`, http.StatusNoContent},
	})
}
