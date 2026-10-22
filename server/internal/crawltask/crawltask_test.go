package crawltask

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/uptrace/bun"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/entitlements"
	"github.com/fclairamb/solidping/server/internal/handlers/attachments"
	"github.com/fclairamb/solidping/server/internal/regions"
)

// v038BulkClaim is the bulk-lane SELECT of a v0.38 node
// (checkjobsvc.selectDueBulkJobs before spec 2026-10-22-03), kept here as the
// old node's view during a rolling upgrade.
func v038BulkClaim(ctx context.Context, idb bun.IDB, now time.Time) ([]*models.CheckJob, error) {
	var jobs []*models.CheckJob

	err := idb.NewSelect().
		Model(&jobs).
		Where("lane = ?", 2).
		Where("scheduled_at <= ?", now).
		WhereGroup(" AND ", func(q *bun.SelectQuery) *bun.SelectQuery {
			return q.
				WhereOr("lease_expires_at IS NULL").
				WhereOr("lease_expires_at < ?", now)
		}).
		OrderExpr("effective_scheduled_at ASC").
		Scan(ctx)

	return jobs, err
}

// upBody is the finish body of an "up" run.
func upBody() json.RawMessage {
	return json.RawMessage(`{"status":` + strconv.Itoa(int(checkerdef.StatusUp)) + `}`)
}

// newFindingsCount reads output.newFindings.count.
func newFindingsCount(t *testing.T, output map[string]any) any {
	t.Helper()

	newFindings, ok := output["newFindings"].(map[string]any)
	require.True(t, ok, "the result carries newFindings")

	return newFindings["count"]
}

// runMaterialization is the engine-agnostic contract of a run's start and
// end: one task per due crawl job, the job leased to it, released at its end.
func runMaterialization(t *testing.T, dbSvc db.Service) {
	t.Helper()

	r := require.New(t)
	ctx := t.Context()
	h := newHarness(t, dbSvc, nil)
	region := "eu-mat-" + h.org.UID[:6]

	check := h.crawl(h.org.UID, region, models.JSONMap{"url": "https://www.acme.com/"})
	since := time.Now()

	other := models.NewCheck(h.org.UID, "http-"+h.org.UID[:6], "http")
	other.Regions = []string{region}
	other.Config = models.JSONMap{"url": "https://www.acme.com/"}
	r.NoError(dbSvc.CreateCheck(ctx, other))
	h.due(other)

	// A due crawl job becomes exactly one task, claimed at once, and the
	// job is leased to the run (no worker, an expiry past the deadline).
	worker := cloudWorker(t, dbSvc, region)

	claim, err := h.tasks.Claim(ctx, worker)
	r.NoError(err)
	r.NotNil(claim)
	r.Equal(Kind, claim.Kind)
	r.Equal(check.UID, claim.CheckUID)
	r.Equal(region, claim.Region)

	job := h.job(check)
	r.Nil(job.LeaseWorkerUID)
	r.NotNil(job.LeaseExpiresAt)
	r.True(job.LeaseExpiresAt.After(time.Now().Add(defaultMaxRunDuration)), "leased past the run's deadline")
	r.Len(h.tasksOf(check), 1)

	// A second claim does not create a second task.
	claim2, err := h.tasks.Claim(ctx, cloudWorker(t, dbSvc, region))
	r.NoError(err)
	r.Nil(claim2)
	r.Len(h.tasksOf(check), 1)

	// Rolling upgrade: the v0.38 bulk claim skips a job leased to a task.
	old, err := v038BulkClaim(ctx, dbSvc.DB(), time.Now())
	r.NoError(err)

	for _, oldJob := range old {
		r.NotEqual(job.UID, oldJob.UID, "a v0.38 node never claims a job leased to a task")
	}

	// A non-crawl job is untouched.
	otherJob := h.job(other)
	r.Nil(otherJob.LeaseExpiresAt)
	r.Empty(h.tasksOf(other))

	// "Run now" during the run is parked, then honored by the run's end.
	parked := time.Now().Add(-time.Minute).UTC().Truncate(time.Millisecond)
	r.NoError(dbSvc.RequestCheckRun(ctx, job.UID, parked))
	r.NotNil(h.job(check).RunRequestedAt)

	r.NoError(h.tasks.Finish(ctx, claim.UID, worker.UID, upBody()))

	released := h.job(check)
	r.Nil(released.LeaseExpiresAt)
	r.Nil(released.RunRequestedAt)
	r.WithinDuration(parked, *released.ScheduledAt, time.Millisecond, "the parked run request is honored")

	// The next run's end anchors the next period on the run's start: a run
	// that started 3 h ago on a 1 h check is due again at once.
	claim, err = h.tasks.Claim(ctx, worker)
	r.NoError(err)
	r.NotNil(claim)

	started := time.Now().Add(-3 * time.Hour).UTC()
	h.setTask(claim.UID, "run_started_at", started)

	r.NoError(h.tasks.Finish(ctx, claim.UID, worker.UID, upBody()))

	released = h.job(check)
	r.Nil(released.LeaseExpiresAt)
	r.True(released.ScheduledAt.After(started), "after the run's start")
	r.True(released.ScheduledAt.Before(time.Now()), "anchored on the start, not on the end")
	r.Len(h.results(check, since), 2, "one result per run")
}

