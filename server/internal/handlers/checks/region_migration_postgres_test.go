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

// Ports for the region-migration embedded-Postgres suites (spec
// 2026-08-24-08). Distinct from every other _postgres_test.go port claimed in
// the repo (see the port-numbering comment in
// postgres_headroom_postgres_test.go), so the parallel suites never collide.
const (
	portRegionMigrationPG = 15498
	portRegionStalePG     = 15499
)

// The SQLite suite in region_migration_test.go cannot catch a Postgres-only
// regression here: `checks.regions` is a real `text[]` on Postgres and a JSON
// text array on SQLite, so ListChecksReferencingRegion (`= ANY`),
// ListChecksWithStaleJobRegions (`unnest` / `array_length`) and the
// `pgdialect.Array` write in MigrateCheckRegionSlug are entirely different
// code on the two backends. These twins exercise the Postgres half.

// newRegionMigrationPostgresService builds a checks.Service over a REAL
// embedded Postgres. Self-skips under -short and on any embedded-startup
// error, mirroring the other _postgres_test.go files.
func newRegionMigrationPostgresService(
	t *testing.T, port uint32,
) (*checks.Service, *postgres.Service, *models.Organization) {
	t.Helper()

	if testing.Short() {
		t.Skip("skipping embedded-postgres test in -short mode")
	}

	ctx := t.Context()
	r := require.New(t)

	dbSvc, err := postgres.New(ctx, &postgres.Config{Embedded: true, Port: port, RunMode: "test"})
	if err != nil {
		t.Skipf("embedded postgres unavailable: %v", err)
	}

	t.Cleanup(func() { _ = dbSvc.Close() })

	if initErr := dbSvc.Initialize(ctx); initErr != nil {
		t.Skipf("embedded postgres init failed: %v", initErr)
	}

	r.NoError(dbSvc.SetSystemParameter(ctx, regions.ParamRegions, migratedRegionDefs(), false))

	org := models.NewOrganization("region-migration-pg", "Region Migration PG Org")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	entSvc := entcore.NewService(dbSvc, entcore.DefaultsFor(config.DeploymentModeSelfHosted), 0)
	svc := checks.NewService(dbSvc, notifier.NewLocalEventNotifier(), disabledCreds(t), entSvc)

	return svc, dbSvc, org
}

// pgJobRegions lists a check's job regions on Postgres (nil rendered as "").
func pgJobRegions(t *testing.T, dbSvc *postgres.Service, checkUID string) []string {
	t.Helper()

	jobs, err := dbSvc.ListCheckJobsByCheckUID(t.Context(), checkUID)
	require.NoError(t, err)

	out := make([]string, 0, len(jobs))
	for _, job := range jobs {
		region := ""
		if job.Region != nil {
			region = *job.Region
		}

		out = append(out, region)
	}

	return out
}

