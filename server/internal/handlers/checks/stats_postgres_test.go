package checks_test

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/postgres"
	entcore "github.com/fclairamb/solidping/server/internal/entitlements"
	"github.com/fclairamb/solidping/server/internal/handlers/checks"
	"github.com/fclairamb/solidping/server/internal/notifier"
)

// Port for the check-stats embedded-Postgres suite. Distinct from every other
// _postgres_test.go port claimed in the repo (15438-15466 — see the
// port-numbering comment in postgres_headroom_postgres_test.go), so the
// parallel suites never collide.
const portCheckStatsPG = 15467

// newStatsPostgresService builds a checks.Service over a REAL embedded
// Postgres so the GROUP BY aggregation is exercised on the production dialect
// — the SQLite suite alone could never catch a Postgres-only regression (e.g.
// a boolean literal or GROUP BY expression SQLite tolerates and Postgres
// rejects). Self-skips under -short (the default `make test` / CI mode) and on
// any embedded-startup error, mirroring the other _postgres_test.go files.
//
// Deliberately called ONCE for the whole file: every case gets its own
// organization inside the single instance instead of its own instance, because
// concurrently booting two embedded-postgres servers races on the shared
// extraction directory (~/.embedded-postgres-go/extracted) and one of them
// silently skips.
func newStatsPostgresService(t *testing.T) (*checks.Service, db.Service) {
	t.Helper()

	if testing.Short() {
		t.Skip("skipping embedded-postgres test in -short mode")
	}

	ctx := t.Context()

	dbSvc, err := postgres.New(ctx, &postgres.Config{
		Embedded: true, Port: portCheckStatsPG, RunMode: "test",
	})
	if err != nil {
		t.Skipf("embedded postgres unavailable: %v", err)
	}

	t.Cleanup(func() { _ = dbSvc.Close() })

	if initErr := dbSvc.Initialize(ctx); initErr != nil {
		t.Skipf("embedded postgres init failed: %v", initErr)
	}

	entSvc := entcore.NewService(dbSvc, entcore.DefaultsFor(config.DeploymentModeSelfHosted), 0)
	svc := checks.NewService(dbSvc, notifier.NewLocalEventNotifier(), disabledCreds(t), entSvc)

	return svc, dbSvc
}

// TestGetCheckStats_Postgres is the Postgres twin of the SQLite suites: the
// very same table of cases (statsAggregationCases) plus the issue #172
// >100-checks regression proof, run against the production dialect.
func TestGetCheckStats_Postgres(t *testing.T) {
	t.Parallel()

	svc, dbSvc := newStatsPostgresService(t)

	for i, tc := range statsAggregationCases() {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)
			ctx := t.Context()

			org := models.NewOrganization(fmt.Sprintf("pgstats-%d", i), "PG Stats Org")
			r.NoError(dbSvc.CreateOrganization(ctx, org))

			seedStatsChecks(ctx, t, dbSvc, org.UID, tc.fixtures)

			stats, err := svc.GetCheckStats(ctx, org.Slug)
			r.NoError(err)
			requireStatsEqual(t, tc.expected, stats)
		})
	}

	// 250 checks must all be counted, well past the list endpoint's 100-row
	// page clamp that made the dashboard's client-side counting wrong.
	t.Run("beyond the 100-row page clamp", func(t *testing.T) {
		t.Parallel()
		r := require.New(t)
		ctx := t.Context()

		org := models.NewOrganization("pgstats-clamp", "PG Stats Clamp Org")
		r.NoError(dbSvc.CreateOrganization(ctx, org))

		const (
			upCount       = 130
			downCount     = 105
			disabledCount = 15
		)

		fixtures := make([]statsFixture, 0, upCount+downCount+disabledCount)
		for range upCount {
			fixtures = append(fixtures, statsFixture{status: models.CheckStatusUp, enabled: true})
		}

		for range downCount {
			fixtures = append(fixtures, statsFixture{status: models.CheckStatusDown, enabled: true})
		}

		for range disabledCount {
			fixtures = append(fixtures, statsFixture{status: models.CheckStatusUp, enabled: false})
		}

		seedStatsChecks(ctx, t, dbSvc, org.UID, fixtures)

		stats, err := svc.GetCheckStats(ctx, org.Slug)
		r.NoError(err)

		r.Equal(upCount+downCount+disabledCount, stats.Total)
		r.Greater(stats.Total, 100, "total must exceed the 100-row page clamp")
		r.Equal(upCount+downCount, stats.Enabled)
		r.Equal(disabledCount, stats.Disabled)
		r.Equal(downCount, stats.Down)
		r.Greater(stats.Down, 100, "down count must exceed what one clamped page could ever report")
		r.Equal(downCount, stats.HardDown)
		r.Equal(upCount+disabledCount, stats.ByStatus[models.WireStatusUp])
		r.Equal(downCount, stats.ByStatus[models.WireStatusDown])
	})
}
