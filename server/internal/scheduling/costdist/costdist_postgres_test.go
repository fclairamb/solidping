package costdist_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/postgres"
	"github.com/fclairamb/solidping/server/internal/scheduling/costdist"
	"github.com/fclairamb/solidping/server/internal/utils/timeutils"
)

// TestCompute_PostgresPercentiles exercises the real percentile_cont path on an
// embedded PostgreSQL. It self-skips under `-short` (the default `make test`
// mode) and on any embedded-startup error, so it only runs when the full suite
// is invoked without -short. When it does run, it asserts the SAME fixture as
// the SQLite test, proving the two dialects agree.
func TestCompute_PostgresPercentiles(t *testing.T) {
	t.Parallel()

	if testing.Short() {
		t.Skip("skipping embedded-postgres test in -short mode")
	}

	ctx := t.Context()
	r := require.New(t)

	dbSvc, err := postgres.New(ctx, &postgres.Config{
		Embedded:    true,
		EmbeddedDir: t.TempDir(),
		Port:        15434,
		RunMode:     "test",
	})
	if err != nil {
		t.Skipf("embedded postgres unavailable: %v", err)
	}

	t.Cleanup(func() { _ = dbSvc.Close() })

	if initErr := dbSvc.Initialize(ctx); initErr != nil {
		t.Skipf("embedded postgres init failed: %v", initErr)
	}

	org := models.NewOrganization("cdpgorg", "CD PG Org")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	costs := []float64{100, 200, 300, 400, 500, 600, 700, 800, 900, 1000}
	for i, c := range costs {
		check := models.NewCheck(org.UID, slugFor(i), "sleep")
		check.Enabled = false
		r.NoError(dbSvc.CreateCheck(ctx, check))

		cj := models.NewCheckJob(org.UID, check.UID, timeutils.Duration(time.Minute))
		cj.Type = "sleep"
		cj.CostEWMAMs = c
		r.NoError(dbSvc.CreateCheckJob(ctx, cj))
	}

	dist, err := costdist.Compute(ctx, dbSvc.DB(), true /* postgres */, 500)
	r.NoError(err)

	r.Equal(10, dist.TotalJobs)
	r.Equal(5, dist.FastJobs)
	r.Equal(5, dist.SlowJobs)

	// Same expected values as the SQLite fixture — percentile_cont semantics.
	r.InDelta(550, dist.CostEWMAMs.P50, 0.001)
	r.InDelta(910, dist.CostEWMAMs.P90, 0.001)
	r.InDelta(991, dist.CostEWMAMs.P99, 0.001)
	r.InDelta(1000, dist.CostEWMAMs.Max, 0.001)
}
