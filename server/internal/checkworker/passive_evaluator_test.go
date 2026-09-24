package checkworker

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/app/services"
	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/checkworker/checkjobsvc"
	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/handlers/checks"
	"github.com/fclairamb/solidping/server/internal/handlers/heartbeat"
	"github.com/fclairamb/solidping/server/internal/notifier"
)

// Spec 2026-09-25-04: passive checks (heartbeat, email) are evaluated by the
// jobs node's PassiveEvaluator, from one NULL-region job, with no check worker
// and no region involved. No test in this file starts a CheckWorker: the point
// of several of them is that none is needed.

// passiveEvalEnv is one in-memory database plus the services every evaluator
// in a test shares.
type passiveEvalEnv struct {
	ctx context.Context //nolint:containedctx // the test's own context, shared by its helpers
	db  *sqlite.Service
	svc *services.Registry
	org *models.Organization
}

func newPassiveEvalEnv(t *testing.T) (*passiveEvalEnv, context.Context) {
	t.Helper()

	ctx := t.Context()

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	require.NoError(t, err)
	require.NoError(t, dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	registry := services.NewRegistry()
	registry.CheckJobs = checkjobsvc.NewService(dbSvc.DB())

	events := notifier.NewLocalEventNotifier()
	t.Cleanup(func() { _ = events.Close() })
	registry.EventNotifier = events

	org := models.NewOrganization("passive-eval-org", "")
	require.NoError(t, dbSvc.CreateOrganization(ctx, org))

	return &passiveEvalEnv{ctx: ctx, db: dbSvc, svc: registry, org: org}, ctx
}

// evaluator builds and registers one jobs node's evaluator. nodeName is its
// SP_NODE_NAME, so two evaluators in one test are two jobs nodes.
func (env *passiveEvalEnv) evaluator(t *testing.T, nodeName string) *PassiveEvaluator {
	t.Helper()

	ctx := env.ctx

	cfg := &config.Config{Node: config.NodeConfig{Name: nodeName}}
	evaluator := NewPassiveEvaluator(env.db, cfg, env.svc, env.svc.CheckJobs)
	require.NoError(t, evaluator.register(ctx))

	return evaluator
}

// passiveCheck creates a passive check the way the API would store it, with a
// region list that must be dropped, and returns it with its one job.
func (env *passiveEvalEnv) passiveCheck(
	t *testing.T, checkType checkerdef.CheckType,
) (*models.Check, *models.CheckJob) {
	t.Helper()

	ctx := env.ctx

	check := models.NewCheck(env.org.UID, "passive-"+uuid.New().String()[:8], string(checkType))
	check.Config = models.JSONMap{"token": "tok-" + uuid.NewString()}
	check.Regions = []string{"gravelines"}
	require.NoError(t, env.db.CreateCheck(ctx, check))

	jobs, err := env.db.ListCheckJobsByCheckUID(ctx, check.UID)
	require.NoError(t, err)
	require.Len(t, jobs, 1, "a passive check owns exactly one job")
	require.Nil(t, jobs[0].Region, "and it has no region")

	return check, jobs[0]
}

// age backdates a check's creation (past the first-signal grace) and makes
// its job due now.
func (env *passiveEvalEnv) age(t *testing.T, check *models.Check, by time.Duration) {
	t.Helper()

	ctx := env.ctx

	createdAt := time.Now().Add(-by)
	_, err := env.db.DB().NewUpdate().Model((*models.Check)(nil)).
		Set("created_at = ?", createdAt).Where("uid = ?", check.UID).Exec(ctx)
	require.NoError(t, err)

	env.makeDue(t, check)
}

func (env *passiveEvalEnv) makeDue(t *testing.T, check *models.Check) {
	t.Helper()

	ctx := env.ctx

	due := time.Now().Add(-time.Second)
	_, err := env.db.DB().NewUpdate().Model((*models.CheckJob)(nil)).
		Set("scheduled_at = ?", due).
		Set("effective_scheduled_at = ?", due).
		Where("check_uid = ?", check.UID).
		Exec(ctx)
	require.NoError(t, err)
}

// signal inserts an inbound signal row (what the heartbeat/email ingest
// writes: no worker, no region).
func (env *passiveEvalEnv) signal(
	t *testing.T, check *models.Check, status models.ResultStatus, at time.Time,
) *models.Result {
	t.Helper()

	ctx := env.ctx

	row := models.NewResult(env.org.UID, check.UID, status, 0)
	row.PeriodStart = at
	row.Output = models.JSONMap{"message": "Heartbeat received"}
	require.NoError(t, env.db.CreateResult(ctx, row))

	return row
}

// evaluations returns the evaluation rows written for a check, oldest first.
func (env *passiveEvalEnv) evaluations(t *testing.T, checkUID string) []*models.Result {
	t.Helper()

	ctx := env.ctx

	var rows []*models.Result
	require.NoError(t, env.db.DB().NewSelect().Model(&rows).
		Where("check_uid = ?", checkUID).
		Where("worker_uid IS NOT NULL").
		Order("period_start ASC").
		Scan(ctx))

	return rows
}

func (env *passiveEvalEnv) check(t *testing.T, checkUID string) *models.Check {
	t.Helper()

	ctx := env.ctx

	check, err := env.db.GetCheck(ctx, env.org.UID, checkUID)
	require.NoError(t, err)

	return check
}

func (env *passiveEvalEnv) incidentCount(t *testing.T, checkUID string) int {
	t.Helper()

	ctx := env.ctx

	count, err := env.db.DB().NewSelect().Model((*models.Incident)(nil)).
		Where("check_uid = ?", checkUID).Count(ctx)
	require.NoError(t, err)

	return count
}

func (env *passiveEvalEnv) job(t *testing.T, checkUID string) *models.CheckJob {
	t.Helper()

	ctx := env.ctx

	jobs, err := env.db.ListCheckJobsByCheckUID(ctx, checkUID)
	require.NoError(t, err)
	require.Len(t, jobs, 1)

	return jobs[0]
}

// TestPassiveEvaluatorOverdueGoesDownWithoutAnyCheckWorker is the 2026-09-24
// `lauterbourg` case: every regional check worker is gone (none is started
// here), and an overdue heartbeat must still go Down. Email checks take the
// exact same path.
func TestPassiveEvaluatorOverdueGoesDownWithoutAnyCheckWorker(t *testing.T) {
	t.Parallel()

	for _, checkType := range []checkerdef.CheckType{checkerdef.CheckTypeHeartbeat, checkerdef.CheckTypeEmail} {
		t.Run(string(checkType), func(t *testing.T) {
			t.Parallel()
			r := require.New(t)

			env, ctx := newPassiveEvalEnv(t)
			evaluator := env.evaluator(t, "jobs-node-a")

			check, _ := env.passiveCheck(t, checkType)
			r.Empty(check.Regions, "the region list was dropped")

			env.age(t, check, time.Hour)
			beat := env.signal(t, check, models.ResultStatusUp, time.Now().Add(-3*time.Minute))

			evaluated, _, err := evaluator.RunOnce(ctx)
			r.NoError(err)
			r.Equal(1, evaluated)

			rows := env.evaluations(t, check.UID)
			r.Len(rows, 1)

			row := rows[0]
			r.Equal(int(models.ResultStatusDown), *row.Status)
			r.Contains(row.Output[outputKeyMessage], "overdue")
			r.Equal(true, row.Output[outputKeyEvaluation])
			r.Equal(beat.UID, row.Output[outputKeyLastSignalResultUID])
			r.Nil(row.Region, "an evaluation ran nowhere: no region")
			r.Equal(evaluator.worker.Load().UID, *row.WorkerUID, "written by the jobs node's own workers row")

			refreshed := env.check(t, check.UID)
			r.NotEqual(models.CheckStatusUp, refreshed.Status)
			r.NotEqual(models.CheckStatusCreated, refreshed.Status)
			r.NotNil(refreshed.LastResultAt, "a real evaluation advances last_result_at like any result")

			// The lease is released onto the next tick.
			job := env.job(t, check.UID)
			r.Nil(job.LeaseWorkerUID)
			r.True(job.ScheduledAt.After(time.Now()))
		})
	}
}

// TestPassiveEvaluatorOnTimeIsUp is the positive control for the overdue test.
func TestPassiveEvaluatorOnTimeIsUp(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	env, ctx := newPassiveEvalEnv(t)
	evaluator := env.evaluator(t, "jobs-node-a")

	check, _ := env.passiveCheck(t, checkerdef.CheckTypeHeartbeat)
	env.age(t, check, time.Hour)
	env.signal(t, check, models.ResultStatusUp, time.Now().Add(-10*time.Second))

	_, _, err := evaluator.RunOnce(ctx)
	r.NoError(err)

	rows := env.evaluations(t, check.UID)
	r.Len(rows, 1)
	r.Equal(int(models.ResultStatusUp), *rows[0].Status)
	r.Equal(models.CheckStatusUp, env.check(t, check.UID).Status)
}

// TestPassiveEvaluatorTwoJobsNodesOneEvaluationPerTick: two jobs nodes claim
// the same due jobs at the same time; each check gets exactly one evaluation
// for the tick, and running both again inside the same period adds nothing.
func TestPassiveEvaluatorTwoJobsNodesOneEvaluationPerTick(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	env, ctx := newPassiveEvalEnv(t)
	nodes := []*PassiveEvaluator{
		env.evaluator(t, "jobs-node-a"),
		env.evaluator(t, "jobs-node-b"),
	}
	r.NotEqual(nodes[0].worker.Load().UID, nodes[1].worker.Load().UID, "two nodes, two workers rows")

	for _, node := range nodes {
		node.batch = 2 // small batches so the two nodes interleave
	}

	const checkCount = 8

	created := make([]*models.Check, 0, checkCount)

	for i := range checkCount {
		checkType := checkerdef.CheckTypeHeartbeat
		if i%2 == 1 {
			checkType = checkerdef.CheckTypeEmail
		}

		check, _ := env.passiveCheck(t, checkType)
		env.age(t, check, time.Hour)
		created = append(created, check)
	}

	runAll := func() {
		var wg sync.WaitGroup

		for _, node := range nodes {
			wg.Add(1)

			go func(evaluator *PassiveEvaluator) {
				defer wg.Done()

				for range checkCount {
					_, _, _ = evaluator.RunOnce(ctx)
				}
			}(node)
		}

		wg.Wait()
	}

	runAll()

	for _, check := range created {
		r.Lenf(env.evaluations(t, check.UID), 1, "check %s must be evaluated exactly once", check.UID)
	}

	runAll()

	for _, check := range created {
		r.Lenf(env.evaluations(t, check.UID), 1, "no second evaluation inside the same period")
	}
}

// TestPassiveEvaluatorNeverReadsItsOwnRows pins the result marker: an
// evaluation must never come back from LastSignals, so the second evaluation
// still points at the beat, not at the first evaluation.
func TestPassiveEvaluatorNeverReadsItsOwnRows(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	env, ctx := newPassiveEvalEnv(t)
	evaluator := env.evaluator(t, "jobs-node-a")

	check, _ := env.passiveCheck(t, checkerdef.CheckTypeHeartbeat)
	env.age(t, check, time.Hour)
	beat := env.signal(t, check, models.ResultStatusUp, time.Now().Add(-5*time.Minute))

	for range 3 {
		env.makeDue(t, check)

		_, _, err := evaluator.RunOnce(ctx)
		r.NoError(err)
	}

	rows := env.evaluations(t, check.UID)
	r.Len(rows, 3)

	for _, row := range rows {
		r.Equal(beat.UID, row.Output[outputKeyLastSignalResultUID], "every evaluation read the beat")
	}

	signals, err := env.db.GetLastSignalForChecks(ctx, env.org.UID, []string{check.UID})
	r.NoError(err)
	r.Equal(beat.UID, signals[check.UID].UID, "LastSignals never returns an evaluation row")

	// Even with the worker row gone (results.worker_uid is ON DELETE SET
	// NULL), the evaluation rows stay out of the signal lookup.
	_, err = env.db.DB().NewUpdate().Model((*models.Result)(nil)).
		Set("worker_uid = NULL").
		Where("check_uid = ?", check.UID).
		Where("worker_uid IS NOT NULL").
		Exec(ctx)
	r.NoError(err)

	signals, err = env.db.GetLastSignalForChecks(ctx, env.org.UID, []string{check.UID})
	r.NoError(err)
	r.Equal(beat.UID, signals[check.UID].UID, "orphaned evaluations are still not signals")
}

// TestPassiveEvaluatorFirstSignalGrace is §3: a new heartbeat with no signal
// stays `created` for two periods (nothing written, no incident), then goes
// Down, so a sender that is never set up still alerts.
func TestPassiveEvaluatorFirstSignalGrace(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	env, ctx := newPassiveEvalEnv(t)
	evaluator := env.evaluator(t, "jobs-node-a")

	check, _ := env.passiveCheck(t, checkerdef.CheckTypeHeartbeat)
	period := time.Duration(check.Period)

	// Inside the grace: at creation, and just before the two periods are up.
	for _, age := range []time.Duration{0, 2*period - 5*time.Second} {
		env.age(t, check, age)

		evaluated, _, err := evaluator.RunOnce(ctx)
		r.NoError(err)
		r.Equal(1, evaluated, "the job is claimed")

		r.Empty(env.evaluations(t, check.UID), "no row is written inside the grace")
		r.Equal(models.CheckStatusCreated, env.check(t, check.UID).Status)
		r.Zero(env.incidentCount(t, check.UID))

		job := env.job(t, check.UID)
		r.Nil(job.LeaseWorkerUID, "the lease is released")
		r.True(job.ScheduledAt.After(time.Now()), "onto the next tick")
	}

	// Past the grace: Down, as before this spec.
	env.age(t, check, 2*period+5*time.Second)

	_, _, err := evaluator.RunOnce(ctx)
	r.NoError(err)

	rows := env.evaluations(t, check.UID)
	r.Len(rows, 1)
	r.Equal(int(models.ResultStatusDown), *rows[0].Status)
	r.Equal("No heartbeat received", rows[0].Output[outputKeyMessage])
	r.NotEqual(models.CheckStatusCreated, env.check(t, check.UID).Status)
}

// TestPassiveEvaluatorPingInsideGraceIsUpWithNoIncident: one real ping inside
// the grace makes the check Up, and the evaluation that follows keeps it Up.
// No incident at any point.
func TestPassiveEvaluatorPingInsideGraceIsUpWithNoIncident(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	env, ctx := newPassiveEvalEnv(t)
	evaluator := env.evaluator(t, "jobs-node-a")

	check, _ := env.passiveCheck(t, checkerdef.CheckTypeHeartbeat)
	token, _ := check.Config["token"].(string)

	// First tick: inside the grace, nothing happens.
	env.makeDue(t, check)
	_, _, err := evaluator.RunOnce(ctx)
	r.NoError(err)
	r.Equal(models.CheckStatusCreated, env.check(t, check.UID).Status)

	// A real ping through the heartbeat ingest.
	beats := heartbeat.NewService(env.db, nil, nil, nil, 0)
	r.NoError(beats.ReceiveHeartbeat(ctx, env.org.Slug, *check.Slug, token, "", "", 0, "test", "", "POST", nil))
	r.Equal(models.CheckStatusUp, env.check(t, check.UID).Status, "the ping itself makes the check Up")

	// The next evaluation, still inside the grace window, confirms it.
	env.makeDue(t, check)
	_, _, err = evaluator.RunOnce(ctx)
	r.NoError(err)

	rows := env.evaluations(t, check.UID)
	r.Len(rows, 1, "a signal ends the grace: the evaluation is written")
	r.Equal(int(models.ResultStatusUp), *rows[0].Status)
	r.Equal(models.CheckStatusUp, env.check(t, check.UID).Status)
	r.Zero(env.incidentCount(t, check.UID), "no incident at any point")
}

// TestPassiveEvaluatorGraceDoesNotReopenAfterSignalRollsUp is the raw-row
// rollup half of §3: a heartbeat's ONE real signal was rolled up and its raw
// row deleted (aggregation.retention_raw, default 24h) while the check is
// still younger than the 2-period grace window — a real scenario for a
// heartbeat whose period is at or beyond that retention.
//
// LastSignals then reads back nil, exactly as if no signal had ever arrived.
// Grace must NOT reopen on that: the ping already happened, and
// check.LastResultAt (written by incidents.ProcessCheckResult off the ping,
// never rolled up) still proves it. The evaluation must run the normal rule
// now, not wait out a second grace window.
//
// PRE-FIX this test FAILS: inFirstSignalGrace looks at the now-nil lastSignal
// alone, finds the check still under 2 periods old, and grants a second
// grace — no row is written and the check is left exactly as if it had never
// been pinged, one full period later than it should have gone Down.
func TestPassiveEvaluatorGraceDoesNotReopenAfterSignalRollsUp(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	env, ctx := newPassiveEvalEnv(t)
	evaluator := env.evaluator(t, "jobs-node-a")

	check, _ := env.passiveCheck(t, checkerdef.CheckTypeHeartbeat)
	period := time.Duration(check.Period)
	token, _ := check.Config["token"].(string)

	// One real ping through the heartbeat ingest: writes the raw signal row
	// and, via ProcessCheckResult, check.last_result_at.
	beats := heartbeat.NewService(env.db, nil, nil, nil, 0)
	r.NoError(beats.ReceiveHeartbeat(ctx, env.org.Slug, *check.Slug, token, "", "", 0, "test", "", "POST", nil))
	r.Equal(models.CheckStatusUp, env.check(t, check.UID).Status)
	r.NotNil(env.check(t, check.UID).LastResultAt, "the ping durably records that a signal arrived")

	// Simulate the aggregation job rolling that raw row up and deleting it:
	// the only trace of the ping in `results` is gone, but last_result_at
	// survives on the check row.
	_, err := env.db.DB().NewDelete().
		Model((*models.Result)(nil)).
		Where("check_uid = ?", check.UID).
		Where("worker_uid IS NULL").
		Where("status = ?", int(models.ResultStatusUp)).
		Exec(ctx)
	r.NoError(err)

	// Still well inside the 2-period grace window by age alone.
	env.age(t, check, 2*period-5*time.Second)

	_, _, err = evaluator.RunOnce(ctx)
	r.NoError(err)

	rows := env.evaluations(t, check.UID)
	r.Len(rows, 1, "the normal rule runs now: grace does not reopen on a rolled-up signal")
	r.Equal(int(models.ResultStatusDown), *rows[0].Status)
	r.NotEqual(models.CheckStatusCreated, env.check(t, check.UID).Status,
		"the check must not fall back to the first-signal grace state")
}

// TestPassiveBootRepairThenOneEvaluation is the migration's end state: a
// passive check still carrying regions and regional jobs (what the 024
// migration and the boot repair exist to fix) is healed by the startup
// reconcile into one NULL-region job, a second repair recreates nothing, and
// the jobs node then evaluates it exactly once per tick.
func TestPassiveBootRepairThenOneEvaluation(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	env, ctx := newPassiveEvalEnv(t)
	evaluator := env.evaluator(t, "jobs-node-a")

	check, job := env.passiveCheck(t, checkerdef.CheckTypeHeartbeat)

	// Recreate the legacy shape: regions on the row, one job per region, no
	// NULL-region job.
	_, err := env.db.DB().NewUpdate().Model((*models.Check)(nil)).
		Set("regions = ?", `["eu-west","us-east"]`).Where("uid = ?", check.UID).Exec(ctx)
	r.NoError(err)
	r.NoError(env.db.DeleteCheckJob(ctx, job.UID))

	for _, region := range []string{"eu-west", "us-east"} {
		legacy := models.NewCheckJob(env.org.UID, check.UID, check.Period)
		legacy.Type = check.Type
		legacy.Config = check.Config
		legacy.Region = &region
		r.NoError(env.db.CreateCheckJob(ctx, legacy))
	}

	checksSvc := checks.NewService(env.db, env.svc.EventNotifier, nil, nil)

	reconciled, err := checksSvc.ReconcileStaleJobSchedules(ctx)
	r.NoError(err)
	r.Equal(1, reconciled)

	healed := env.job(t, check.UID)
	r.Nil(healed.Region, "the boot repair leaves exactly one NULL-region job")

	reconciled, err = checksSvc.ReconcileStaleJobSchedules(ctx)
	r.NoError(err)
	r.Zero(reconciled, "a second boot repair finds nothing and recreates no regional job")
	r.Nil(env.job(t, check.UID).Region)

	env.age(t, check, time.Hour)

	for range 3 {
		_, _, err = evaluator.RunOnce(ctx)
		r.NoError(err)
	}

	r.Len(env.evaluations(t, check.UID), 1, "evaluated exactly once for the tick")
}

func TestPassiveWorkerSlugFitsTheColumnRule(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	r.Equal("jobs-api-1", passiveWorkerSlug("api-1"))

	long := passiveWorkerSlug("a-very-long-node-name-x")
	r.LessOrEqual(len(long), passiveWorkerSlugMaxLen)
	r.Regexp(config.WorkerSlugPattern, long)
	r.NotEqual(long, passiveWorkerSlug("a-very-long-node-name-y"), "two long names sharing a prefix stay distinct")
}
