package checks_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/postgres"
	entcore "github.com/fclairamb/solidping/server/internal/entitlements"
	"github.com/fclairamb/solidping/server/internal/handlers/checks"
	"github.com/fclairamb/solidping/server/internal/notifier"
	"github.com/fclairamb/solidping/server/internal/regions"
)

// Port for the region-health embedded-Postgres suite (spec 2026-08-24-09).
// Distinct from every other _postgres_test.go port claimed in the repo (see
// the port-numbering comment in postgres_headroom_postgres_test.go) — highest
// claimed at the time this was written was 15499 (region_migration).
const portRegionHealthPG = 15500

// RegionHealth's checksReferencing count reads `checks.regions` through a
// narrow, hand-rolled ColumnExpr("regions") projection rather than a full
// Model(&checks) scan — the shape every OTHER checks.regions reader in this
// package uses. On Postgres, `regions` is a real `text[]`; on SQLite it is a
// JSON-encoded string. The SQLite suite in region_health_test.go cannot catch
// a Postgres-only decode regression in that narrower projection, so this twin
// exercises it against a real embedded Postgres.
func TestRegionHealthPostgresChecksRegionsProjection(t *testing.T) {
	t.Parallel()

	if testing.Short() {
		t.Skip("skipping embedded-postgres test in -short mode")
	}

	ctx := t.Context()
	r := require.New(t)

	dbSvc, err := postgres.New(ctx, &postgres.Config{Embedded: true, Port: portRegionHealthPG, RunMode: "test"})
	if err != nil {
		t.Skipf("embedded postgres unavailable: %v", err)
	}

	t.Cleanup(func() { _ = dbSvc.Close() })

	if initErr := dbSvc.Initialize(ctx); initErr != nil {
		t.Skipf("embedded postgres init failed: %v", initErr)
	}

	r.NoError(dbSvc.SetSystemParameter(ctx, regions.ParamRegions, healthRegionDefs(), false))

	org := models.NewOrganization("region-health-pg", "Region Health PG Org")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	entSvc := entcore.NewService(dbSvc, entcore.DefaultsFor(config.DeploymentModeSelfHosted), 0)
	svc := checks.NewService(dbSvc, notifier.NewLocalEventNotifier(), disabledCreds(t), entSvc)

	future := time.Now().Add(time.Hour)
	past := time.Now().Add(-2 * time.Hour)

	// A check naming TWO slugs at once exercises the array decode with more
	// than one element, and a repeated slug (via a raw update below) exercises
	// the per-check de-dup.
	multiCheck := createRegionCheck(t, svc, org.Slug, []string{"healthy", "ghost-pg"})
	_, err = dbSvc.DB().NewUpdate().
		Model((*models.CheckJob)(nil)).
		Set("scheduled_at = ?", future).
		Where("check_uid = ? AND region = ?", multiCheck.UID, "healthy").
		Exec(ctx)
	r.NoError(err)
	_, err = dbSvc.DB().NewUpdate().
		Model((*models.CheckJob)(nil)).
		Set("scheduled_at = ?", past).
		Where("check_uid = ? AND region = ?", multiCheck.UID, "ghost-pg").
		Exec(ctx)
	r.NoError(err)

	healthyWorker := models.NewWorker("w-healthy-pg", "w-healthy-pg")
	healthySlug := "healthy"
	healthyWorker.Region = &healthySlug
	_, err = dbSvc.RegisterOrUpdateWorker(ctx, healthyWorker)
	r.NoError(err)

	report, err := svc.RegionHealth(ctx)
	r.NoError(err)

	byslug := make(map[string]checks.RegionHealthRow, len(report.Regions))
	for _, row := range report.Regions {
		byslug[row.Slug] = row
	}

	healthy := byslug["healthy"]
	r.True(healthy.Declared)
	r.Equal(1, healthy.ChecksReferencing)
	r.Equal(1, healthy.Jobs)
	r.Equal(1, healthy.LiveWorkers)
	r.False(healthy.Ghost)

	ghost := byslug["ghost-pg"]
	r.False(ghost.Declared)
	r.Equal(1, ghost.ChecksReferencing)
	r.Equal(1, ghost.Jobs)
	r.Equal(1, ghost.JobsOverdue)
	r.Equal(0, ghost.LiveWorkers)
	r.True(ghost.Ghost)
}
