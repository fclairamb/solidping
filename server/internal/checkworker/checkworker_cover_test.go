package checkworker

import (
	"context"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/checkworker/checkjobsvc"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/stats"
)

func TestWorkerChannelCollectorCollect(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	runner, dbSvc, _ := setupTestRunner(t)
	t.Cleanup(func() { _ = dbSvc.Close() })
	runner.setWorker(models.NewWorker("collector-worker", "Collector"))
	runner.busySlow.Store(3)

	collector := newWorkerChannelCollector(runner)

	reg := prometheus.NewPedanticRegistry()
	r.NoError(reg.Register(collector))

	families, err := reg.Gather()
	r.NoError(err)
	r.Len(families, 2)

	values := map[string]float64{}
	for _, fam := range families {
		values[fam.GetName()] = fam.GetMetric()[0].GetGauge().GetValue()
	}
	r.InDelta(0, values["solidping_worker_jobs_channel_depth"], 0)
	r.InDelta(3, values["solidping_worker_busy_slow"], 0)
	r.Equal(2, testutil.CollectAndCount(collector))
}

func TestPassiveEvaluatorNextWait(t *testing.T) {
	t.Parallel()

	evaluator := &PassiveEvaluator{batch: 10, poll: 10 * time.Second}

	tests := []struct {
		name      string
		evaluated int
		nextIn    time.Duration
		err       error
		want      time.Duration
	}{
		{"error falls back to poll", 0, time.Second, context.DeadlineExceeded, 10 * time.Second},
		{"full batch loops at once", 10, time.Second, nil, 0},
		{"next tick sooner than poll", 2, 3 * time.Second, nil, 3 * time.Second},
		{"next tick later than poll", 2, time.Minute, nil, 10 * time.Second},
		{"unknown next tick", 0, 0, nil, 10 * time.Second},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tt.want, evaluator.nextWait(tt.evaluated, tt.nextIn, tt.err))
		})
	}
}

func TestPassiveEvaluatorRunRegistersAndStops(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	env, _ := newPassiveEvalEnv(t)
	check, _ := env.passiveCheck(t, checkerdef.CheckTypeHeartbeat)
	env.age(t, check, time.Hour)

	evaluator := env.evaluator(t, "run-node")
	evaluator.worker.Store(nil)

	ctx, cancel := context.WithCancel(env.ctx)
	done := make(chan error, 1)

	go func() { done <- evaluator.Run(ctx) }()

	// Wait for the first pass to write the overdue evaluation.
	r.Eventually(func() bool { return len(env.evaluations(t, check.UID)) > 0 },
		10*time.Second, 20*time.Millisecond)

	cancel()

	select {
	case err := <-done:
		r.ErrorIs(err, context.Canceled)
	case <-time.After(10 * time.Second):
		r.Fail("Run did not stop")
	}
	r.NotNil(evaluator.worker.Load())
}

func TestPassiveEvaluatorRunOnceNotRegistered(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	env, _ := newPassiveEvalEnv(t)
	evaluator := env.evaluator(t, "unreg-node")
	evaluator.worker.Store(nil)

	_, _, err := evaluator.RunOnce(env.ctx)
	r.ErrorIs(err, errPassiveEvaluatorNotRegistered)
}

func TestPassiveEvaluatorReleaseUnevaluated(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	env, _ := newPassiveEvalEnv(t)
	check, _ := env.passiveCheck(t, checkerdef.CheckTypeHeartbeat)
	env.makeDue(t, check)

	evaluator := env.evaluator(t, "release-node")
	workerUID := evaluator.worker.Load().UID

	claimed, _, err := env.svc.CheckJobs.ClaimPassiveJobs(env.ctx, workerUID, 10)
	r.NoError(err)
	r.Len(claimed, 1)

	// A job without ScheduledAt exercises the "now" fallback too.
	noSchedule := *claimed[0]
	noSchedule.ScheduledAt = nil

	canceled, cancel := context.WithCancel(env.ctx)
	cancel()

	evaluator.releaseUnevaluated(canceled, []*models.CheckJob{claimed[0], &noSchedule}, workerUID)

	// Released: claimable again straight away.
	again, _, err := env.svc.CheckJobs.ClaimPassiveJobs(env.ctx, workerUID, 10)
	r.NoError(err)
	r.Len(again, 1)
}