// TestMigrateRegion_Postgres exercises the whole migration against the real
// text[] column: the array rewrite, the unique-index collapse, the dry run and
// idempotency.
func TestMigrateRegion_Postgres(t *testing.T) {
	t.Parallel()

	svc, dbSvc, org := newRegionMigrationPostgresService(t, portRegionMigrationPG)
	r := require.New(t)
	ctx := t.Context()

	moved, err := svc.CreateCheck(ctx, org.Slug, checks.CreateCheckRequest{
		Type:    "http",
		Config:  map[string]any{"url": "https://example.com/moved"},
		Regions: []string{"default"},
		Period:  strPtr("00:01:00"),
	})
	r.NoError(err)

	collapsed, err := svc.CreateCheck(ctx, org.Slug, checks.CreateCheckRequest{
		Type:    "http",
		Config:  map[string]any{"url": "https://example.com/collapsed"},
		Regions: []string{"default", "gravelines"},
		Period:  strPtr("00:01:00"),
	})
	r.NoError(err)

	// Backdate every job so the overdue accounting has something to report.
	past := time.Now().Add(-90 * time.Minute)
	_, err = dbSvc.DB().NewUpdate().
		Model((*models.CheckJob)(nil)).
		Set("scheduled_at = ?", past).
		Where("region = ?", "default").
		Exec(ctx)
	r.NoError(err)

	// Dry run first: full report, zero writes.
	dry, err := svc.MigrateRegion(ctx,
		checks.RegionMigrationRequest{From: "default", To: "gravelines", DryRun: true}, "actor-uid")
	r.NoError(err)
	r.Equal(2, dry.ChecksUpdated)
	r.Equal(1, dry.JobsReassigned)
	r.Equal(1, dry.JobsDeleted)
	r.Equal(1, dry.OverdueRecovered)

	stillStale, err := dbSvc.GetCheck(ctx, org.UID, moved.UID)
	r.NoError(err)
	r.Equal([]string{"default"}, stillStale.Regions, "dry run must not touch the text[] column")

	applied, err := svc.MigrateRegion(ctx,
		checks.RegionMigrationRequest{From: "default", To: "gravelines"}, "actor-uid")
	r.NoError(err)
	r.Equal(dry.ChecksUpdated, applied.ChecksUpdated)
	r.Equal(dry.JobsReassigned, applied.JobsReassigned)
	r.Equal(dry.JobsDeleted, applied.JobsDeleted)
	r.Equal(map[string]int{org.Slug: 2}, applied.ByOrg)

	storedMoved, err := dbSvc.GetCheck(ctx, org.UID, moved.UID)
	r.NoError(err)
	r.Equal([]string{"gravelines"}, storedMoved.Regions)
	r.Equal([]string{"gravelines"}, pgJobRegions(t, dbSvc, moved.UID))

	storedCollapsed, err := dbSvc.GetCheck(ctx, org.UID, collapsed.UID)
	r.NoError(err)
	r.Equal([]string{"gravelines"}, storedCollapsed.Regions,
		"a check declaring both slugs must de-duplicate, not keep gravelines twice")
	r.Equal([]string{"gravelines"}, pgJobRegions(t, dbSvc, collapsed.UID))

	// Idempotent.
	again, err := svc.MigrateRegion(ctx,
		checks.RegionMigrationRequest{From: "default", To: "gravelines"}, "actor-uid")
	r.NoError(err)
	r.Zero(again.ChecksUpdated)
	r.Zero(again.JobsReassigned)
	r.Zero(again.JobsDeleted)
}

// TestStartupReconcileFixesStaleJobRegions_Postgres is the Postgres twin of
// the boot-pass test: the `unnest` / `= ANY(c.regions)` predicates only exist
// on this backend.
func TestStartupReconcileFixesStaleJobRegions_Postgres(t *testing.T) {
	t.Parallel()

	svc, dbSvc, org := newRegionMigrationPostgresService(t, portRegionStalePG)
	r := require.New(t)
	ctx := t.Context()

	drifted, err := svc.CreateCheck(ctx, org.Slug, checks.CreateCheckRequest{
		Type:    "http",
		Config:  map[string]any{"url": "https://example.com/drifted"},
		Regions: []string{"gravelines"},
		Period:  strPtr("00:01:00"),
	})
	r.NoError(err)

	holed, err := svc.CreateCheck(ctx, org.Slug, checks.CreateCheckRequest{
		Type:    "http",
		Config:  map[string]any{"url": "https://example.com/holed"},
		Regions: []string{"gravelines", "paris"},
		Period:  strPtr("00:01:00"),
	})
	r.NoError(err)

	// A job stranded under the pre-rename slug…
	_, err = dbSvc.DB().NewUpdate().
		Model((*models.CheckJob)(nil)).
		Set("region = ?", "default").
		Where("check_uid = ?", drifted.UID).
		Exec(ctx)
	r.NoError(err)

	// …and the symmetric hole: a declared region with no job at all.
	_, err = dbSvc.DB().NewDelete().
		Model((*models.CheckJob)(nil)).
		Where("check_uid = ?", holed.UID).
		Where("region = ?", "paris").
		Exec(ctx)
	r.NoError(err)

	n, err := svc.ReconcileStaleJobSchedules(ctx)
	r.NoError(err)
	r.Equal(2, n)

	r.Equal([]string{"gravelines"}, pgJobRegions(t, dbSvc, drifted.UID))
	r.ElementsMatch([]string{"gravelines", "paris"}, pgJobRegions(t, dbSvc, holed.UID))

	n, err = svc.ReconcileStaleJobSchedules(ctx)
	r.NoError(err)
	r.Zero(n, "startup reconcile must stay idempotent once regions line up")
}
