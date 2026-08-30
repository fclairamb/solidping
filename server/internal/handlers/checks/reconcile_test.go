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
	"github.com/fclairamb/solidping/server/internal/utils/timeutils"
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
// createCheckJobs, which materializes the first job per region) now creates
// each region's job at the FULL check period and staggers the first
// scheduled_at by the inter-region spread (spec 2026-07-20-05) — but it is
// still NOT phase-aligned by D1/D2: it deliberately keeps pinning region 0's
// very first scheduled_at to literal time.Now() so the check.created
// express-runner path (checkjobsvc.ClaimJobsForCheck, which gates on
// "scheduled_at <= now" with no claim-ahead window) can still claim and
// execute it immediately, guaranteeing a fast first result after check
// creation. Jittering that very first tick (as NextAligned would) was tried
// and reverted during implementation: it pushed region 0's first scheduled_at
// a full period into the future, starving the express path. Deterministic
// phase leveling for a fresh multi-region check therefore only takes full
// effect starting from its first reconcile (any subsequent edit — see
// TestReconcilePeriodEditRelevelsAllRegions etc. below) or from its first
// organic release cycle (calculateNextScheduledAt in worker.go, covered by
// TestCalculateNextScheduledAt_PhaseLocked in worker_test.go).

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

	// Every region now runs at the FULL new period (2m) — no basePeriod×n
	// split — with an inter-region spread of period/n = 2m/2 = 1m.
	jobPeriod := 2 * time.Minute
	for _, j := range jobsAfter {
		r.Equal(jobPeriod, time.Duration(j.Period), "each region's job runs at the full period, not the split period")
	}

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

	r.InDelta(60, diff(phaseOf(byRegion["eu-2"]), phaseOf(byRegion["default"])), 1,
		"period edit must re-level both regions one spread (period/n = 1m) apart")
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

	// Full period per region (1m, not the old 3m split); spread = 1m/3 = 20s.
	jobPeriod := time.Minute
	for _, j := range jobsAfter {
		r.Equal(jobPeriod, time.Duration(j.Period), "each region's job runs at the full period, not the split period")
	}

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
	// its phase relative to default must now be 2×spread (40s), proving the
	// survivor was re-leveled rather than left at its stale phase.
	r.InDelta(20, diff(phaseOf(byRegion["eu-2"]), phaseOf(byRegion["default"])), 1)
	r.InDelta(40, diff(phaseOf(byRegion["us-1"]), phaseOf(byRegion["default"])), 1)
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

	// Full period per region (1m); spread after removal = 1m/2 = 30s.
	jobPeriod := time.Minute
	for _, j := range jobsAfter {
		r.Equal(jobPeriod, time.Duration(j.Period), "each region's job runs at the full period, not the split period")
	}

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

	// us-1 shifted from index 2 (3-region set) to index 1 (2-region set) — its
	// phase relative to default must now be one spread (30s), proving it was
	// re-leveled rather than left at its stale index-2 phase.
	r.InDelta(30, diff(phaseOf(byRegion["us-1"]), phaseOf(byRegion["default"])), 1,
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

// TestReconcileRegionSpreadOverrideApplied verifies that a regionSpread
// override (spec 2026-07-20-05) is persisted and drives the per-region phase
// leveling: three regions with a 5s spread land 5s apart instead of the
// default period/n = 20s.
func TestReconcileRegionSpreadOverrideApplied(t *testing.T) {
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

	// Set a 5s override via update — this triggers a reconcile that re-levels
	// every region's phase deterministically (NextAligned, sorted index).
	updated, err := svc.UpdateCheck(ctx, org.Slug, resp.UID, &checks.UpdateCheckRequest{
		RegionSpread: strPtr("00:00:05"),
	})
	r.NoError(err)
	r.NotNil(updated.RegionSpread, "response echoes the persisted regionSpread")
	r.Equal("00:00:05", *updated.RegionSpread)

	jobsAfter, err := dbSvc.ListCheckJobsByCheckUID(ctx, resp.UID)
	r.NoError(err)
	r.Len(jobsAfter, 3)

	byRegion := jobsByRegion(t, jobsAfter)
	jobPeriod := time.Minute
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

	r.InDelta(5, diff(phaseOf(byRegion["eu-2"]), phaseOf(byRegion["default"])), 1,
		"eu-2 is one 5s override-spread after default")
	r.InDelta(10, diff(phaseOf(byRegion["us-1"]), phaseOf(byRegion["default"])), 1,
		"us-1 is two 5s override-spreads after default")

	// Clearing it (explicit empty string) reverts to the default and persists.
	cleared, err := svc.UpdateCheck(ctx, org.Slug, resp.UID, &checks.UpdateCheckRequest{
		RegionSpread: strPtr(""),
	})
	r.NoError(err)
	r.Nil(cleared.RegionSpread, "empty string clears the override back to the default")
}

// TestRegionSpreadValidation verifies the 0 <= regionSpread < period bound on
// both create and update (spec 2026-07-20-05).
func TestRegionSpreadValidation(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := t.Context()

	svc, _, org := newReconcileTestService(t)

	// Create: spread == period is rejected (must be strictly less).
	_, err := svc.CreateCheck(ctx, org.Slug, checks.CreateCheckRequest{
		Type:         "http",
		Config:       map[string]any{"url": "https://example.com"},
		Regions:      []string{"default", "eu-2"},
		Period:       strPtr("00:01:00"),
		RegionSpread: strPtr("00:01:00"),
	})
	r.Error(err)
	r.Contains(err.Error(), "regionSpread")

	// Create: spread > period is rejected.
	_, err = svc.CreateCheck(ctx, org.Slug, checks.CreateCheckRequest{
		Type:         "http",
		Config:       map[string]any{"url": "https://example.com"},
		Regions:      []string{"default", "eu-2"},
		Period:       strPtr("00:01:00"),
		RegionSpread: strPtr("00:02:00"),
	})
	r.Error(err)

	// Create: negative spread is rejected.
	_, err = svc.CreateCheck(ctx, org.Slug, checks.CreateCheckRequest{
		Type:         "http",
		Config:       map[string]any{"url": "https://example.com"},
		Regions:      []string{"default", "eu-2"},
		Period:       strPtr("00:01:00"),
		RegionSpread: strPtr("-5s"),
	})
	r.Error(err)

	// Create: a valid spread (and 0 = fire together) is accepted and persisted.
	resp, err := svc.CreateCheck(ctx, org.Slug, checks.CreateCheckRequest{
		Type:         "http",
		Config:       map[string]any{"url": "https://example.com"},
		Regions:      []string{"default", "eu-2"},
		Period:       strPtr("00:01:00"),
		RegionSpread: strPtr("00:00:10"),
	})
	r.NoError(err)
	r.NotNil(resp.RegionSpread)
	r.Equal("00:00:10", *resp.RegionSpread)

	// Update: spread >= the effective period is rejected.
	_, err = svc.UpdateCheck(ctx, org.Slug, resp.UID, &checks.UpdateCheckRequest{
		RegionSpread: strPtr("00:01:30"),
	})
	r.Error(err)
	r.Contains(err.Error(), "regionSpread")
}

// TestStartupReconcileFixesStaleSplitPeriodJobs verifies the one-shot startup
// pass (spec 2026-07-20-05): a multi-region check whose jobs still carry the
// old basePeriod×n split period is re-leveled to the full per-region period,
// and re-running the pass is a no-op (idempotent).
func TestStartupReconcileFixesStaleSplitPeriodJobs(t *testing.T) {
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

	// Simulate pre-spec rows: rewrite every job to the old 3m split period.
	stale := timeutils.Duration(3 * time.Minute)
	_, err = dbSvc.DB().NewUpdate().
		Model((*models.CheckJob)(nil)).
		Set("period = ?", stale).
		Where("check_uid = ?", resp.UID).
		Exec(ctx)
	r.NoError(err)

	// First pass: finds and fixes the one stale check.
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

	// Second pass: nothing stale remains — idempotent.
	n, err = svc.ReconcileStaleJobSchedules(ctx)
	r.NoError(err)
	r.Zero(n, "startup reconcile must be idempotent")
}

func strPtr(s string) *string { return &s }

// TestReconcileTypeOnlyConfigEditReachesCheckJobs is the end-to-end regression
// test for spec 2026-08-29-09: a PATCH that changes only the *types* of config
// values must still be copied onto the denormalized check_jobs.config.
//
// The production incident, replayed here through the public API: a check's
// expectedStatusCodes were patched to JSON numbers (the HTTP checker requires
// strings, so every run failed to parse), then patched back to strings. The
// check's own config column was correct both times, but configEqual compared
// values with fmt.Sprintf("%v", …) — which renders [200 403] identically for
// numbers and strings — so needsUpdate stayed false and the job kept
// dispatching the broken snapshot forever. Only disable/enable (which deletes
// and recreates the jobs) could unstick it.
//
// The same edits must NOT move scheduled_at: config is dispatch payload, not
// schedule input, so syncing it must never drift when the check runs.
func TestReconcileTypeOnlyConfigEditReachesCheckJobs(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := t.Context()

	svc, dbSvc, org := newReconcileTestService(t)

	resp, err := svc.CreateCheck(ctx, org.Slug, checks.CreateCheckRequest{
		Type: "http",
		Config: map[string]any{
			"url":                 "https://acme.com",
			"expectedStatusCodes": []any{"200", "403"},
		},
		Regions: []string{"default", "eu-2"},
		Period:  strPtr("00:01:00"),
	})
	r.NoError(err)

	jobsBefore, err := dbSvc.ListCheckJobsByCheckUID(ctx, resp.UID)
	r.NoError(err)
	r.Len(jobsBefore, 2)

	scheduleBefore := make(map[string]time.Time, len(jobsBefore))
	for _, j := range jobsBefore {
		r.NotNil(j.ScheduledAt)
		scheduleBefore[j.UID] = *j.ScheduledAt
	}

	// Step 1 — break it exactly as production did: same two status codes, now
	// as JSON numbers. Nothing but the value types changed.
	numeric := map[string]any{"url": "https://acme.com", "expectedStatusCodes": []any{200, 403}}
	_, err = svc.UpdateCheck(ctx, org.Slug, resp.UID, &checks.UpdateCheckRequest{Config: &numeric})
	r.NoError(err)

	jobsNumeric, err := dbSvc.ListCheckJobsByCheckUID(ctx, resp.UID)
	r.NoError(err)
	r.Len(jobsNumeric, 2)

	for _, j := range jobsNumeric {
		r.Equal([]any{float64(200), float64(403)}, j.Config["expectedStatusCodes"],
			"a type-only config edit must reach check_jobs.config")
		r.NotNil(j.ScheduledAt)
		r.True(scheduleBefore[j.UID].Equal(*j.ScheduledAt),
			"a config-only edit must not re-level the job's schedule")
	}

	// Step 2 — the fix the operator applied, and the edit that used to be
	// swallowed: back to strings, again a pure type change.
	stringed := map[string]any{"url": "https://acme.com", "expectedStatusCodes": []any{"200", "403"}}
	_, err = svc.UpdateCheck(ctx, org.Slug, resp.UID, &checks.UpdateCheckRequest{Config: &stringed})
	r.NoError(err)

	jobsFixed, err := dbSvc.ListCheckJobsByCheckUID(ctx, resp.UID)
	r.NoError(err)
	r.Len(jobsFixed, 2)

	for _, j := range jobsFixed {
		r.Equal([]any{"200", "403"}, j.Config["expectedStatusCodes"],
			"the repair PATCH must reach check_jobs.config — this is the bug")
		r.Equal("https://acme.com", j.Config["url"], "the merged config's other keys survive")
		r.NotNil(j.ScheduledAt)
		r.True(scheduleBefore[j.UID].Equal(*j.ScheduledAt),
			"a config-only edit must not re-level the job's schedule")
	}
}

// TestReconcileNoOpConfigEditTouchesNothing is the positive control for the
// test above: a comparison that simply always reported "different" would fix
// the regression while rewriting every job row on every edit. A PATCH that
// re-sends the identical config must still short-circuit — no schedule reset,
// and no write at all (updated_at unchanged).
func TestReconcileNoOpConfigEditTouchesNothing(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := t.Context()

	svc, dbSvc, org := newReconcileTestService(t)

	config := map[string]any{
		"url":                 "https://acme.com",
		"expectedStatusCodes": []any{"200", "403"},
		"headers":             map[string]any{"X-Acme": "1"},
	}

	resp, err := svc.CreateCheck(ctx, org.Slug, checks.CreateCheckRequest{
		Type:    "http",
		Config:  config,
		Regions: []string{"default", "eu-2"},
		Period:  strPtr("00:01:00"),
	})
	r.NoError(err)

	jobsBefore, err := dbSvc.ListCheckJobsByCheckUID(ctx, resp.UID)
	r.NoError(err)
	r.Len(jobsBefore, 2)

	type snapshot struct {
		scheduledAt time.Time
		updatedAt   time.Time
	}

	before := make(map[string]snapshot, len(jobsBefore))
	for _, j := range jobsBefore {
		r.NotNil(j.ScheduledAt)
		before[j.UID] = snapshot{scheduledAt: *j.ScheduledAt, updatedAt: j.UpdatedAt}
	}

	same := map[string]any{
		"url":                 "https://acme.com",
		"expectedStatusCodes": []any{"200", "403"},
		"headers":             map[string]any{"X-Acme": "1"},
	}
	_, err = svc.UpdateCheck(ctx, org.Slug, resp.UID, &checks.UpdateCheckRequest{Config: &same})
	r.NoError(err)

	jobsAfter, err := dbSvc.ListCheckJobsByCheckUID(ctx, resp.UID)
	r.NoError(err)
	r.Len(jobsAfter, 2)

	for _, j := range jobsAfter {
		snap, ok := before[j.UID]
		r.True(ok, "job UIDs must be stable across a no-op edit")
		r.NotNil(j.ScheduledAt)
		r.True(snap.scheduledAt.Equal(*j.ScheduledAt), "a no-op config edit must not re-level the schedule")
		r.True(snap.updatedAt.Equal(j.UpdatedAt), "a no-op config edit must not write the job row at all")
	}
}