func TestMaterialization_SQLite(t *testing.T) {
	t.Parallel()

	runMaterialization(t, newSQLite(t))
}

// A whole crawl of an httptest site with the real checker: several slices
// with the state round-tripping through the server, one result per run, the
// report stored and diffed against the previous run's.
func TestFullRunWithTheCrawlChecker(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	h := newHarness(t, newSQLite(t), nil)
	web := site(t, 30)
	region := "eu-west"
	check := h.crawl(h.org.UID, region, siteConfig(web.URL))
	worker := cloudWorker(t, h.db, region)

	since := time.Now()
	slices := h.runToEnd(worker, check)
	r.GreaterOrEqual(slices, 2, "32 pages at 20 units per slice take several slices")

	results := h.results(check, since)
	r.Len(results, 1, "one result for the whole run")

	first := results[0]
	r.Equal(int(models.ResultStatusDown), *first.Status, "the broken internal link fails the run")
	r.GreaterOrEqual(first.Output["pagesCrawled"], float64(31))
	r.NotEmpty(first.Output[OutputKeyReportFileUID])
	r.EqualValues(1, newFindingsCount(t, first.Output), "every finding is new on the first run")
	r.Equal(worker.UID, *first.WorkerUID)
	r.Equal(region, *first.Region)

	reports, err := h.store.ListCrawlReports(t.Context(), h.org.UID, check.UID)
	r.NoError(err)
	r.Len(reports, 1)

	// The second run diffs against the first run's report.
	h.due(check)

	since = time.Now()
	h.runToEnd(worker, check)

	results = h.results(check, since)
	r.Len(results, 1)
	r.EqualValues(0, newFindingsCount(t, results[0].Output), "the broken link is not new")

	tasks := h.tasksOf(check)
	r.Len(tasks, 2)
	r.Equal(models.WorkerTaskStatusDone, tasks[0].Status)
	r.Equal(models.WorkerTaskStatusDone, tasks[1].Status)
}

// A slice's units are reserved from the org's bucket and what it did not use
// is refunded; a drained bucket defers the slice without counting an attempt.
func TestUnitsReservedRefundedAndDeferred(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := t.Context()
	dbSvc := newSQLite(t)
	ent := entitlements.NewService(dbSvc, entitlements.DefaultsFor(config.DeploymentModeSaaS), 0) // 10/min
	h := newHarness(t, dbSvc, ent)

	fake := &fakeChecker{script: []func(checkerdef.StepInput) (checkerdef.StepOutput, error){partial(`{"n":1}`, 2)}}
	h.useChecker(fake)

	check := h.crawl(h.org.UID, "eu-west", models.JSONMap{"url": "https://www.acme.com/"})
	worker := cloudWorker(t, dbSvc, "eu-west")

	// 10 tokens: the slice reserves 5, uses 2, the server refunds 3: 8 left.
	claim := h.slice(worker)
	r.NotNil(claim)
	r.Equal(5, claim.Units)
	r.Len(fake.calls(), 1)

	left, err := ent.ReserveCheckExecutionsUpTo(ctx, h.org.UID, 100)
	r.NoError(err)
	r.Equal(8, left, "positive control: the bucket moved by the units used")

	// The bucket is empty: the next slice is deferred, the checker never runs.
	r.Nil(h.slice(worker))
	r.Len(fake.calls(), 1, "no slice runs when no unit is granted")

	task := h.tasksOf(check)[0]
	r.Equal(models.WorkerTaskStatusPending, task.Status)
	r.Equal(0, task.Attempts, "a deferral counts no attempt")
	r.True(task.NotBefore.After(time.Now().Add(30*time.Second)), "deferred, not hot-looped")
	r.True(task.NotBefore.Before(time.Now().Add(2*time.Minute)), "a run in progress waits a minute")

	// A run not started yet waits for its next tick, and has not started.
	fresh := h.crawl(h.org.UID, "eu-west", models.JSONMap{"url": "https://www.acme.com/"})
	r.Nil(h.slice(worker))

	freshTask := h.tasksOf(fresh)[0]
	r.Nil(freshTask.RunStartedAt)
	r.True(freshTask.NotBefore.After(time.Now().Add(30*time.Minute)), "a new run waits for its next tick")
}

