package checks_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	entcore "github.com/fclairamb/solidping/server/internal/entitlements"
	"github.com/fclairamb/solidping/server/internal/handlers/base"
	"github.com/fclairamb/solidping/server/internal/handlers/checks"
	"github.com/fclairamb/solidping/server/internal/httpx"
	"github.com/fclairamb/solidping/server/internal/notifier"
)

// TestListChecksStatusFilterValidating exercises the ?status= query param
// end-to-end through the handler for the case this spec adds: "validating"
// as an accepted token, multi-status union filtering, and rejection of an
// unknown token. The org carries one check per status (up/down/validating/
// created) so an up check acts as the negative control throughout.
func TestListChecksStatusFilterValidating(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := t.Context()

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	org := models.NewOrganization("status-filter-h", "Status Filter Handler Org")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	mkCheck := func(slug string, status models.CheckStatus) {
		c := models.NewCheck(org.UID, slug, "http")
		c.Status = status
		r.NoError(dbSvc.CreateCheck(ctx, c))
	}
	mkCheck("sf-up", models.CheckStatusUp)
	mkCheck("sf-down", models.CheckStatusDown)
	mkCheck("sf-validating", models.CheckStatusValidating)
	mkCheck("sf-created", models.CheckStatusCreated)

	entSvc := entcore.NewService(dbSvc, entcore.DefaultsFor(config.DeploymentModeSelfHosted), 0)
	svc := checks.NewService(dbSvc, notifier.NewLocalEventNotifier(), disabledCreds(t), entSvc)
	handler := checks.NewHandler(svc, &config.Config{})

	router := httpx.New()
	group := router.NewGroup("/api/v1/orgs/:org/checks")
	group.GET("", handler.ListChecks)

	listSlugs := func(queryString string) (int, []string, map[string]any) {
		req := httptest.NewRequestWithContext(
			ctx, http.MethodGet, "/api/v1/orgs/"+org.Slug+"/checks"+queryString, http.NoBody)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			var errBody map[string]any
			_ = json.Unmarshal(rec.Body.Bytes(), &errBody)

			return rec.Code, nil, errBody
		}

		var body struct {
			Data []struct {
				Slug string `json:"slug"`
			} `json:"data"`
		}
		r.NoError(json.Unmarshal(rec.Body.Bytes(), &body))
		slugs := make([]string, len(body.Data))
		for i, d := range body.Data {
			slugs[i] = d.Slug
		}

		return rec.Code, slugs, nil
	}

	// ?status=validating → only the validating check, with the up check
	// present in the org as the negative control (absent from the result).
	code, slugs, _ := listSlugs("?status=validating")
	r.Equal(http.StatusOK, code)
	r.Equal([]string{"sf-validating"}, slugs)

	// ?status=down,validating → the union of both.
	code, slugs, _ = listSlugs("?status=down,validating")
	r.Equal(http.StatusOK, code)
	r.ElementsMatch([]string{"sf-down", "sf-validating"}, slugs)

	// Unknown token → 400 VALIDATION_ERROR, not silently ignored.
	code, _, errBody := listSlugs("?status=bogus")
	r.Equal(http.StatusBadRequest, code)
	r.Equal(string(base.ErrorCodeValidationError), errBody["code"])
}
