package checks_test

import (
	"sort"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/postgres"
	entcore "github.com/fclairamb/solidping/server/internal/entitlements"
	"github.com/fclairamb/solidping/server/internal/handlers/checks"
	"github.com/fclairamb/solidping/server/internal/notifier"
	"github.com/fclairamb/solidping/server/internal/utils/timeutils"
)

// Ports for the multi-region embedded-Postgres suites (spec 2026-07-20-05).
// Distinct from every other _postgres_test.go port claimed in the repo
// (15438-15448, 15451-15455 — see the port-numbering comment in
// postgres_headroom_postgres_test.go), so the parallel suites never collide.
const (
	portStaleReconcilePG = 15456
	portRegionSpreadPG   = 15457
)

// newReconcilePostgresService builds a checks.Service over a REAL embedded
// Postgres (not SQLite) so the interval `region_spread` column, the
// `regions` text[] round-trip, and the dialect-specific
// ListChecksWithStaleJobPeriods query are exercised on Postgres — the SQLite
// suites (reconcile_test.go) can never catch a Postgres-only regression there.
// Self-skips under -short (the default `make test` / CI mode) and on any
// embedded-startup error, mirroring the other _postgres_test.go files.
func newReconcilePostgresService(
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

	org := models.NewOrganization("reconcile-pg", "Reconcile PG Org")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	entSvc := entcore.NewService(dbSvc, entcore.DefaultsFor(config.DeploymentModeSelfHosted), 0)
	svc := checks.NewService(dbSvc, notifier.NewLocalEventNotifier(), disabledCreds(t), entSvc)

	return svc, dbSvc, org
}

// TestStartupReconcileFixesStaleSplitPeriodJobs_Postgres is the Postgres twin
// of TestStartupReconcileFixesStaleSplitPeriodJobs: the one-shot startup pass
// must find and re-level pre-spec split-period jobs, and re-running it must be
// a no-op. The `cj.period <> c.period` predicate compares Postgres `interval`
// values here, not SQLite text — the SQLite suite alone could never catch a
// broken interval comparison.
func TestStartupReconcileFixesStaleSplitPeriodJobs_Postgres(t *testing.T) {
	t.Parallel()

	svc, dbSvc, org := newReconcilePostgresService(t, portStaleReconcilePG)
	r := require.New(t)
	ctx := t.Context()

	resp, err := svc.CreateCheck(ctx, org.Slug, checks.CreateCheckRequest{
		Type:    "http",
		Config:  map[string]any{"url": "https://example.com"},
		Regions: []string{"default", "eu-2", "us-1"},
		Period:  strPtr("00:01:00"),
	})
	r.NoError(err)

	// Simulate pre-spec rows: rewrite every job to the old 3m split period.
	stale := timeutils.Duration(3 * time.Minute)
	_, err = dbSvc.DB().NewUpdate().
		Model((*models.CheckJob)(nil)).
		Set("period = ?", stale).
		Where("check_uid = ?", resp.UID).
		Exec(ctx)
	r.NoError(err)

	n, err := svc.ReconcileStaleJobSchedules(ctx)
	r.NoError(err)
	r.Equal(1, n)

	jobsAfter, err := dbSvc.ListCheckJobsByCheckUID(ctx, resp.UID)
	r.NoError(err)
	r.Len(jobsAfter, 3)
	for _, j := range jobsAfter {
		r.Equal(time.Minute, time.Duration(j.Period),
			"stale split-period job must be re-leveled to the full per-region period")
		r.NotNil(j.ScheduledAt)
		r.NotNil(j.EffectiveScheduledAt)
	}

	// Idempotent: nothing stale remains.
	n, err = svc.ReconcileStaleJobSchedules(ctx)
	r.NoError(err)
	r.Zero(n, "startup reconcile must be idempotent")
}

// TestRegionSpreadRoundTripAndStagger_Postgres exercises the region_spread
// interval column round-trip and the spread-staggered createCheckJobs path on
// Postgres: validation, persistence through a fresh read, and the per-region
// full-period jobs staggered by the override spread (spec 2026-07-20-05).
func TestRegionSpreadRoundTripAndStagger_Postgres(t *testing.T) {
	t.Parallel()

	svc, dbSvc, org := newReconcilePostgresService(t, portRegionSpreadPG)
	r := require.New(t)
	ctx := t.Context()

	// Validation: spread == period is rejected on Postgres too.
	_, err := svc.CreateCheck(ctx, org.Slug, checks.CreateCheckRequest{
		Type:         "http",
		Config:       map[string]any{"url": "https://example.com"},
		Regions:      []string{"default", "eu-2"},
		Period:       strPtr("00:01:00"),
		RegionSpread: strPtr("00:01:00"),
	})
	r.Error(err)
	r.Contains(err.Error(), "regionSpread")

	// Create with a valid 5s override across 3 regions.
	before := time.Now()
	resp, err := svc.CreateCheck(ctx, org.Slug, checks.CreateCheckRequest{
		Type:         "http",
		Config:       map[string]any{"url": "https://example.com"},
		Regions:      []string{"default", "eu-2", "us-1"},
		Period:       strPtr("00:01:00"),
		RegionSpread: strPtr("00:00:05"),
	})
	r.NoError(err)
	r.NotNil(resp.RegionSpread)
	r.Equal("00:00:05", *resp.RegionSpread)

	// The interval column round-trips through a fresh read from Postgres.
	got, err := svc.GetCheck(ctx, org.Slug, resp.UID, checks.GetCheckOptions{})
	r.NoError(err)
	r.NotNil(got.RegionSpread)
	r.Equal("00:00:05", *got.RegionSpread)

	// createCheckJobs materialized one full-period job per region (no split),
	// its first tick staggered by the 5s override spread; region 0 stays at
	// ~now for the check-created express path.
	jobs, err := dbSvc.ListCheckJobsByCheckUID(ctx, resp.UID)
	r.NoError(err)
	r.Len(jobs, 3)

	offsets := make([]int, 0, len(jobs))
	for _, j := range jobs {
		r.Equal(time.Minute, time.Duration(j.Period), "each region runs at the full period, not the split period")
		r.NotNil(j.ScheduledAt)
		offsets = append(offsets, int(j.ScheduledAt.Sub(before).Round(time.Second)/time.Second))
	}
	sort.Ints(offsets)

	// Sorted offsets ~ [0, 5, 10]: consecutive first ticks are one spread apart.
	r.InDelta(5, offsets[1]-offsets[0], 1, "regions staggered by the override spread")
	r.InDelta(5, offsets[2]-offsets[1], 1, "regions staggered by the override spread")

	// Clearing the override reverts to the default and persists.
	cleared, err := svc.UpdateCheck(ctx, org.Slug, resp.UID, &checks.UpdateCheckRequest{
		RegionSpread: strPtr(""),
	})
	r.NoError(err)
	r.Nil(cleared.RegionSpread, "empty string clears the override back to the default")
}
