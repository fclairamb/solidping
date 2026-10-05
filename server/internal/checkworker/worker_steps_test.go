package checkworker

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/app/services"
	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/checkworker/backend"
	"github.com/fclairamb/solidping/server/internal/checkworker/checkjobsvc"
	"github.com/fclairamb/solidping/server/internal/checkworker/scheduling"
	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/entitlements"
	"github.com/fclairamb/solidping/server/internal/handlers/attachments"
	"github.com/fclairamb/solidping/server/internal/handlers/filestorage/localfs"
	"github.com/fclairamb/solidping/server/internal/notifier"
)

var errFakeSlice = errors.New("fake slice failure")

// fakeStepChecker is a scripted StepChecker: each Step call runs the next
// function of script and records its input.
type fakeStepChecker struct {
	mu     sync.Mutex
	inputs []checkerdef.StepInput
	script []func(in checkerdef.StepInput) (checkerdef.StepOutput, error)
}

func (f *fakeStepChecker) Type() checkerdef.CheckType           { return checkerdef.CheckTypeCrawl }
func (f *fakeStepChecker) Validate(*checkerdef.CheckSpec) error { return nil }
func (f *fakeStepChecker) UnitsPerSlice() int                   { return 5 }

func (f *fakeStepChecker) Execute(context.Context, checkerdef.Config) (*checkerdef.Result, error) {
	return &checkerdef.Result{Status: checkerdef.StatusUp}, nil
}

func (f *fakeStepChecker) Step(
	_ context.Context, _ checkerdef.Config, in checkerdef.StepInput,
) (checkerdef.StepOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.inputs = append(f.inputs, in)
	if len(f.script) == 0 {
		return checkerdef.StepOutput{}, errFakeSlice
	}

	next := f.script[0]
	f.script = f.script[1:]

	return next(in)
}

func (f *fakeStepChecker) calls() []checkerdef.StepInput {
	f.mu.Lock()
	defer f.mu.Unlock()

	return append([]checkerdef.StepInput(nil), f.inputs...)
}

func partial(state string, units int) func(checkerdef.StepInput) (checkerdef.StepOutput, error) {
	return func(checkerdef.StepInput) (checkerdef.StepOutput, error) {
		return checkerdef.StepOutput{
			State: []byte(state), Units: units, Progress: map[string]any{"pagesDone": units},
		}, nil
	}
}

func done(status checkerdef.Status) func(checkerdef.StepInput) (checkerdef.StepOutput, error) {
	return func(checkerdef.StepInput) (checkerdef.StepOutput, error) {
		return checkerdef.StepOutput{
			Done:   true,
			Units:  1,
			Result: &checkerdef.Result{Status: status, Output: map[string]any{"pagesCrawled": 3}},
			Report: []byte(`{"findings":[],"pagesCrawled":3}`),
		}, nil
	}
}

func failing(checkerdef.StepInput) (checkerdef.StepOutput, error) {
	return checkerdef.StepOutput{}, errFakeSlice
}

// stepFixture is a worker with a real DirectBackend, SQLite and local file
// storage, and one crawl check whose job sits on the bulk lane.
type stepFixture struct {
	t       *testing.T
	runner  *CheckWorker
	db      *sqlite.Service
	org     *models.Organization
	check   *models.Check
	checker *fakeStepChecker
}

