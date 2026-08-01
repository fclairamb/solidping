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

// TestListChecksSortTargetHostParam exercises the sort=targetHost query param
// end-to-end through the handler: it is accepted and orders by the derived
// targetHost (none-of-host/url/target last), an unknown value is a 400
// VALIDATION_ERROR, and each response carries the computed targetHost field.
func TestListChecksSortTargetHostParam(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := t.Context()

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	org := models.NewOrganization("sort-th-h", "Sort TargetHost Handler Org")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	base0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	mkCheck := func(slug, checkType string, config models.JSONMap, offsetSec int) {
		c := models.NewCheck(org.UID, slug, checkType)
		c.Config = config
		c.CreatedAt = base0.Add(time.Duration(offsetSec) * time.Second)
		r.NoError(dbSvc.CreateCheck(ctx, c))
	}
	mkCheck("sth-b", "http", models.JSONMap{"url": "https://b.example.com/x"}, 100)
	mkCheck("sth-a", "tcp", models.JSONMap{"host": "a.example.com", "port": float64(22)}, 200)
	mkCheck("sth-none", "heartbeat", models.JSONMap{"token": "tok"}, 300)

	entSvc := entcore.NewService(dbSvc, entcore.DefaultsFor(config.DeploymentModeSelfHosted), 0)
	svc := checks.NewService(dbSvc, notifier.NewLocalEventNotifier(), disabledCreds(t), entSvc)
	handler := checks.NewHandler(svc, &config.Config{})

	router := httpx.New()
	group := router.NewGroup("/api/v1/orgs/:org/checks")
	group.GET("", handler.ListChecks)

	type checkOut struct {
		Slug       string  `json:"slug"`
		TargetHost *string `json:"targetHost"`
	}

	list := func(queryString string) (int, []checkOut, map[string]any) {
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
			Data []checkOut `json:"data"`
		}
		r.NoError(json.Unmarshal(rec.Body.Bytes(), &body))

		return rec.Code, body.Data, nil
	}

	// sort=targetHost → a.example.com, b.example.com, then none-of-host/url/
	// target last.
	code, data, _ := list("?sort=targetHost")
	r.Equal(http.StatusOK, code)
	r.Len(data, 3)
	r.Equal([]string{"sth-a", "sth-b", "sth-none"}, []string{data[0].Slug, data[1].Slug, data[2].Slug})

	r.NotNil(data[0].TargetHost)
	r.Equal("a.example.com", *data[0].TargetHost)
	r.NotNil(data[1].TargetHost)
	r.Equal("b.example.com", *data[1].TargetHost)
	r.Nil(data[2].TargetHost, "heartbeat check has no host/url/target field to derive targetHost from")

	// Unknown sort value → 400 VALIDATION_ERROR (not silently ignored).
	code, _, errBody := list("?sort=bogus")
	r.Equal(http.StatusBadRequest, code)
	r.Equal(string(base.ErrorCodeValidationError), errBody["code"])
}
