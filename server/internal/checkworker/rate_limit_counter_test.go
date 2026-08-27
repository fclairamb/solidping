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

	return newGateFixtureFor(t, perMinuteCap, false)
}

// newGateFixtureFor builds the fixture with control over the check's `internal`
// flag. The job carries its check the way a real claim does — checkjobsvc
// attaches it inside the claim transaction (attachChecks), which is the only
// reason the gate can see internal-ness without a column on check_jobs.
// TestClaimAttachesInternalCheck (checkjobsvc) is what proves that attachment
// actually happens, so this hand-attachment is a shortcut, not an assumption.
func newGateFixtureFor(t *testing.T, perMinuteCap *int, internal bool) *gateFixture {
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
	check.Internal = internal
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
	job.Check = check

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

// TestWorkerRateLimitGateExemptsInternalChecks is spec 2026-08-27-01 on the
// in-process path: an internal check is exempt from MaxChecks and absent from
// the org's demand figure, so it must not spend a MaxChecksPerMinute token
// either. The cap is pinned at 0 — the harshest possible bucket — so an
// unexempted job could not possibly get through, and the exemption is the only
// thing that can produce this result.
//
//nolint:paralleltest // shares the package's in-memory DB helper
func TestWorkerRateLimitGateExemptsInternalChecks(t *testing.T) {
	r := require.New(t)

	fixture := newGateFixtureFor(t, entitlements.Int(0), true)

	deferred, err := fixture.runner.applyRateLimitGate(t.Context(), slog.Default(), fixture.job)
	r.NoError(err)
	r.False(deferred, "an internal check must run even with the bucket fully drained")
	r.Zero(fixture.skipsToday(t),
		"an exempt check must not tick the org's skipped-today counter")

	// Repeat: the exemption is not a one-shot allowance that a refilling bucket
	// could explain.
	for range 3 {
		deferred, err = fixture.runner.applyRateLimitGate(t.Context(), slog.Default(), fixture.job)
		r.NoError(err)
		r.False(deferred)
	}
	r.Zero(fixture.skipsToday(t))
}

// TestWorkerRateLimitGateStillMetersNormalChecks is the positive control for the
// test above, run through the same fixture with the same drained bucket: the
// ONLY difference is the check's internal flag. Without this pair, an exemption
// bug that waved everything through would look identical.
//
//nolint:paralleltest // shares the package's in-memory DB helper
func TestWorkerRateLimitGateStillMetersNormalChecks(t *testing.T) {
	r := require.New(t)

	fixture := newGateFixtureFor(t, entitlements.Int(0), false)

	deferred, err := fixture.runner.applyRateLimitGate(t.Context(), slog.Default(), fixture.job)
	r.NoError(err)
	r.True(deferred, "a normal check is still turned away by a drained bucket")
	r.Equal(1, fixture.skipsToday(t), "and is still counted")
}

// TestWorkerRateLimitGateMetersAJobWithNoAttachedCheck pins the nil-Check
// direction: a job whose check row vanished between scheduling and claim is
// metered like a normal one. Exempting the unknown case would turn a deleted
// check into a free pass.
//
//nolint:paralleltest // shares the package's in-memory DB helper
func TestWorkerRateLimitGateMetersAJobWithNoAttachedCheck(t *testing.T) {
	r := require.New(t)

	fixture := newGateFixtureFor(t, entitlements.Int(0), true)
	fixture.job.Check = nil

	deferred, err := fixture.runner.applyRateLimitGate(t.Context(), slog.Default(), fixture.job)
	r.NoError(err)
	r.True(deferred)
	r.Equal(1, fixture.skipsToday(t))
}
