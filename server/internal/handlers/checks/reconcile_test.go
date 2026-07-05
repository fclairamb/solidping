package checks_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	entcore "github.com/fclairamb/solidping/server/internal/entitlements"
	"github.com/fclairamb/solidping/server/internal/handlers/checks"
	"github.com/fclairamb/solidping/server/internal/notifier"
)

// newReconcileTestService builds a checks.Service over a fresh in-memory
// sqlite DB with one organization, for tests exercising reconcileCheckJobs
// (spec 2026-07-05-08 D2) through the public CreateCheck/UpdateCheck API.
func newReconcileTestService(t *testing.T) (*checks.Service, *sqlite.Service, *models.Organization) {
	t.Helper()
	r := require.New(t)
	ctx := t.Context()

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	org := models.NewOrganization("reconcile-h", "Reconcile Handler Org")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	entSvc := entcore.NewService(dbSvc, entcore.DefaultsFor(config.DeploymentModeSelfHosted), 0)
	svc := checks.NewService(dbSvc, notifier.NewLocalEventNotifier(), disabledCreds(t), entSvc)

	return svc, dbSvc, org
}

// jobsByRegion indexes check_jobs rows by region (nil-region key is "").
func jobsByRegion(t *testing.T, jobs []*models.CheckJob) map[string]*models.CheckJob {
	t.Helper()

	out := make(map[string]*models.CheckJob, len(jobs))
	for _, j := range jobs {
		region := ""
		if j.Region != nil {
			region = *j.Region
		}
		out[region] = j
	}

	return out
}

// Scope boundary: check *creation* (db/sqlite's and db/postgres's
// createCheckJobs, which materializes the first job per region) is NOT
// touched by this spec and is NOT phase-aligned by D1/D2 — it deliberately
// keeps pinning region 0's very first scheduled_at to literal time.Now() so
// the check.created express-runner path (checkjobsvc.ClaimJobsForCheck,
// which gates on "scheduled_at <= now" with no claim-ahead window) can still
// claim and execute it immediately, guaranteeing a fast first result after
// check creation. Jittering that very first tick (as NextAligned would) was
// tried and reverted during implementation: it pushed region 0's first
// scheduled_at up to a full base period into the future, silently starving
// the express path and regressing "check created → fast first result".
// Region leveling for a fresh multi-region check therefore only takes full
// effect starting from its first reconcile (any subsequent edit — see
// TestReconcilePeriodEditRelevelsAllRegions etc. below) or from its first
// organic release cycle (calculateNextScheduledAt in worker.go, covered by
// TestCalculateNextScheduledAt_PhaseLocked in worker_test.go). This mirrors
// the spec's own D1/D2 file:line scope, which cites only
// calculateNextScheduledAt (worker.go) and reconcileCheckJobs (this file's
// service.go) — not createCheckJobs (db/sqlite.go, db/postgres.go).

// TestReconcilePeriodEditRelevelsAllRegions verifies AC2: editing the
// check's period immediately re-levels scheduled_at for every region's job
// (D2 — the update path used to only touch period/config/plan_weight, never
// scheduled_at, which was bug F1).
func TestReconcilePeriodEditRelevelsAllRegions(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := t.Context()

	svc, dbSvc, org := newReconcileTestService(t)

	resp, err := svc.CreateCheck(ctx, org.Slug, checks.CreateCheckRequest{
		Type:    "http",
		Config:  map[string]any{"url": "https://example.com"},
		Regions: []string{"default", "eu-2"},
		Period:  strPtr("00:01:00"),
	})
	r.NoError(err)

	jobsBefore, err := dbSvc.ListCheckJobsByCheckUID(ctx, resp.UID)
	r.NoError(err)
	r.Len(jobsBefore, 2)

	// Corrupt one job's scheduled_at to simulate a drifted/unleveled state
	// (as F1 would produce in production before this fix).
	drifted := time.Now().Add(17 * time.Second)
	_, err = dbSvc.DB().NewUpdate().
		Model((*models.CheckJob)(nil)).
		Set("scheduled_at = ?", drifted).
		Set("effective_scheduled_at = ?", drifted).
		Where("uid = ?", jobsBefore[0].UID).
		Exec(ctx)
	r.NoError(err)

	newPeriod := "00:02:00"
	_, err = svc.UpdateCheck(ctx, org.Slug, resp.UID, &checks.UpdateCheckRequest{
		Period: &newPeriod,
	})
	r.NoError(err)

	jobsAfter, err := dbSvc.ListCheckJobsByCheckUID(ctx, resp.UID)
	r.NoError(err)
	r.Len(jobsAfter, 2)

	byRegion := jobsByRegion(t, jobsAfter)
	jobPeriod := 4 * time.Minute // 2 regions × 2m new base period
	phaseOf := func(cj *models.CheckJob) int64 {
		return cj.ScheduledAt.Unix() % int64(jobPeriod/time.Second)
	}

	diff := func(a, b int64) int64 {
		d := a - b
		if d < 0 {
			d += int64(jobPeriod / time.Second)
		}

		return d
	}

	r.InDelta(120, diff(phaseOf(byRegion["eu-2"]), phaseOf(byRegion["default"])), 1,
		"period edit must re-level both regions to the new phase, 2m apart")
}

