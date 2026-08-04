package workers_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkworker/checkjobsvc"
	"github.com/fclairamb/solidping/server/internal/checkworker/scheduling"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/handlers/incidents"
	"github.com/fclairamb/solidping/server/internal/handlers/workers"
	"github.com/fclairamb/solidping/server/internal/jobs/jobsvc"
	"github.com/fclairamb/solidping/server/internal/notifier"
	"github.com/fclairamb/solidping/server/internal/utils/clock"
)

// submitEnv is the fixture for the shared server-side submit path: a real
// sqlite DB, a real claim service, and the workers service wired with the same
// scheduling.Params the in-process worker would use.
type submitEnv struct {
	t         *testing.T
	dbSvc     *sqlite.Service
	svc       *workers.Service
	org       *models.Organization
	workerUID string
}

// submitParams is a fully-enabled scheduling parameter set, so a regression in
// the hoisted accounting is visible in every stored field.
func submitParams() scheduling.Params {
	return scheduling.Params{
		SlowThresholdMs:     1000,
		CheckTimeout:        30 * time.Second,
		TierCreditPerWeight: 2 * time.Second,
		TierCreditMax:       10 * time.Second,
		CostTimeoutFactor:   3,
		CostTimeoutFloor:    2 * time.Second,
		LaneSlowThresholdMs: 1200,
		LaneFastThresholdMs: 800,
	}
}

func newSubmitEnv(t *testing.T) *submitEnv {
	t.Helper()

	ctx := t.Context()
	r := require.New(t)

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	org := models.NewOrganization("acme", "Acme")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	events := notifier.NewLocalEventNotifier()
	t.Cleanup(func() { _ = events.Close() })

	checkJobSvc := checkjobsvc.NewService(dbSvc.DB())
	jobs := jobsvc.NewService(dbSvc.DB(), dbSvc, events, nil)
	svc := workers.NewService(
		dbSvc, checkJobSvc, incidents.NewService(dbSvc, jobs, clock.Real{}, nil), submitParams(),
	)

	worker, err := dbSvc.RegisterOrUpdateWorker(ctx, &models.Worker{Slug: "ag-test", Name: "agent:test"})
	r.NoError(err)

	return &submitEnv{t: t, dbSvc: dbSvc, svc: svc, org: org, workerUID: worker.UID}
}

// leasedJob creates a check and hands its materialized job row a lease held by
// the environment's worker, with a known pre-run scheduling state.
func (e *submitEnv) leasedJob(prevCost, prevDelay float64, lane uint8) *models.CheckJob {
	e.t.Helper()
	r := require.New(e.t)
	ctx := e.t.Context()

	check := models.NewCheck(e.org.UID, "web", "http")
	check.Config = models.JSONMap{"url": "https://example.com"}
	check.Regions = []string{"eu-west-1"}
	check.ConfirmationPeriodSeconds = 0
	r.NoError(e.dbSvc.CreateCheck(ctx, check))

	var job models.CheckJob
	r.NoError(e.dbSvc.DB().NewSelect().Model(&job).Where("check_uid = ?", check.UID).Scan(ctx))

	expires := time.Now().Add(time.Minute)
	scheduledAt := time.Now().Add(-5 * time.Second)

	_, err := e.dbSvc.DB().NewUpdate().
		Model((*models.CheckJob)(nil)).
		Set("lease_worker_uid = ?", e.workerUID).
		Set("lease_expires_at = ?", expires).
		Set("scheduled_at = ?", scheduledAt).
		Set("effective_scheduled_at = ?", scheduledAt).
		Set("cost_ewma_ms = ?", prevCost).
		Set("delay_ewma_ms = ?", prevDelay).
		Set("lane = ?", lane).
		Set("plan_weight = ?", 2).
		Where("uid = ?", job.UID).
		Exec(ctx)
	r.NoError(err)

	return e.reload(job.UID)
}

func (e *submitEnv) reload(jobUID string) *models.CheckJob {
	e.t.Helper()

	var job models.CheckJob
	require.NoError(e.t,
		e.dbSvc.DB().NewSelect().Model(&job).Where("uid = ?", jobUID).Scan(e.t.Context()))

	return &job
}

