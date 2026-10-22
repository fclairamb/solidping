package workertasks_test

import (
	"context"
	"encoding/json"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/handlers/attachments"
	"github.com/fclairamb/solidping/server/internal/handlers/files"
	"github.com/fclairamb/solidping/server/internal/handlers/filestorage/localfs"
	"github.com/fclairamb/solidping/server/internal/workertasks"
)

const fakeKind = "fake"

// fakeKind is the test-only kind: it counts its hooks and can be told to
// defer the next claim.
type fakeHooks struct {
	failCalls     atomic.Int32
	completeCalls atomic.Int32
	deferNext     atomic.Bool
	lastBody      atomic.Value
}

func (h *fakeHooks) kind() workertasks.TaskKind {
	return workertasks.TaskKind{
		Name: fakeKind,
		PrepareClaim: func(_ context.Context, _ *models.WorkerTask, _ json.RawMessage) (*workertasks.Prepared, error) {
			if h.deferNext.CompareAndSwap(true, false) {
				until := time.Now().Add(time.Hour)

				return &workertasks.Prepared{DeferUntil: &until}, nil
			}

			return &workertasks.Prepared{Units: 10, SliceBudget: 30 * time.Second}, nil
		},
		Complete: func(_ context.Context, _ *models.WorkerTask, body json.RawMessage) (string, error) {
			h.completeCalls.Add(1)
			h.lastBody.Store(string(body))

			return "", nil
		},
		Fail: func(_ context.Context, _ *models.WorkerTask, _ string) error {
			h.failCalls.Add(1)

			return nil
		},
	}
}

// env is one database, one service with the fake kind, and its hooks.
type env struct {
	db    db.Service
	svc   *workertasks.Service
	hooks *fakeHooks
}

func newEnv(t *testing.T, dbSvc db.Service) *env {
	t.Helper()

	localfs.Register()

	cfg := &config.Config{}
	cfg.Auth.JWTSecret = "test-secret"
	cfg.FileStorage.Type = "local"
	cfg.FileStorage.LocalRoot = t.TempDir()

	svc := workertasks.NewService(dbSvc.DB())
	svc.SetStateStore(attachments.NewService(files.NewService(dbSvc, cfg), dbSvc, cfg))

	hooks := &fakeHooks{}
	svc.RegisterKind(hooks.kind())

	return &env{db: dbSvc, svc: svc, hooks: hooks}
}

func newSQLiteDB(t *testing.T) db.Service {
	t.Helper()

	dbSvc, err := sqlite.New(t.Context(), sqlite.Config{InMemory: true})
	require.NoError(t, err)
	require.NoError(t, dbSvc.Initialize(t.Context()))
	t.Cleanup(func() { _ = dbSvc.Close() })

	return dbSvc
}

func (e *env) org(t *testing.T) *models.Organization {
	t.Helper()

	org := models.NewOrganization("org-"+uuid.NewString()[:8], "Acme")
	require.NoError(t, e.db.CreateOrganization(t.Context(), org))

	return org
}

// checkTask creates a check of org and a pending fake task on it.
func (e *env) checkTask(t *testing.T, orgUID, region string) *models.WorkerTask {
	t.Helper()

	check := models.NewCheck(orgUID, "c-"+uuid.NewString()[:8], "http")
	check.Enabled = false
	require.NoError(t, e.db.CreateCheck(t.Context(), check))

	task := models.NewWorkerTask(orgUID, fakeKind, region)
	task.CheckUID = &check.UID
	task.Payload = models.JSONMap{"url": "https://acme.com"}
	require.NoError(t, e.svc.Create(t.Context(), task))

	return task
}

func (e *env) jobTask(t *testing.T, orgUID, region string) *models.WorkerTask {
	t.Helper()

	job := models.NewJob(&orgUID, "discovery")
	_, err := e.db.DB().NewInsert().Model(job).Exec(t.Context())
	require.NoError(t, err)

	task := models.NewWorkerTask(orgUID, fakeKind, region)
	task.JobUID = &job.UID
	require.NoError(t, e.svc.Create(t.Context(), task))

	return task
}

