package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/uptrace/bunrouter"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/utils/timeutils"
)

func seedCostJob(
	ctx context.Context, r *require.Assertions, dbSvc *sqlite.Service,
	orgUID, slug string, cost, delay float64,
) {
	check := models.NewCheck(orgUID, slug, "sleep")
	check.Enabled = false
	r.NoError(dbSvc.CreateCheck(ctx, check))

	cj := models.NewCheckJob(orgUID, check.UID, timeutils.Duration(time.Minute))
	cj.Type = "sleep"
	cj.CostEWMAMs = cost
	cj.DelayEWMAMs = delay
	r.NoError(dbSvc.CreateCheckJob(ctx, cj))
}

func TestGetCostDistribution_Handler(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	r := require.New(t)

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	org := models.NewOrganization("cdhorg", "CDH Org")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	seedCostJob(ctx, r, dbSvc, org.UID, "job-fast", 200, 0)
	seedCostJob(ctx, r, dbSvc, org.UID, "job-mid", 900, 10)
	seedCostJob(ctx, r, dbSvc, org.UID, "job-slow", 8000, 500)

	srv := &Server{dbService: dbSvc}

	// newReq builds a bunrouter request for the endpoint with the given raw
	// query string, threading the test context.
	newReq := func(rawQuery string) bunrouter.Request {
		url := "/api/mgmt/scheduling/cost-distribution"
		if rawQuery != "" {
			url += "?" + rawQuery
		}

		req := httptest.NewRequestWithContext(ctx, http.MethodGet, url, http.NoBody)

		return bunrouter.NewRequest(req)
	}

	t.Run("default threshold", func(t *testing.T) {
		t.Parallel()
		rr := require.New(t)

		w := httptest.NewRecorder()

		rr.NoError(srv.getCostDistribution(w, newReq("")))
		rr.Equal(http.StatusOK, w.Code)
		rr.Equal("application/json", w.Header().Get("Content-Type"))

		var resp costDistributionResponse
		rr.NoError(json.Unmarshal(w.Body.Bytes(), &resp))
		rr.Equal(3, resp.Data.TotalJobs)
		rr.Equal(1000, resp.Data.ThresholdMs)
		rr.Equal(2, resp.Data.FastJobs) // 200, 900 <= 1000
		rr.Equal(1, resp.Data.SlowJobs) // 8000 > 1000
		rr.InDelta(8000, resp.Data.CostEWMAMs.Max, 0.001)
		rr.InDelta(500, resp.Data.DelayEWMAMs.Max, 0.001)
	})

	t.Run("custom threshold via query param", func(t *testing.T) {
		t.Parallel()
		rr := require.New(t)

		w := httptest.NewRecorder()

		rr.NoError(srv.getCostDistribution(w, newReq("thresholdMs=500")))
		rr.Equal(http.StatusOK, w.Code)

		var resp costDistributionResponse
		rr.NoError(json.Unmarshal(w.Body.Bytes(), &resp))
		rr.Equal(500, resp.Data.ThresholdMs)
		rr.Equal(1, resp.Data.FastJobs) // only 200 <= 500
		rr.Equal(2, resp.Data.SlowJobs) // 900, 8000 > 500
	})
}
