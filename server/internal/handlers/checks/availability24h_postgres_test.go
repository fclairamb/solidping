package checks_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/postgres"
	entcore "github.com/fclairamb/solidping/server/internal/entitlements"
	"github.com/fclairamb/solidping/server/internal/handlers/checks"
	"github.com/fclairamb/solidping/server/internal/notifier"
)

// Port for the availability24h embedded-Postgres suite. Distinct from every
// other _postgres_test.go port claimed in the repo (see the port-numbering
// comment in postgres_headroom_postgres_test.go, and portCheckStatsPG in
// stats_postgres_test.go), so the parallel suites never collide.
const portAvailability24hPG = 15502

// newAvailability24hPostgresDB boots a REAL embedded Postgres so the two
// tier-aligned SQL aggregates behind GetOrgAvailability24h are exercised on
// the production dialect — the CASE/SUM raw-tier query in particular is
// exactly the kind of thing SQLite tolerates more loosely than Postgres.
// Self-skips under -short (the default `make test` / CI mode) and on any
// embedded-startup error, mirroring the other _postgres_test.go files.
//
// Returns only the db.Service, not a checks.Service: each subtest below
// builds its own checks.Service over this shared connection so it can pin
// its own `now` via SetRegionHealthNowForTest without racing every other
// parallel subtest over one shared Service's clock field.
func newAvailability24hPostgresDB(t *testing.T) db.Service {
	t.Helper()

	if testing.Short() {
		t.Skip("skipping embedded-postgres test in -short mode")
	}

	ctx := t.Context()

	dbSvc, err := postgres.New(ctx, &postgres.Config{
		Embedded: true, Port: portAvailability24hPG, RunMode: "test",
	})
	if err != nil {
		t.Skipf("embedded postgres unavailable: %v", err)
	}

	t.Cleanup(func() { _ = dbSvc.Close() })

	if initErr := dbSvc.Initialize(ctx); initErr != nil {
		t.Skipf("embedded postgres init failed: %v", initErr)
	}

	return dbSvc
}

// TestGetCheckStatsAvailability24h_Postgres is the Postgres twin of the
// SQLite suite: the very same table of cases (availability24hCases), run
// against the production dialect.
func TestGetCheckStatsAvailability24h_Postgres(t *testing.T) {
	t.Parallel()

	dbSvc := newAvailability24hPostgresDB(t)
	entSvc := entcore.NewService(dbSvc, entcore.DefaultsFor(config.DeploymentModeSelfHosted), 0)

	for i, tc := range availability24hCases() {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)
			ctx := t.Context()

			org := models.NewOrganization(fmt.Sprintf("pgavail24h-%d", i), "PG Availability Org")
			r.NoError(dbSvc.CreateOrganization(ctx, org))

			// Own Service instance per subtest: SetRegionHealthNowForTest
			// mutates a shared field, and subtests run in parallel.
			svc := checks.NewService(dbSvc, notifier.NewLocalEventNotifier(), disabledCreds(t), entSvc)
			now := time.Now().UTC()
			checks.SetRegionHealthNowForTest(svc, func() time.Time { return now })

			check := models.NewCheck(org.UID, fmt.Sprintf("pgavail24h-check-%d", i), "http")
			r.NoError(dbSvc.CreateCheck(ctx, check))

			tc.setup(ctx, t, dbSvc, org.UID, check.UID, now)

			stats, err := svc.GetCheckStats(ctx, org.Slug)
			r.NoError(err)

			requireAvailability24hEqual(t, tc.wantPct, stats.Availability24h)
		})
	}
}