func (e *env) row(t *testing.T, uid string) *models.WorkerTask {
	t.Helper()

	task := new(models.WorkerTask)
	require.NoError(t, e.db.DB().NewSelect().Model(task).Where("uid = ?", uid).Scan(t.Context()))

	return task
}

func (e *env) expireLease(t *testing.T, uid string) {
	t.Helper()

	_, err := e.db.DB().NewUpdate().Model((*models.WorkerTask)(nil)).
		Set("lease_until = ?", time.Now().UTC().Add(-time.Second)).
		Where("uid = ?", uid).Exec(t.Context())
	require.NoError(t, err)
}

func (e *env) makeDue(t *testing.T, uid string) {
	t.Helper()

	_, err := e.db.DB().NewUpdate().Model((*models.WorkerTask)(nil)).
		Set("not_before = ?", time.Now().UTC().Add(-time.Second)).
		Where("uid = ?", uid).Exec(t.Context())
	require.NoError(t, err)
}

func agent(orgUID, region string) *workertasks.WorkerIdentity {
	return &workertasks.WorkerIdentity{
		UID: uuid.NewString(), Region: region, OrgUID: orgUID, IsAgent: true,
		Kinds: []string{fakeKind}, Slots: 1,
	}
}

func cloud(region string, slots int) *workertasks.WorkerIdentity {
	return &workertasks.WorkerIdentity{UID: uuid.NewString(), Region: region, Kinds: []string{fakeKind}, Slots: slots}
}

// runServiceSuite runs every service test against one engine. Subtests
// share the database but each has its own service, hooks, orgs and regions.
func runServiceSuite(t *testing.T, dbSvc db.Service) {
	t.Helper()

	for name, test := range map[string]func(*testing.T, *env){
		"ClaimScoping":         testClaimScoping,
		"SlotsAndLeases":       testSlotsAndLeases,
		"Steps":                testSteps,
		"SliceFailures":        testSliceFailures,
		"Fencing":              testFencing,
		"Deferral":             testDeferral,
		"OneActiveRunPerCheck": testOneActiveRunPerCheck,
		"FinishAndCancel":      testFinishAndCancel,
		"LazyExpiry":           testLazyExpiry,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			test(t, newEnv(t, dbSvc))
		})
	}
}

func TestService_SQLite(t *testing.T) {
	t.Parallel()

	runServiceSuite(t, newSQLiteDB(t))
}

