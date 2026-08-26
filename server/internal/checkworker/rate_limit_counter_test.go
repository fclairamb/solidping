package checkworker

import (
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/entitlements"
)

// gateFixture wires a CheckWorker to a real entitlements service backed by the
// same in-memory database, so the rate-limit gate under test writes the usage
// counter for real rather than against a stub.
type gateFixture struct {
	runner *CheckWorker
	dbSvc  *sqlite.Service
	org    *models.Organization
	job    *models.CheckJob
}

func newGateFixture(t *testing.T, perMinuteCap *int) *gateFixture {
	t.Helper()
	r := require.New(t)

	runner, dbSvc, ctx := setupTestRunner(t)
	t.Cleanup(func() { _ = dbSvc.Close() })

	org := models.NewOrganization("gate-org", "Gate Org")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	worker := models.NewWorker("gate-worker", "Gate Worker")
	_, err := dbSvc.DB().NewInsert().Model(worker).Exec(ctx)
	r.NoError(err)
	runner.setWorker(worker)

	entSvc := entitlements.NewService(
		dbSvc, entitlements.DefaultsFor(config.DeploymentModeSelfHosted), 0,
	)
	r.NoError(entSvc.Set(ctx, org.UID, entitlements.Entitlements{
		Source: models.EntitlementSourceAdmin,
		Limits: entitlements.Limits{MaxChecksPerMinute: perMinuteCap},
	}, "test", ""))
	runner.services.Entitlements = entSvc

	check := models.NewCheck(org.UID, "gate-check", "http")
	r.NoError(dbSvc.CreateCheck(ctx, check))

	job := new(models.CheckJob)
	r.NoError(dbSvc.DB().NewSelect().
		Model(job).
		Where("check_uid = ?", check.UID).
		Scan(ctx))

	scheduledAt := time.Now().Add(-10 * time.Second)
	_, err = dbSvc.DB().NewUpdate().
		Model((*models.CheckJob)(nil)).
		Set("scheduled_at = ?", scheduledAt).
		Set("effective_scheduled_at = ?", scheduledAt).
		Set("lease_worker_uid = ?", worker.UID).
		Where("uid = ?", job.UID).
		Exec(ctx)
	r.NoError(err)

	job.ScheduledAt = &scheduledAt
	job.EffectiveScheduledAt = &scheduledAt
	job.LeaseWorkerUID = &worker.UID

	return &gateFixture{runner: runner, dbSvc: dbSvc, org: org, job: job}
}

// release re-takes the lease the previous deferral dropped, so the fixture can
// drive a second window without re-running the whole claim path.
func (f *gateFixture) release(t *testing.T) {
	t.Helper()

	_, err := f.dbSvc.DB().NewUpdate().
		Model((*models.CheckJob)(nil)).
		Set("lease_worker_uid = ?", *f.job.LeaseWorkerUID).
		Where("uid = ?", f.job.UID).
		Exec(t.Context())
	require.NoError(t, err)
}

// skipsToday reads the daily rate-limited-skip counter for the fixture's org.
func (f *gateFixture) skipsToday(t *testing.T) int {
	t.Helper()

	count, err := f.dbSvc.GetMonthlyUsage(
		t.Context(), f.org.UID, models.UsageCounterKindCheckRateLimited,
		time.Now().UTC().Format("2006-01-02"),
	)
	require.NoError(t, err)

	return count
}

// TestWorkerRateLimitGateCountsTheSkip covers the in-process worker half of
// spec 2026-08-26-03: when the per-org bucket is drained, the deferral must
// also leave a customer-visible trace. Before this, the only traces were an
// INFO log and a Prometheus counter — neither of which reaches the org whose
// results have gaps.
//
// The cap is pinned at 0 so the bucket is drained deterministically, with no
// dependence on refill timing.
//
//nolint:paralleltest // shares the package's in-memory DB helper
func TestWorkerRateLimitGateCountsTheSkip(t *testing.T) {
	r := require.New(t)

	fixture := newGateFixture(t, entitlements.Int(0))
	ctx := t.Context()

	r.Zero(fixture.skipsToday(t), "nothing skipped before the gate runs")

	deferred, err := fixture.runner.applyRateLimitGate(ctx, slog.Default(), fixture.job)
	r.NoError(err)
	r.True(deferred, "a drained bucket must turn the execution away")
	r.Equal(1, fixture.skipsToday(t), "the deferral must be counted for the org")

	// Every skip counts, not just the first: the banner reports a volume.
	fixture.release(t)
	deferred, err = fixture.runner.applyRateLimitGate(ctx, slog.Default(), fixture.job)
	r.NoError(err)
	r.True(deferred)
	r.Equal(2, fixture.skipsToday(t))
}

// TestWorkerRateLimitGateCountsNothingWhenUnderCap is the positive control for
// the test above: a check that is allowed to run must leave the counter alone,
// or the banner would light up for every healthy org.
//
//nolint:paralleltest // shares the package's in-memory DB helper
func TestWorkerRateLimitGateCountsNothingWhenUnderCap(t *testing.T) {
	r := require.New(t)

	fixture := newGateFixture(t, entitlements.Int(600))

	deferred, err := fixture.runner.applyRateLimitGate(t.Context(), slog.Default(), fixture.job)
	r.NoError(err)
	r.False(deferred, "an org inside its cap must be allowed to execute")
	r.Zero(fixture.skipsToday(t), "an execution that ran is not a skip")
}

// TestWorkerRateLimitGateWithoutEntitlementsService covers agent mode, where
// there is no in-process entitlements service at all: the gate must wave the
// check through rather than panic or defer everything.
//
//nolint:paralleltest // shares the package's in-memory DB helper
func TestWorkerRateLimitGateWithoutEntitlementsService(t *testing.T) {
	r := require.New(t)

	fixture := newGateFixture(t, entitlements.Int(0))
	fixture.runner.services.Entitlements = nil

	deferred, err := fixture.runner.applyRateLimitGate(t.Context(), slog.Default(), fixture.job)
	r.NoError(err)
	r.False(deferred)
	r.Zero(fixture.skipsToday(t))
}
