package costdist_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/scheduling/costdist"
	"github.com/fclairamb/solidping/server/internal/utils/timeutils"
)

// seedJob inserts a check (+ its check_job) carrying the given cost/delay EWMA.
func seedJob(
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

func TestCompute_SQLitePercentilesAndCounts(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	r := require.New(t)

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	org := models.NewOrganization("cdorg", "CD Org")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	// Costs 100..1000 in steps of 100 (10 values). Delay all 0.
	costs := []float64{100, 200, 300, 400, 500, 600, 700, 800, 900, 1000}
	for i, c := range costs {
		seedJob(ctx, r, dbSvc, org.UID, slugFor(i), c, 0)
	}

	dist, err := costdist.Compute(ctx, dbSvc.DB(), false /* sqlite */, 500)
	r.NoError(err)

	r.Equal(10, dist.TotalJobs)
	r.Equal(500, dist.ThresholdMs)
	// cost <= 500: 100,200,300,400,500 -> 5 fast; 5 slow.
	r.Equal(5, dist.FastJobs)
	r.Equal(5, dist.SlowJobs)

	// percentile_cont over sorted [100..1000], n=10:
	//   p50 = value at rank 0.5*9=4.5 -> interp(500,600)=550
	//   p90 = rank 0.9*9=8.1 -> interp(900,1000)=910
	//   p99 = rank 0.99*9=8.91 -> interp(900,1000)=991
	r.InDelta(550, dist.CostEWMAMs.P50, 0.001)
	r.InDelta(910, dist.CostEWMAMs.P90, 0.001)
	r.InDelta(991, dist.CostEWMAMs.P99, 0.001)
	r.InDelta(1000, dist.CostEWMAMs.Max, 0.001)

	// Delay is all zero.
	r.InDelta(0, dist.DelayEWMAMs.P50, 0.001)
	r.InDelta(0, dist.DelayEWMAMs.Max, 0.001)
}

func TestCompute_EmptyIsZero(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	r := require.New(t)

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	dist, err := costdist.Compute(ctx, dbSvc.DB(), false, 0)
	r.NoError(err)
	r.Equal(0, dist.TotalJobs)
	r.Equal(0, dist.FastJobs)
	r.Equal(0, dist.SlowJobs)
	// Default threshold applied when 0 passed.
	r.Equal(costdist.DefaultThresholdMs, dist.ThresholdMs)
	r.InDelta(0, dist.CostEWMAMs.Max, 0.001)
}

func TestCompute_DefaultThresholdApplied(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	r := require.New(t)

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	org := models.NewOrganization("cdorg2", "CD Org 2")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	// Two fast (<=1000) and one slow (>1000) at the default 1000ms threshold.
	seedJob(ctx, r, dbSvc, org.UID, "j-a", 200, 5)
	seedJob(ctx, r, dbSvc, org.UID, "j-b", 900, 0)
	seedJob(ctx, r, dbSvc, org.UID, "j-c", 8000, 1200)

	dist, err := costdist.Compute(ctx, dbSvc.DB(), false, 0)
	r.NoError(err)
	r.Equal(costdist.DefaultThresholdMs, dist.ThresholdMs)
	r.Equal(3, dist.TotalJobs)
	r.Equal(2, dist.FastJobs)
	r.Equal(1, dist.SlowJobs)
	r.InDelta(8000, dist.CostEWMAMs.Max, 0.001)
	r.InDelta(1200, dist.DelayEWMAMs.Max, 0.001)
}

func slugFor(i int) string {
	return "cost-job-" + string(rune('a'+i))
}