func testClaimScoping(t *testing.T, e *env) {
	t.Helper()

	r := require.New(t)
	ctx := t.Context()

	orgA, orgB := e.org(t), e.org(t)
	region := "@dc1-" + uuid.NewString()[:6]
	cloudRegion := "eu-west-" + uuid.NewString()[:6]

	taskA := e.checkTask(t, orgA.UID, region)
	taskB := e.jobTask(t, orgB.UID, region)
	cloudA := e.checkTask(t, orgA.UID, cloudRegion)
	cloudB := e.checkTask(t, orgB.UID, cloudRegion)

	// Two orgs that both define @dc1 never claim each other's tasks.
	claim, err := e.svc.Claim(ctx, agent(orgA.UID, region))
	r.NoError(err)
	r.NotNil(claim)
	r.Equal(taskA.UID, claim.UID)
	r.Equal(orgA.UID, claim.OrganizationUID)
	r.JSONEq(`{"url":"https://acme.com"}`, string(claim.Input))
	r.Equal(10, claim.Units)

	again, err := e.svc.Claim(ctx, agent(orgA.UID, region))
	r.NoError(err)
	r.Nil(again, "org A has nothing else in its region")

	claim, err = e.svc.Claim(ctx, agent(orgB.UID, region))
	r.NoError(err)
	r.NotNil(claim)
	r.Equal(taskB.UID, claim.UID)
	r.Equal(*taskB.JobUID, claim.JobUID)

	// An agent of another region gets nothing.
	none, err := e.svc.Claim(ctx, agent(orgA.UID, "@other"))
	r.NoError(err)
	r.Nil(none)

	// A cloud worker claims any org's task of its region, and never a private one.
	worker := cloud(cloudRegion, 4)
	seen := map[string]bool{}

	for range 2 {
		got, claimErr := e.svc.Claim(ctx, worker)
		r.NoError(claimErr)
		r.NotNil(got)
		seen[got.UID] = true
	}

	r.True(seen[cloudA.UID] && seen[cloudB.UID])

	private := e.checkTask(t, orgA.UID, region)
	none, err = e.svc.Claim(ctx, cloud(region, 4))
	r.NoError(err)
	r.Nil(none, "a cloud worker never claims a private region's task")
	r.Equal(models.WorkerTaskStatusPending, e.row(t, private.UID).Status)

	// Exact match: a cloud worker of a parent region does not serve a child.
	none, err = e.svc.Claim(ctx, cloud("eu", 4))
	r.NoError(err)
	r.Nil(none)

	// Unknown kinds are ignored.
	unknown := agent(orgA.UID, region)
	unknown.Kinds = []string{"nope"}
	none, err = e.svc.Claim(ctx, unknown)
	r.NoError(err)
	r.Nil(none)
	r.Equal(models.WorkerTaskStatusPending, e.row(t, private.UID).Status)
}

func testSlotsAndLeases(t *testing.T, e *env) {
	t.Helper()

	r := require.New(t)
	ctx := t.Context()

	org := e.org(t)
	region := "@slots-" + uuid.NewString()[:6]
	first := e.checkTask(t, org.UID, region)
	e.checkTask(t, org.UID, region)

	worker := agent(org.UID, region)

	claim, err := e.svc.Claim(ctx, worker)
	r.NoError(err)
	r.NotNil(claim)
	r.Equal(first.UID, claim.UID, "the oldest row first")
	r.Equal(1, claim.Attempt)

	none, err := e.svc.Claim(ctx, worker)
	r.NoError(err)
	r.Nil(none, "the only slot is held")

	// Another worker cannot take a live lease.
	other := agent(org.UID, region)
	other.Kinds = worker.Kinds
	got, err := e.svc.Claim(ctx, other)
	r.NoError(err)
	r.NotNil(got)
	r.NotEqual(first.UID, got.UID)

	// An expired lease is reclaimable and attempts increment; the 4th claim
	// fails the task with max_attempts and calls Fail once.
	r.NoError(e.svc.FailSlice(ctx, got.UID, other.UID, "x")) // park the second task out of the way

	failsBefore := e.hooks.failCalls.Load()

	for attempt := 2; attempt <= workertasks.MaxAttempts; attempt++ {
		e.expireLease(t, first.UID)

		again, claimErr := e.svc.Claim(ctx, agent(org.UID, region))
		r.NoError(claimErr)
		r.NotNil(again)
		r.Equal(first.UID, again.UID)
		r.Equal(attempt, again.Attempt)
	}

	e.expireLease(t, first.UID)

	none, err = e.svc.Claim(ctx, agent(org.UID, region))
	r.NoError(err)
	r.Nil(none)

	row := e.row(t, first.UID)
	r.Equal(models.WorkerTaskStatusFailed, row.Status)
	r.NotNil(row.ErrorCode)
	r.Equal(workertasks.ErrorMaxAttempts, *row.ErrorCode)
	r.Equal(failsBefore+1, e.hooks.failCalls.Load())
}