// An expired lease is reclaimed and the run goes on from the last saved state.
func TestLostLeaseResumesFromTheSavedState(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	h := newHarness(t, newSQLite(t), nil)

	fake := &fakeChecker{script: []func(checkerdef.StepInput) (checkerdef.StepOutput, error){
		partial(`{"n":1}`, 1), done(1),
	}}
	h.useChecker(fake)

	check := h.crawl(h.org.UID, "eu-west", models.JSONMap{"url": "https://www.acme.com/"})
	worker := cloudWorker(t, h.db, "eu-west")

	r.NotNil(h.slice(worker))

	// A worker claims the next slice and dies: the lease expires, another
	// worker gets the same slice with the saved state.
	claim, err := h.tasks.Claim(t.Context(), worker)
	r.NoError(err)
	r.NotNil(claim)
	h.setTask(claim.UID, "lease_until", time.Now().Add(-time.Second).UTC())

	other := cloudWorker(t, h.db, "eu-west")
	resumed := h.slice(other)
	r.NotNil(resumed)
	r.JSONEq(`{"n":1}`, string(resumed.State), "the run resumes from the last saved state")
	r.Equal(2, resumed.Attempt)
	r.Equal(1, resumed.Slice)
	r.JSONEq(`{"n":1}`, string(fake.calls()[1].State))
	r.Equal(models.WorkerTaskStatusDone, h.tasksOf(check)[0].Status)
}

// Three failed slices in a row end the run with one error result.
func TestThreeFailedSlicesWriteOneErrorResult(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	h := newHarness(t, newSQLite(t), nil)

	fake := &fakeChecker{script: []func(checkerdef.StepInput) (checkerdef.StepOutput, error){
		partial(`{"n":1}`, 1), failing, failing, failing,
	}}
	h.useChecker(fake)

	check := h.crawl(h.org.UID, "eu-west", models.JSONMap{"url": "https://www.acme.com/"})
	worker := cloudWorker(t, h.db, "eu-west")
	since := time.Now()

	first := h.slice(worker)
	r.NotNil(first)

	for range 3 {
		h.setTask(first.UID, "not_before", time.Now().Add(-time.Second).UTC())
		r.NotNil(h.slice(worker))
	}

	task := h.tasksOf(check)[0]
	r.Equal(models.WorkerTaskStatusFailed, task.Status)
	r.Equal(codeStepFailed, *task.ErrorCode)

	results := h.results(check, since)
	r.Len(results, 1, "one error result")
	r.Equal(int(models.ResultStatusError), *results[0].Status)
	r.Contains(results[0].Output[checkerdef.OutputKeyError], "3 slices")
	r.Nil(h.job(check).LeaseExpiresAt, "the failed run released its job")
}