func newStepFixture(t *testing.T) *stepFixture {
	t.Helper()

	ctx := t.Context()

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	require.NoError(t, err)
	require.NoError(t, dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	localfs.Register()

	cfg := &config.Config{}
	cfg.Server.CheckWorker = config.CheckWorkerConfig{Nb: 5, FetchMaxAhead: 5 * time.Minute}
	cfg.Auth.JWTSecret = "test-secret"
	cfg.FileStorage.Type = "local"
	cfg.FileStorage.LocalRoot = t.TempDir()

	svcList := services.NewRegistry()
	checkJobSvc := checkjobsvc.NewService(dbSvc.DB())
	svcList.CheckJobs = checkJobSvc

	events := notifier.NewLocalEventNotifier()
	t.Cleanup(func() { _ = events.Close() })
	svcList.EventNotifier = events

	runner := NewCheckWorker(dbSvc, cfg, svcList, checkJobSvc)

	org := models.NewOrganization("steps", "Steps")
	require.NoError(t, dbSvc.CreateOrganization(ctx, org))

	worker := models.NewWorker("steps-worker", "Steps Worker")
	_, err = dbSvc.DB().NewInsert().Model(worker).Exec(ctx)
	require.NoError(t, err)
	runner.setWorker(worker)

	check := models.NewCheck(org.UID, "crawl-acme", "crawl")
	check.Config = models.JSONMap{"url": "https://www.acme.com/", "maxRunDuration": "5m"}
	require.NoError(t, dbSvc.CreateCheck(ctx, check))

	fake := &fakeStepChecker{}
	runner.getChecker = func(ct checkerdef.CheckType) (checkerdef.Checker, bool) {
		if ct == checkerdef.CheckTypeCrawl {
			return fake, true
		}

		return nil, false
	}

	return &stepFixture{t: t, runner: runner, db: dbSvc, org: org, check: check, checker: fake}
}

// job reads the stored job row.
func (f *stepFixture) job() *models.CheckJob {
	f.t.Helper()

	job := new(models.CheckJob)
	require.NoError(f.t, f.db.DB().NewSelect().Model(job).Where("check_uid = ?", f.check.UID).Scan(f.t.Context()))

	return job
}

// set updates columns of the job row.
func (f *stepFixture) set(column string, value any) {
	f.t.Helper()

	_, err := f.db.DB().NewUpdate().Model((*models.CheckJob)(nil)).
		Set(column+" = ?", value).Where("check_uid = ?", f.check.UID).Exec(f.t.Context())
	require.NoError(f.t, err)
}

// slice claims the job (bulk lane, due now) and executes one slice.
func (f *stepFixture) slice() {
	f.t.Helper()

	f.set("scheduled_at", time.Now().Add(-time.Second))

	jobs, _, err := f.runner.backend.ClaimJobs(f.t.Context(), f.runner.getWorker().UID, nil, 1, 0, 1, time.Minute)
	require.NoError(f.t, err)
	require.Len(f.t, jobs, 1)
	require.Equal(f.t, scheduling.LaneBulk, jobs[0].Lane)

	require.NoError(f.t, f.runner.executeJob(f.t.Context(), slog.Default(), jobs[0]))
}

// workerResults returns the result rows a worker wrote (not the
// "Check created" seed row).
func (f *stepFixture) workerResults() []*models.Result {
	f.t.Helper()

	var results []*models.Result
	require.NoError(f.t, f.db.DB().NewSelect().Model(&results).
		Where("check_uid = ?", f.check.UID).Where("worker_uid IS NOT NULL").Scan(f.t.Context()))

	return results
}

func (f *stepFixture) files(topic string) []*models.File {
	f.t.Helper()

	rows, _, err := f.db.ListFiles(f.t.Context(), f.org.UID, models.ListFilesFilter{Topic: topic})
	require.NoError(f.t, err)

	return rows
}

func TestStepPartialSliceThenFinalSlice(t *testing.T) {
	t.Parallel()

	f := newStepFixture(t)
	f.checker.script = append(f.checker.script, partial(`{"n":1}`, 2), done(checkerdef.StatusUp))

	// Partial slice: no result row, a state file, step_count 1, due now.
	f.slice()

	require.Empty(t, f.workerResults(), "a partial slice writes no result")
	require.Len(t, f.files(attachments.CheckStepStateTopic(f.check.UID)), 1)

	job := f.job()
	require.NotNil(t, job.StepRunUID)
	require.NotNil(t, job.StepStateFileUID)
	require.NotNil(t, job.StepRunStartedAt)
	require.Equal(t, 1, job.StepCount)
	require.Nil(t, job.LeaseWorkerUID)
	require.WithinDuration(t, time.Now(), *job.ScheduledAt, 5*time.Second)
	require.Nil(t, f.checker.calls()[0].State, "a run's first slice gets a nil state")
	require.Equal(t, 5, f.checker.calls()[0].MaxUnits)

	// The run started 30h ago (24h period): the next run is anchored on the
	// run start, which puts it in the past, not a period after now.
	f.set("step_run_started_at", time.Now().Add(-30*time.Hour))

	f.slice()

	require.JSONEq(t, `{"n":1}`, string(f.checker.calls()[1].State), "the next slice resumes the saved state")

	results := f.workerResults()
	require.Len(t, results, 1, "a finished run writes exactly one result")
	require.Equal(t, int(checkerdef.StatusUp), *results[0].Status)

	reportUID, ok := results[0].Output[OutputKeyReportFileUID].(string)
	require.True(t, ok)
	require.Len(t, f.files(attachments.CheckCrawlReportTopic(f.check.UID)), 1)
	require.Equal(t, f.files(attachments.CheckCrawlReportTopic(f.check.UID))[0].UID, reportUID)

	job = f.job()
	require.Nil(t, job.StepRunUID)
	require.Nil(t, job.StepStateFileUID)
	require.Nil(t, job.StepRunStartedAt)
	require.Zero(t, job.StepCount)
	require.Equal(t, scheduling.LaneBulk, job.Lane)
	require.True(t, job.ScheduledAt.Before(time.Now()), "next run anchored on step_run_started_at + period")
}

// A worker that dies after writing state N+1 but before SubmitStep: the next
// claim resumes from state N, still on disk thanks to keep-2.
func TestStepCrashAfterStateWriteResumesPreviousState(t *testing.T) {
	t.Parallel()

	f := newStepFixture(t)
	f.checker.script = append(f.checker.script, partial(`{"n":"A"}`, 1), partial(`{"n":"C"}`, 1))

	f.slice()

	steps, ok := f.runner.backend.(backend.StepBackend)
	require.True(t, ok)

	// The "crashed" worker wrote state B and never moved the pointer.
	_, err := steps.SaveStepState(t.Context(), f.job(), &backend.SaveStepStateRequest{
		RunUID: *f.job().StepRunUID, Step: 2, Payload: []byte(`{"n":"B"}`),
	})
	require.NoError(t, err)

	f.slice()

	require.JSONEq(t, `{"n":"A"}`, string(f.checker.calls()[1].State))
	require.Equal(t, 2, f.job().StepCount)
}

// A stale worker's SubmitStep, after another worker advanced the run, changes
// nothing.
func TestStepStaleWorkerIsFencedOut(t *testing.T) {
	t.Parallel()

	f := newStepFixture(t)
	f.checker.script = append(f.checker.script, partial(`{"n":1}`, 1), partial(`{"n":2}`, 1))

	f.set("scheduled_at", time.Now().Add(-time.Second))

	staleWorker := models.NewWorker("stale-worker", "Stale Worker")
	_, err := f.db.DB().NewInsert().Model(staleWorker).Exec(t.Context())
	require.NoError(t, err)

	jobs, _, err := f.runner.backend.ClaimJobs(t.Context(), staleWorker.UID, nil, 1, 0, 1, time.Minute)
	require.NoError(t, err)
	require.Len(t, jobs, 1)

	staleCopy := jobs[0]

	// Its lease expires; this worker claims and runs two slices.
	f.set("lease_expires_at", time.Now().Add(-time.Second))
	f.slice()
	f.slice()

	before := f.job()
	steps, _ := f.runner.backend.(backend.StepBackend)
	bogus := "bogus-state"

	err = steps.SubmitStep(t.Context(), staleCopy, staleWorker.UID, &backend.SubmitStepRequest{
		RunUID: "stale-run", RunStartedAt: time.Now(), StateFileUID: &bogus, NextAt: time.Now(),
	})
	require.ErrorIs(t, err, checkjobsvc.ErrJobClaimedByAnother)

	after := f.job()
	require.Equal(t, *before.StepStateFileUID, *after.StepStateFileUID)
	require.Equal(t, *before.StepRunUID, *after.StepRunUID)
	require.Equal(t, before.StepCount, after.StepCount)
}

// A state over 1 MiB is not saved: the same slice re-runs as Final and the
// run ends.
func TestStepStateOverCapFinishesTheRun(t *testing.T) {
	t.Parallel()

	f := newStepFixture(t)
	big := `{"pad":"` + strings.Repeat("x", attachments.MaxStepStateBytes) + `"}`

	f.checker.script = append(f.checker.script, partial(big, 1),
		func(in checkerdef.StepInput) (checkerdef.StepOutput, error) {
			require.True(t, in.Final)
			require.JSONEq(t, big, string(in.State))

			return checkerdef.StepOutput{Done: true, Result: &checkerdef.Result{
				Status: checkerdef.StatusWarning, Output: map[string]any{"incomplete": true},
			}}, nil
		})

	f.slice()

	require.Len(t, f.checker.calls(), 2)
	require.Empty(t, f.files(attachments.CheckStepStateTopic(f.check.UID)), "the oversized state is never saved")

	results := f.workerResults()
	require.Len(t, results, 1)
	require.Equal(t, true, results[0].Output["incomplete"])
	require.Nil(t, f.job().StepRunUID)
}

// Past maxRunDuration the slice is Final.
func TestStepDeadlinePassesFinal(t *testing.T) {
	t.Parallel()

	f := newStepFixture(t)
	f.checker.script = append(f.checker.script, partial(`{"n":1}`, 1), done(checkerdef.StatusUp))

	f.slice()
	require.False(t, f.checker.calls()[0].Final)

	f.set("step_run_started_at", time.Now().Add(-6*time.Minute)) // maxRunDuration is 5m
	f.slice()

	require.True(t, f.checker.calls()[1].Final)
}

// One failed slice keeps the state and requeues; three in a row end the run
// with an error result.
func TestStepThreeFailuresEndTheRun(t *testing.T) {
	t.Parallel()

	f := newStepFixture(t)
	f.checker.script = append(f.checker.script, partial(`{"n":1}`, 1), failing, failing, failing)

	f.slice()

	stateFile := *f.job().StepStateFileUID

	f.slice()

	job := f.job()
	require.Equal(t, 1, job.StepFailures)
	require.Equal(t, stateFile, *job.StepStateFileUID, "a failed slice keeps the previous state")
	require.Empty(t, f.workerResults())

	f.slice()
	require.Equal(t, 2, f.job().StepFailures)

	f.slice()

	results := f.workerResults()
	require.Len(t, results, 1)
	require.Equal(t, int(checkerdef.StatusError), *results[0].Status)
	require.Contains(t, results[0].Output[checkerdef.OutputKeyError], "3 slices")

	job = f.job()
	require.Nil(t, job.StepRunUID)
	require.Zero(t, job.StepFailures)
}

// A drained bucket defers the slice (no Step call); unused units go back.
func TestStepUnitsReservationAndRefund(t *testing.T) {
	t.Parallel()

	f := newStepFixture(t)
	ent := entitlements.NewService(f.db, entitlements.DefaultsFor(config.DeploymentModeSaaS), 0) // 10/min
	f.runner.services.Entitlements = ent
	f.checker.script = append(f.checker.script, partial(`{"n":1}`, 2))

	// 10 tokens: the slice reserves 5, uses 2, refunds 3 → 8 left.
	f.slice()
	require.Len(t, f.checker.calls(), 1)

	left, err := ent.ReserveCheckExecutionsUpTo(t.Context(), f.org.UID, 100)
	require.NoError(t, err)
	require.Equal(t, 8, left)

	// The bucket is now empty: the next slice is deferred, Step never runs.
	f.slice()
	require.Len(t, f.checker.calls(), 1, "no slice runs when no unit is granted")

	job := f.job()
	require.Nil(t, job.LeaseWorkerUID)
	require.NotNil(t, job.StepRunUID, "the run in progress survives the deferral")
	require.True(t, job.ScheduledAt.After(time.Now().Add(30*time.Second)), "deferred, not hot-looped")
	require.True(t, job.ScheduledAt.Before(time.Now().Add(2*time.Minute)), "a run in progress waits a minute")
}