func testSteps(t *testing.T, e *env) {
	t.Helper()

	r := require.New(t)
	ctx := t.Context()

	org := e.org(t)
	region := "@steps-" + uuid.NewString()[:6]
	task := e.checkTask(t, org.UID, region)
	worker := agent(org.UID, region)

	claim, err := e.svc.Claim(ctx, worker)
	r.NoError(err)
	r.Nil(claim.State, "no state on the first slice")
	r.Equal(0, claim.Slice)

	r.NoError(e.svc.Step(ctx, task.UID, worker.UID, workertasks.StepRequest{
		State:    json.RawMessage(`{"cursor":42}`),
		Progress: models.JSONMap{"pages": 3},
	}))

	row := e.row(t, task.UID)
	r.Equal(models.WorkerTaskStatusPending, row.Status)
	r.Equal(1, row.SliceCount)
	r.Equal(0, row.Attempts)
	r.Nil(row.WorkerUID)
	r.Nil(row.LeaseUntil)
	r.NotNil(row.StateFileUID)
	r.EqualValues(3, row.Progress["pages"])

	claim, err = e.svc.Claim(ctx, worker)
	r.NoError(err)
	r.NotNil(claim)
	r.JSONEq(`{"cursor":42}`, string(claim.State))
	r.Equal(1, claim.Slice)

	// A state over 1 MiB is refused and the stored state is unchanged.
	stateBefore := *e.row(t, task.UID).StateFileUID
	huge := `{"blob":"` + strings.Repeat("a", attachments.MaxStepStateBytes) + `"}`

	err = e.svc.Step(ctx, task.UID, worker.UID, workertasks.StepRequest{State: json.RawMessage(huge)})
	r.ErrorIs(err, workertasks.ErrTooLarge)

	row = e.row(t, task.UID)
	r.Equal(stateBefore, *row.StateFileUID)
	r.Equal(1, row.SliceCount)
	r.Equal(models.WorkerTaskStatusClaimed, row.Status)

	// Progress has its own cap.
	err = e.svc.Step(ctx, task.UID, worker.UID, workertasks.StepRequest{
		State:    json.RawMessage(`{}`),
		Progress: models.JSONMap{"x": strings.Repeat("a", workertasks.MaxProgressBytes)},
	})
	r.ErrorIs(err, workertasks.ErrTooLarge)

	err = e.svc.Step(ctx, task.UID, worker.UID, workertasks.StepRequest{State: json.RawMessage(`not json`)})
	r.ErrorIs(err, workertasks.ErrInvalidState)
}

func testSliceFailures(t *testing.T, e *env) {
	t.Helper()

	r := require.New(t)
	ctx := t.Context()

	org := e.org(t)
	region := "@fail-" + uuid.NewString()[:6]
	task := e.checkTask(t, org.UID, region)
	worker := agent(org.UID, region)
	failsBefore := e.hooks.failCalls.Load()

	for i := range workertasks.MaxSliceFailures {
		claim, err := e.svc.Claim(ctx, worker)
		r.NoError(err)
		r.NotNil(claim, "claim %d", i)

		r.NoError(e.svc.FailSlice(ctx, task.UID, worker.UID, "dns_error"))

		row := e.row(t, task.UID)
		if i < workertasks.MaxSliceFailures-1 {
			r.Equal(models.WorkerTaskStatusPending, row.Status)
			r.True(row.NotBefore.After(time.Now()), "a failed slice waits before its retry")
			e.makeDue(t, task.UID)

			continue
		}

		r.Equal(models.WorkerTaskStatusFailed, row.Status)
		r.Equal("dns_error", *row.ErrorCode)
	}

	r.Equal(failsBefore+1, e.hooks.failCalls.Load())
}