// TestReconcileRegionAddedShiftsSurvivorsAndAddsNewSlot verifies D2: adding a
// region to an existing multi-region check re-levels the surviving jobs
// (their index shifts) and gives the new region its own phase-aligned slot.
func TestReconcileRegionAddedShiftsSurvivorsAndAddsNewSlot(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := t.Context()

	svc, dbSvc, org := newReconcileTestService(t)

	resp, err := svc.CreateCheck(ctx, org.Slug, checks.CreateCheckRequest{
		Type:    "http",
		Config:  map[string]any{"url": "https://example.com"},
		Regions: []string{"default", "us-1"}, // sorted: default(0), us-1(1)
		Period:  strPtr("00:01:00"),
	})
	r.NoError(err)

	newRegions := []string{"default", "eu-2", "us-1"} // sorted: default(0), eu-2(1), us-1(2)
	_, err = svc.UpdateCheck(ctx, org.Slug, resp.UID, &checks.UpdateCheckRequest{
		Regions: &newRegions,
	})
	r.NoError(err)

	jobsAfter, err := dbSvc.ListCheckJobsByCheckUID(ctx, resp.UID)
	r.NoError(err)
	r.Len(jobsAfter, 3, "3 jobs after adding eu-2")

	byRegion := jobsByRegion(t, jobsAfter)
	r.Contains(byRegion, "default")
	r.Contains(byRegion, "eu-2", "new region gets its own job")
	r.Contains(byRegion, "us-1")

	jobPeriod := 3 * time.Minute
	phaseOf := func(cj *models.CheckJob) int64 {
		return cj.ScheduledAt.Unix() % int64(jobPeriod/time.Second)
	}

	diff := func(a, b int64) int64 {
		d := a - b
		if d < 0 {
			d += int64(jobPeriod / time.Second)
		}

		return d
	}

	// us-1 shifted from index 1 (2-region set) to index 2 (3-region set) —
	// its phase relative to default must now be 2×basePeriod, proving the
	// survivor was re-leveled rather than left at its stale phase.
	r.InDelta(60, diff(phaseOf(byRegion["eu-2"]), phaseOf(byRegion["default"])), 1)
	r.InDelta(120, diff(phaseOf(byRegion["us-1"]), phaseOf(byRegion["default"])), 1)
}

// TestReconcileRegionRemovedRelevelsSurvivors verifies D2: removing a region
// re-levels the remaining regions' jobs to their shifted index.
func TestReconcileRegionRemovedRelevelsSurvivors(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := t.Context()

	svc, dbSvc, org := newReconcileTestService(t)

	resp, err := svc.CreateCheck(ctx, org.Slug, checks.CreateCheckRequest{
		Type:    "http",
		Config:  map[string]any{"url": "https://example.com"},
		Regions: []string{"default", "eu-2", "us-1"},
		Period:  strPtr("00:01:00"),
	})
	r.NoError(err)

	newRegions := []string{"default", "us-1"} // eu-2 removed
	_, err = svc.UpdateCheck(ctx, org.Slug, resp.UID, &checks.UpdateCheckRequest{
		Regions: &newRegions,
	})
	r.NoError(err)

	jobsAfter, err := dbSvc.ListCheckJobsByCheckUID(ctx, resp.UID)
	r.NoError(err)
	r.Len(jobsAfter, 2, "eu-2's job must be deleted")

	byRegion := jobsByRegion(t, jobsAfter)
	r.NotContains(byRegion, "eu-2")
	r.Contains(byRegion, "default")
	r.Contains(byRegion, "us-1")

	jobPeriod := 2 * time.Minute // 2 regions × 1m base period
	phaseOf := func(cj *models.CheckJob) int64 {
		return cj.ScheduledAt.Unix() % int64(jobPeriod/time.Second)
	}

	diff := func(a, b int64) int64 {
		d := a - b
		if d < 0 {
			d += int64(jobPeriod / time.Second)
		}

		return d
	}

	// us-1 shifted from index 2 (3-region set) to index 1 (2-region set).
	r.InDelta(60, diff(phaseOf(byRegion["us-1"]), phaseOf(byRegion["default"])), 1,
		"us-1 must be re-leveled to its new index-1 phase, not left at its stale index-2 phase")
}

// TestReconcileConfigOnlyEditDoesNotRewriteScheduledAt guards against
// over-triggering: an edit that touches neither period nor the region set
// (config-only) must not rewrite scheduled_at — reconcile should only
// re-level when something that actually affects phase changed.
func TestReconcileConfigOnlyEditDoesNotRewriteScheduledAt(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := t.Context()

	svc, dbSvc, org := newReconcileTestService(t)

	resp, err := svc.CreateCheck(ctx, org.Slug, checks.CreateCheckRequest{
		Type:    "http",
		Config:  map[string]any{"url": "https://example.com"},
		Regions: []string{"default", "eu-2"},
		Period:  strPtr("00:01:00"),
	})
	r.NoError(err)

	jobsBefore, err := dbSvc.ListCheckJobsByCheckUID(ctx, resp.UID)
	r.NoError(err)
	r.Len(jobsBefore, 2)

	beforeByUID := make(map[string]time.Time, len(jobsBefore))
	for _, j := range jobsBefore {
		beforeByUID[j.UID] = *j.ScheduledAt
	}

	// Same URL, different name: config unchanged, no period/region change.
	newName := "renamed-check"
	_, err = svc.UpdateCheck(ctx, org.Slug, resp.UID, &checks.UpdateCheckRequest{
		Name: &newName,
	})
	r.NoError(err)

	jobsAfter, err := dbSvc.ListCheckJobsByCheckUID(ctx, resp.UID)
	r.NoError(err)
	r.Len(jobsAfter, 2)

	for _, j := range jobsAfter {
		before, ok := beforeByUID[j.UID]
		r.True(ok, "job UIDs must be stable across an unrelated edit")
		r.True(before.Equal(*j.ScheduledAt),
			"scheduled_at must not change on an edit unrelated to period/regions/config/plan_weight")
	}
}

func strPtr(s string) *string { return &s }