// TestSubmitResultPersistsSchedulingState is the accounting-parity guarantee:
// a probe result arriving over a remote transport must advance the SAME
// cost/delay EWMAs, effective deadline and lane the in-process worker would
// have persisted. Before this hoist the agent path released the lease and
// nothing else, so a shared cloud region served by platform agents would have
// degraded to unweighted FIFO with every job frozen in its creation lane.
func TestSubmitResultPersistsSchedulingState(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	e := newSubmitEnv(t)

	before := e.leasedJob(500, 100, 0)
	execStart := time.Now()

	resp, err := e.svc.SubmitResult(t.Context(), &workers.SubmitResultRequest{
		JobUID:    before.UID,
		WorkerUID: e.workerUID,
		Status:    int(models.ResultStatusUp),
		Duration:  2500, // ms
		ExecStart: &execStart,
		FromProbe: true,
	})
	r.NoError(err)

	after := e.reload(before.UID)

	want := submitParams().PostExec(&scheduling.PostExecInput{
		PrevCostEWMAMs:       before.CostEWMAMs,
		PrevDelayEWMAMs:      before.DelayEWMAMs,
		PrevLane:             before.Lane,
		PlanWeight:           before.PlanWeight,
		ScheduledAt:          before.ScheduledAt,
		EffectiveScheduledAt: before.EffectiveScheduledAt,
		DurationMs:           2500,
		TimedOut:             false,
		ExecStart:            &execStart,
		NextScheduledAt:      resp.NextScheduledAt,
	})

	r.InDelta(want.CostEWMAMs, after.CostEWMAMs, 1e-6)
	r.NotEqual(before.CostEWMAMs, after.CostEWMAMs, "the cost EWMA must actually move")
	r.InDelta(want.DelayEWMAMs, after.DelayEWMAMs, 1e-6)
	r.Equal(want.Lane, after.Lane)
	r.NotNil(after.EffectiveScheduledAt)
	r.WithinDuration(want.EffectiveScheduledAt, *after.EffectiveScheduledAt, time.Millisecond)
	r.Nil(after.LeaseWorkerUID, "the lease is still released")
}

// TestSubmitResultWithoutExecStartLeavesDelayEWMA: an executor predating the
// execStart wire field still folds its cost sample and lane, but contributes no
// delay sample — the stored telemetry is carried over, not decayed towards 0.
func TestSubmitResultWithoutExecStartLeavesDelayEWMA(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	e := newSubmitEnv(t)

	before := e.leasedJob(500, 777, 0)

	_, err := e.svc.SubmitResult(t.Context(), &workers.SubmitResultRequest{
		JobUID:    before.UID,
		WorkerUID: e.workerUID,
		Status:    int(models.ResultStatusUp),
		Duration:  2500,
		FromProbe: true,
	})
	r.NoError(err)

	after := e.reload(before.UID)
	r.InDelta(777.0, after.DelayEWMAMs, 1e-6, "no execStart means no delay sample")
	r.NotEqual(before.CostEWMAMs, after.CostEWMAMs, "the cost sample still lands")
}

// TestSubmitResultDispatchErrorLeavesAccountingUntouched: a dispatch-time error
// (undecryptable credentials, an unassemblable tunnel) never ran the probe, so
// its zero duration must NOT be folded into the cost EWMA — otherwise an
// expensive check would look cheap and flip lanes on every failed dispatch.
func TestSubmitResultDispatchErrorLeavesAccountingUntouched(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	e := newSubmitEnv(t)

	before := e.leasedJob(5000, 100, 1)

	_, err := e.svc.SubmitResult(t.Context(), &workers.SubmitResultRequest{
		JobUID:    before.UID,
		WorkerUID: e.workerUID,
		Status:    int(models.ResultStatusError),
		Output:    map[string]any{"error": "credentials cannot be decrypted"},
		FromProbe: false,
	})
	r.NoError(err)

	after := e.reload(before.UID)
	r.InDelta(before.CostEWMAMs, after.CostEWMAMs, 1e-6, "a non-probe result carries no cost sample")
	r.InDelta(before.DelayEWMAMs, after.DelayEWMAMs, 1e-6)
	r.Equal(before.Lane, after.Lane, "the lane must not flip on a dispatch error")
	r.Nil(after.LeaseWorkerUID, "the lease is still released")
}

// TestSubmitResultTimeoutIsPinnedToCeiling: the remote path applies the same
// timeout pin the in-process worker does.
func TestSubmitResultTimeoutIsPinnedToCeiling(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	e := newSubmitEnv(t)

	before := e.leasedJob(1000, 0, 0)

	_, err := e.svc.SubmitResult(t.Context(), &workers.SubmitResultRequest{
		JobUID:    before.UID,
		WorkerUID: e.workerUID,
		Status:    int(models.ResultStatusTimeout),
		Duration:  5, // truncated before the deadline fired
		FromProbe: true,
	})
	r.NoError(err)

	after := e.reload(before.UID)
	r.InDelta(
		scheduling.UpdateEWMA(1000, float64(submitParams().EffectiveCheckTimeout().Milliseconds())),
		after.CostEWMAMs, 1e-6)
	r.Greater(after.CostEWMAMs, before.CostEWMAMs)
}