func testFencing(t *testing.T, e *env) {
	t.Helper()

	r := require.New(t)
	ctx := t.Context()

	org := e.org(t)
	region := "@fence-" + uuid.NewString()[:6]
	task := e.checkTask(t, org.UID, region)
	worker := agent(org.UID, region)

	_, err := e.svc.Claim(ctx, worker)
	r.NoError(err)

	step := workertasks.StepRequest{State: json.RawMessage(`{"n":1}`)}
	intruder := uuid.NewString()

	// Another worker.
	_, err = e.svc.Heartbeat(ctx, task.UID, intruder)
	r.ErrorIs(err, workertasks.ErrTaskGone)
	r.ErrorIs(e.svc.Step(ctx, task.UID, intruder, step), workertasks.ErrTaskGone)
	r.ErrorIs(e.svc.Finish(ctx, task.UID, intruder, nil), workertasks.ErrTaskGone)
	r.ErrorIs(e.svc.FailSlice(ctx, task.UID, intruder, "x"), workertasks.ErrTaskGone)

	// The holder's heartbeat works and extends the lease.
	lease, err := e.svc.Heartbeat(ctx, task.UID, worker.UID)
	r.NoError(err)
	r.True(lease.After(time.Now().Add(time.Minute)))

	// After expiry.
	e.expireLease(t, task.UID)

	_, err = e.svc.Heartbeat(ctx, task.UID, worker.UID)
	r.ErrorIs(err, workertasks.ErrTaskGone)
	r.ErrorIs(e.svc.Step(ctx, task.UID, worker.UID, step), workertasks.ErrTaskGone)
	r.ErrorIs(e.svc.Finish(ctx, task.UID, worker.UID, nil), workertasks.ErrTaskGone)

	row := e.row(t, task.UID)
	r.Nil(row.StateFileUID, "nothing written")
	r.Equal(0, row.SliceCount)

	// On a canceled task (its own region: the expired task above is
	// reclaimable and older).
	region2 := region + "-c"
	e2 := e.checkTask(t, org.UID, region2)
	worker2 := agent(org.UID, region2)
	claim, err := e.svc.Claim(ctx, worker2)
	r.NoError(err)
	r.Equal(e2.UID, claim.UID)

	n, err := e.svc.Cancel(ctx, workertasks.CancelFilter{CheckUID: *e2.CheckUID})
	r.NoError(err)
	r.Equal(1, n)

	_, err = e.svc.Heartbeat(ctx, e2.UID, worker2.UID)
	r.ErrorIs(err, workertasks.ErrTaskGone)
	r.ErrorIs(e.svc.Step(ctx, e2.UID, worker2.UID, step), workertasks.ErrTaskGone)
	r.ErrorIs(e.svc.Finish(ctx, e2.UID, worker2.UID, nil), workertasks.ErrTaskGone)

	row = e.row(t, e2.UID)
	r.Equal(models.WorkerTaskStatusCancelled, row.Status)
	r.Nil(row.StateFileUID)
}

func testDeferral(t *testing.T, e *env) {
	t.Helper()

	r := require.New(t)
	ctx := t.Context()

	org := e.org(t)
	region := "@defer-" + uuid.NewString()[:6]
	task := e.checkTask(t, org.UID, region)

	e.hooks.deferNext.Store(true)

	claim, err := e.svc.Claim(ctx, agent(org.UID, region))
	r.NoError(err)
	r.Nil(claim, "the only task deferred")

	row := e.row(t, task.UID)
	r.Equal(models.WorkerTaskStatusPending, row.Status)
	r.Equal(0, row.Attempts, "a deferral counts no attempt")
	r.Nil(row.WorkerUID)
	r.True(row.NotBefore.After(time.Now().Add(30 * time.Minute)))

	e.makeDue(t, task.UID)

	claim, err = e.svc.Claim(ctx, agent(org.UID, region))
	r.NoError(err)
	r.NotNil(claim)
	r.Equal(1, claim.Attempt)
}