func TestPassiveEvaluatorRunOnceCanceledReleasesClaims(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	env, _ := newPassiveEvalEnv(t)
	check, _ := env.passiveCheck(t, checkerdef.CheckTypeHeartbeat)
	env.age(t, check, time.Hour)

	evaluator := env.evaluator(t, "cancel-node")

	// Claim with a live context, then evaluate with a dead one: simulate by
	// canceling after the claim through a wrapping jobs service.
	ctx, cancel := context.WithCancel(env.ctx)
	evaluator.jobs = &cancelAfterClaim{Service: evaluator.jobs, cancel: cancel}

	count, _, err := evaluator.RunOnce(ctx)
	r.ErrorIs(err, context.Canceled)
	r.Equal(1, count)
	r.Empty(env.evaluations(t, check.UID))
}

func TestCheckWorkerSelfStats(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	runner, dbSvc, ctx := setupTestRunner(t)
	t.Cleanup(func() { _ = dbSvc.Close() })

	// Default org is created by Initialize only in some modes; ensure it.
	if _, err := dbSvc.GetOrganizationBySlug(ctx, "default"); err != nil {
		r.NoError(dbSvc.CreateOrganization(ctx, models.NewOrganization("default", "")))
	}

	region := "eu-stats"
	worker := models.NewWorker("stats-worker", "Stats Worker")
	worker.Region = &region
	_, err := dbSvc.DB().NewInsert().Model(worker).Exec(ctx)
	r.NoError(err)
	runner.setWorker(worker)

	r.NoError(runner.setupSelfStats(ctx))
	r.NotEmpty(runner.internalCheckUID)
	firstUID := runner.internalCheckUID

	// Second call reuses the existing internal check.
	r.NoError(runner.createInternalCheck(ctx))
	r.Equal(firstUID, runner.internalCheckUID)

	// Legacy check (not internal) gets repaired.
	_, err = dbSvc.DB().NewUpdate().Model((*models.Check)(nil)).
		Set("internal = ?", false).Where("uid = ?", firstUID).Exec(ctx)
	r.NoError(err)
	r.NoError(runner.createInternalCheck(ctx))

	tests := []struct {
		name     string
		reported stats.ReportedStats
		want     models.ResultStatus
	}{
		{"all failed", stats.ReportedStats{TotalChecks: 2, FailedChecks: 2}, models.ResultStatusDown},
		{"no checks", stats.ReportedStats{}, models.ResultStatusDown},
		{"some ok", stats.ReportedStats{TotalChecks: 3, FailedChecks: 1, FreeRunners: 2}, models.ResultStatusUp},
	}
	for _, tt := range tests {
		runner.reportStats(tt.reported)
	}

	var rows []*models.Result
	r.NoError(dbSvc.DB().NewSelect().Model(&rows).
		Where("check_uid = ?", firstUID).Order("created_at ASC").Scan(ctx))
	r.GreaterOrEqual(len(rows), len(tests))

	counts := map[int]int{}
	for _, row := range rows {
		counts[*row.Status]++
	}
	r.GreaterOrEqual(counts[int(models.ResultStatusDown)], 2)
	r.GreaterOrEqual(counts[int(models.ResultStatusUp)], 1)
}

func TestCheckWorkerSetupSelfStatsNoDefaultOrg(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	runner, dbSvc, ctx := setupTestRunner(t)
	t.Cleanup(func() { _ = dbSvc.Close() })
	runner.setWorker(models.NewWorker("no-org", "No Org"))

	if _, err := dbSvc.GetOrganizationBySlug(ctx, "default"); err == nil {
		t.Skip("default org seeded; nothing to assert")
	}

	r.Error(runner.setupSelfStats(ctx))
}

// cancelAfterClaim cancels the context right after a successful claim.
type cancelAfterClaim struct {
	checkjobsvc.Service
	cancel context.CancelFunc
}

func (c *cancelAfterClaim) ClaimPassiveJobs(
	ctx context.Context, workerUID string, limit int,
) ([]*models.CheckJob, time.Duration, error) {
	jobs, next, err := c.Service.ClaimPassiveJobs(ctx, workerUID, limit)
	c.cancel()

	return jobs, next, err
}
