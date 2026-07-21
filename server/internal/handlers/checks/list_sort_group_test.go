package checks_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

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

// TestListChecksSortGroupParam exercises the sort=group query param end-to-end
// through the handler: it is accepted and reorders into display order, an
// unknown value is a 400 VALIDATION_ERROR, and omitting it keeps the default
// created_at-DESC ordering.
func TestListChecksSortGroupParam(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := t.Context()

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	org := models.NewOrganization("sort-group-h", "Sort Group Handler Org")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	// Groups A (sort_order 0) and B (sort_order 10); one ungrouped check.
	groupA := models.NewCheckGroup(org.UID, "grp-a", "grp-a")
	groupA.SortOrder = 0
	r.NoError(dbSvc.CreateCheckGroup(ctx, groupA))
	groupB := models.NewCheckGroup(org.UID, "grp-b", "grp-b")
	groupB.SortOrder = 10
	r.NoError(dbSvc.CreateCheckGroup(ctx, groupB))

	base0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	mkCheck := func(slug string, group *string, offsetSec int) {
		c := models.NewCheck(org.UID, slug, "http")
		c.CheckGroupUID = group
		c.CreatedAt = base0.Add(time.Duration(offsetSec) * time.Second)
		r.NoError(dbSvc.CreateCheck(ctx, c))
	}
	// created_at: ca(100) in A, cb(200) in B, cu(300) ungrouped (newest).
	mkCheck("sg-ca", &groupA.UID, 100)
	mkCheck("sg-cb", &groupB.UID, 200)
	mkCheck("sg-cu", nil, 300)

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

	// sort=group → display order: group A, group B, then ungrouped.
	code, slugs, _ := listSlugs("?sort=group")
	r.Equal(http.StatusOK, code)
	r.Equal([]string{"sg-ca", "sg-cb", "sg-cu"}, slugs)

	// Unknown sort value → 400 VALIDATION_ERROR (not silently ignored).
	code, _, errBody := listSlugs("?sort=bogus")
	r.Equal(http.StatusBadRequest, code)
	r.Equal(string(base.ErrorCodeValidationError), errBody["code"])

	// No sort param → default created_at DESC ordering, unchanged.
	code, slugs, _ = listSlugs("")
	r.Equal(http.StatusOK, code)
	r.Equal([]string{"sg-cu", "sg-cb", "sg-ca"}, slugs)
}
