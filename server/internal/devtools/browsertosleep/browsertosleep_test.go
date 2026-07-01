package browsertosleep_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/devtools/browsertosleep"
	"github.com/fclairamb/solidping/server/internal/utils/timeutils"
)

func newDB(t *testing.T) (context.Context, *require.Assertions, *sqlite.Service) {
	t.Helper()

	ctx := t.Context()
	r := require.New(t)

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	return ctx, r, dbSvc
}

// seedCheck creates a check (job auto-materialization disabled) plus one
// check_job with the given cost_ewma_ms, and returns the check.
func seedCheck(
	ctx context.Context, r *require.Assertions, dbSvc *sqlite.Service,
	orgUID, slug, checkType string, costEWMAMs float64,
) *models.Check {
	check := models.NewCheck(orgUID, slug, checkType)
	check.Enabled = false
	check.Config = models.JSONMap{"url": "https://example.com"}
	r.NoError(dbSvc.CreateCheck(ctx, check))

	cj := models.NewCheckJob(orgUID, check.UID, timeutils.Duration(time.Minute))
	cj.Type = checkType
	cj.Config = models.JSONMap{"url": "https://example.com"}
	cj.CostEWMAMs = costEWMAMs
	r.NoError(dbSvc.CreateCheckJob(ctx, cj))

	return check
}

func TestConvert_BrowserBecomesSleepSeededFromCost(t *testing.T) {
	t.Parallel()

	ctx, r, dbSvc := newDB(t)

	org := models.NewOrganization("btsorg", "BTS Org")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	// A browser check with a 4200ms cost signal.
	browserCheck := seedCheck(ctx, r, dbSvc, org.UID, "br1", "browser", 4200)
	// A non-browser check that must be left alone.
	httpCheck := seedCheck(ctx, r, dbSvc, org.UID, "http1", "http", 300)

	res, err := browsertosleep.New(dbSvc, nil).Run(ctx)
	r.NoError(err)
	r.Equal(1, res.ChecksConverted)
	r.Equal(1, res.JobsConverted)

	// Browser check is now a sleep check with sleep_ms == round(cost_ewma_ms).
	got, err := dbSvc.GetCheck(ctx, org.UID, browserCheck.UID)
	r.NoError(err)
	r.Equal("sleep", got.Type)
	r.EqualValues(4200, got.Config["sleep_ms"])

	// Its check_job is retyped too.
	jobs, err := dbSvc.ListCheckJobsByCheckUID(ctx, browserCheck.UID)
	r.NoError(err)
	r.Len(jobs, 1)
	r.Equal("sleep", jobs[0].Type)
	r.EqualValues(4200, jobs[0].Config["sleep_ms"])

	// The http check is untouched.
	gotHTTP, err := dbSvc.GetCheck(ctx, org.UID, httpCheck.UID)
	r.NoError(err)
	r.Equal("http", gotHTTP.Type)
}

func TestConvert_FallbackDefaultWhenNoCost(t *testing.T) {
	t.Parallel()

	ctx, r, dbSvc := newDB(t)

	org := models.NewOrganization("btsorg2", "BTS Org 2")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	// Browser check with no cost signal yet (cost_ewma_ms == 0).
	browserCheck := seedCheck(ctx, r, dbSvc, org.UID, "br1", "browser", 0)

	res, err := browsertosleep.New(dbSvc, nil).Run(ctx)
	r.NoError(err)
	r.Equal(1, res.ChecksConverted)

	got, err := dbSvc.GetCheck(ctx, org.UID, browserCheck.UID)
	r.NoError(err)
	r.Equal("sleep", got.Type)
	r.EqualValues(browsertosleep.DefaultSleepMs, got.Config["sleep_ms"])
}

func TestConvert_Idempotent(t *testing.T) {
	t.Parallel()

	ctx, r, dbSvc := newDB(t)

	org := models.NewOrganization("btsorg3", "BTS Org 3")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	seedCheck(ctx, r, dbSvc, org.UID, "br1", "browser", 1500)

	// First run converts.
	res1, err := browsertosleep.New(dbSvc, nil).Run(ctx)
	r.NoError(err)
	r.Equal(1, res1.ChecksConverted)

	// Second run is a no-op (no browser checks remain).
	res2, err := browsertosleep.New(dbSvc, nil).Run(ctx)
	r.NoError(err)
	r.Equal(0, res2.ChecksConverted)
	r.Equal(0, res2.JobsConverted)
}

func TestConvert_SeedFromMaxCostAcrossJobs(t *testing.T) {
	t.Parallel()

	ctx, r, dbSvc := newDB(t)

	org := models.NewOrganization("btsorg4", "BTS Org 4")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	check := models.NewCheck(org.UID, "b-multi", "browser")
	check.Enabled = false
	r.NoError(dbSvc.CreateCheck(ctx, check))

	// Two jobs (different regions) with different cost; the seed uses the max.
	regionA := "eu"
	regionB := "us"

	cjA := models.NewCheckJob(org.UID, check.UID, timeutils.Duration(time.Minute))
	cjA.Type = "browser"
	cjA.Region = &regionA
	cjA.CostEWMAMs = 1200
	r.NoError(dbSvc.CreateCheckJob(ctx, cjA))

	cjB := models.NewCheckJob(org.UID, check.UID, timeutils.Duration(time.Minute))
	cjB.Type = "browser"
	cjB.Region = &regionB
	cjB.CostEWMAMs = 9000
	r.NoError(dbSvc.CreateCheckJob(ctx, cjB))

	res, err := browsertosleep.New(dbSvc, nil).Run(ctx)
	r.NoError(err)
	r.Equal(1, res.ChecksConverted)
	r.Equal(2, res.JobsConverted)

	got, err := dbSvc.GetCheck(ctx, org.UID, check.UID)
	r.NoError(err)
	r.EqualValues(9000, got.Config["sleep_ms"])
}

func TestConvert_NoOpWhenNoBrowserChecks(t *testing.T) {
	t.Parallel()

	ctx, r, dbSvc := newDB(t)

	org := models.NewOrganization("btsorg5", "BTS Org 5")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	seedCheck(ctx, r, dbSvc, org.UID, "http1", "http", 300)

	res, err := browsertosleep.New(dbSvc, nil).Run(ctx)
	r.NoError(err)
	r.Equal(0, res.ChecksConverted)
	r.Equal(0, res.JobsConverted)
}
