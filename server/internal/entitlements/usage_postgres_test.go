package entitlements_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/postgres"
	"github.com/fclairamb/solidping/server/internal/entitlements"
	"github.com/fclairamb/solidping/server/internal/utils/timeutils"
)

// portUsagePG is distinct from every other _postgres_test.go embedded-Postgres
// port claimed in the repo (15438-15448, 15451-15457 — see the port-numbering
// comment in db/postgres/postgres_headroom_postgres_test.go).
const portUsagePG = 15458

// TestUsageChecksPerMinuteCountsRegions_Postgres is the Postgres twin of
// TestUsageChecksPerMinuteCountsRegions: it exercises ListOrgCheckRates'
// `regions` text[] selection on a REAL Postgres backend (spec 2026-07-20-05),
// which the SQLite suite alone can't validate — a multi-region check
// contributes (60s/period) x region_count to ChecksPerMinute. Self-skips under
// -short and on any embedded-startup error.
func TestUsageChecksPerMinuteCountsRegions_Postgres(t *testing.T) {
	t.Parallel()

	if testing.Short() {
		t.Skip("skipping embedded-postgres test in -short mode")
	}

	ctx := t.Context()
	r := require.New(t)

	dbSvc, err := postgres.New(ctx, &postgres.Config{Embedded: true, Port: portUsagePG, RunMode: "test"})
	if err != nil {
		t.Skipf("embedded postgres unavailable: %v", err)
	}

	t.Cleanup(func() { _ = dbSvc.Close() })

	if initErr := dbSvc.Initialize(ctx); initErr != nil {
		t.Skipf("embedded postgres init failed: %v", initErr)
	}

	org := models.NewOrganization("usage-pg-org", "Usage PG Org")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	svc := entitlements.NewService(dbSvc, entitlements.DefaultsFor(config.DeploymentModeSelfHosted), 0)

	mkCheck := func(slug string, period time.Duration, regions []string) {
		check := models.NewCheck(org.UID, slug, "http")
		check.Period = timeutils.Duration(period)
		check.Regions = regions
		check.Config = models.JSONMap{"url": "https://example.com"}
		r.NoError(dbSvc.CreateCheck(ctx, check))
	}

	// 1m check, no regions → 1.0/min (min region count is 1).
	mkCheck("no-regions", time.Minute, nil)
	// 1m check, 3 regions → 3.0/min.
	mkCheck("three-regions", time.Minute, []string{"default", "eu-2", "us-1"})
	// 30s check, 2 regions → (60/30) x 2 = 4.0/min.
	mkCheck("two-regions-30s", 30*time.Second, []string{"default", "eu-2"})

	usage, err := svc.Usage(ctx, org.UID)
	r.NoError(err)
	r.Equal(3, usage.Checks)
	r.InDelta(8.0, usage.ChecksPerMinute, 0.001,
		"1 (no-region) + 3 (3 regions) + 4 (2 regions @30s) = 8 checks/minute")
}