func testOneActiveRunPerCheck(t *testing.T, e *env) {
	t.Helper()

	r := require.New(t)
	ctx := t.Context()

	org := e.org(t)
	task := e.checkTask(t, org.UID, "@one-"+uuid.NewString()[:6])

	second := models.NewWorkerTask(org.UID, fakeKind, task.Region)
	second.CheckUID = task.CheckUID
	r.Error(e.svc.Create(ctx, second), "a check has at most one active run")

	both := models.NewWorkerTask(org.UID, fakeKind, task.Region)
	r.ErrorIs(e.svc.Create(ctx, both), workertasks.ErrInvalidTask, "a task needs exactly one parent")

	// Once the run ended, a new one may start.
	_, err := e.svc.Cancel(ctx, workertasks.CancelFilter{CheckUID: *task.CheckUID})
	r.NoError(err)

	third := models.NewWorkerTask(org.UID, fakeKind, task.Region)
	third.CheckUID = task.CheckUID
	r.NoError(e.svc.Create(ctx, third))
}

func testFinishAndCancel(t *testing.T, e *env) {
	t.Helper()

	r := require.New(t)
	ctx := t.Context()

	org := e.org(t)
	region := "@finish-" + uuid.NewString()[:6]
	task := e.checkTask(t, org.UID, region)
	worker := agent(org.UID, region)

	_, err := e.svc.Claim(ctx, worker)
	r.NoError(err)
	r.NoError(e.svc.Step(ctx, task.UID, worker.UID, workertasks.StepRequest{State: json.RawMessage(`{}`)}))

	_, err = e.svc.Claim(ctx, worker)
	r.NoError(err)

	completes := e.hooks.completeCalls.Load()
	r.NoError(e.svc.Finish(ctx, task.UID, worker.UID, json.RawMessage(`{"found":2}`)))
	r.Equal(completes+1, e.hooks.completeCalls.Load())
	lastBody, ok := e.hooks.lastBody.Load().(string)
	r.True(ok)
	r.JSONEq(`{"found":2}`, lastBody)

	row := e.row(t, task.UID)
	r.Equal(models.WorkerTaskStatusDone, row.Status)
	r.Nil(row.WorkerUID)

	// A finished task cannot be finished again.
	r.ErrorIs(e.svc.Finish(ctx, task.UID, worker.UID, nil), workertasks.ErrTaskGone)

	// Cancel by job.
	jobTask := e.jobTask(t, org.UID, region)
	n, err := e.svc.Cancel(ctx, workertasks.CancelFilter{JobUID: *jobTask.JobUID})
	r.NoError(err)
	r.Equal(1, n)
	r.Equal(models.WorkerTaskStatusCancelled, e.row(t, jobTask.UID).Status)

	_, err = e.svc.Cancel(ctx, workertasks.CancelFilter{})
	r.ErrorIs(err, workertasks.ErrInvalidTask)
}

func testLazyExpiry(t *testing.T, e *env) {
	t.Helper()

	r := require.New(t)
	ctx := t.Context()

	org := e.org(t)
	region := "@stale-" + uuid.NewString()[:6]
	task := e.checkTask(t, org.UID, region)

	_, err := e.db.DB().NewUpdate().Model((*models.WorkerTask)(nil)).
		Set("updated_at = ?", time.Now().UTC().Add(-25*time.Hour)).
		Where("uid = ?", task.UID).Exec(ctx)
	r.NoError(err)

	got, err := e.svc.Get(ctx, task.UID)
	r.NoError(err)
	r.Equal(models.WorkerTaskStatusFailed, got.Status)
	r.Equal(models.WorkerTaskErrorExpired, *got.ErrorCode)

	claim, err := e.svc.Claim(ctx, agent(org.UID, region))
	r.NoError(err)
	r.Nil(claim, "a stale task is never claimed")

	// A stale run does not block a new one on its check.
	fresh := models.NewWorkerTask(org.UID, fakeKind, region)
	fresh.CheckUID = task.CheckUID
	r.NoError(e.svc.Create(ctx, fresh))
	r.Equal(models.WorkerTaskStatusFailed, e.row(t, task.UID).Status)
}