// A run past its maxRunDuration gets a final slice.
func TestMaxRunDurationMakesTheNextClaimFinal(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	h := newHarness(t, newSQLite(t), nil)

	fake := &fakeChecker{script: []func(checkerdef.StepInput) (checkerdef.StepOutput, error){
		partial(`{"n":1}`, 1), done(1),
	}}
	h.useChecker(fake)

	check := h.crawl(h.org.UID, "eu-west", models.JSONMap{"url": "https://www.acme.com/", "maxRunDuration": "10m"})
	worker := cloudWorker(t, h.db, "eu-west")

	first := h.slice(worker)
	r.NotNil(first)
	r.False(first.Final)

	h.setTask(first.UID, "run_started_at", time.Now().Add(-11*time.Minute).UTC())

	second := h.slice(worker)
	r.NotNil(second)
	r.True(second.Final, "past maxRunDuration, the slice is final")
	r.True(fake.calls()[1].Final)
	r.Equal(models.WorkerTaskStatusDone, h.tasksOf(check)[0].Status)
}

// A state over the cap is not saved: the slice finishes the run with what it
// has, and the report is dropped when the result alone fits.
func TestStateOverTheCapFinishesTheRun(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	h := newHarness(t, newSQLite(t), nil)

	big := `{"pad":"` + strings.Repeat("x", attachments.MaxStepStateBytes) + `"}`
	fake := &fakeChecker{script: []func(checkerdef.StepInput) (checkerdef.StepOutput, error){
		partial(big, 2), done(1),
	}}
	h.useChecker(fake)

	check := h.crawl(h.org.UID, "eu-west", models.JSONMap{"url": "https://www.acme.com/"})
	worker := cloudWorker(t, h.db, "eu-west")
	since := time.Now()

	r.NotNil(h.slice(worker))

	calls := fake.calls()
	r.Len(calls, 2)
	r.True(calls[1].Final, "the over-cap state is finished in the same claim")
	r.Zero(calls[1].MaxUnits)
	r.Equal(models.WorkerTaskStatusDone, h.tasksOf(check)[0].Status)
	r.Len(h.results(check, since), 1)
}

// Agent path: an agent of @dc1 claims and runs the crawl of a @dc1 check of
// its own org, against a site only its test listener serves. Another org's
// agent of the same location never claims it, and a cloud worker never does.
func TestAgentRunsTheCrawlOfItsPrivateLocation(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	h := newHarness(t, newSQLite(t), nil)
	web := site(t, 3)
	check := h.crawl(h.org.UID, "@dc1", siteConfig(web.URL))

	strangers := models.NewOrganization("strangers-"+h.org.UID[:6], "Others")
	r.NoError(h.db.CreateOrganization(t.Context(), strangers))

	r.Nil(h.slice(agentWorker(strangers.UID, "@dc1")), "another org's agent never claims it")
	r.Nil(h.slice(cloudWorker(t, h.db, "@dc1")), "a cloud worker never serves a private location")
	r.Empty(h.tasksOf(check))

	since := time.Now()
	agent := agentWorker(h.org.UID, "@dc1")
	h.runToEnd(agent, check)

	results := h.results(check, since)
	r.Len(results, 1)
	r.Equal("@dc1", *results[0].Region)
	r.Nil(results[0].WorkerUID, "an agent with no workers row names no worker")
	r.GreaterOrEqual(results[0].Output["pagesCrawled"], float64(4))
}

// Pausing the check cancels its run at the next claim, and releases nothing
// that is gone.
func TestPausedCheckCancelsItsRun(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	h := newHarness(t, newSQLite(t), nil)

	fake := &fakeChecker{script: []func(checkerdef.StepInput) (checkerdef.StepOutput, error){partial(`{"n":1}`, 1)}}
	h.useChecker(fake)

	check := h.crawl(h.org.UID, "eu-west", models.JSONMap{"url": "https://www.acme.com/"})
	worker := cloudWorker(t, h.db, "eu-west")
	r.NotNil(h.slice(worker))

	_, err := h.db.DB().NewDelete().Model((*models.CheckJob)(nil)).Where("check_uid = ?", check.UID).Exec(t.Context())
	r.NoError(err)

	r.Nil(h.slice(worker))
	r.Equal(models.WorkerTaskStatusCancelled, h.tasksOf(check)[0].Status)
	r.Len(fake.calls(), 1)
}

// The published capability is the executor's.
func TestCrawlCapabilityIsPublished(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	r.Equal(Capability, regions.CapabilityCrawl)
	r.Equal(Kind, NewExecutor().Kind())
	r.Equal([]string{"ipv4"}, regions.StripTaskCapabilities([]string{"ipv4", Capability}))
}
