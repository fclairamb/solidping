package workertasks_test

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"github.com/uptrace/bun"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/workertasks"
)

const hookedKind = "hooked"

// hookLog records the hooks spec 2026-10-22-03 added: Materialize, Step, End,
// and the cancel answer of PrepareClaim.
type hookLog struct {
	mu          sync.Mutex
	steps       []int // UnitsGranted seen by Step
	ends        []models.WorkerTaskStatus
	cancel      map[string]bool
	defers      map[string]bool
	materialize func(ctx context.Context, tx bun.IDB, identity *workertasks.WorkerIdentity) error
}

func (h *hookLog) kind() workertasks.TaskKind {
	return workertasks.TaskKind{
		Name: hookedKind,
		Materialize: func(ctx context.Context, tx bun.IDB, identity *workertasks.WorkerIdentity) error {
			if h.materialize == nil {
				return nil
			}

			return h.materialize(ctx, tx, identity)
		},
		PrepareClaim: func(_ context.Context, task *models.WorkerTask, _ json.RawMessage) (*workertasks.Prepared, error) {
			h.mu.Lock()
			defer h.mu.Unlock()

			if h.cancel[task.UID] {
				return nil, workertasks.ErrCancelTask
			}

			if h.defers[task.UID] {
				delete(h.defers, task.UID)

				until := time.Now().Add(time.Hour)

				return &workertasks.Prepared{DeferUntil: &until}, nil
			}

			return &workertasks.Prepared{Units: 7}, nil
		},
		Step: func(_ context.Context, task *models.WorkerTask, _ workertasks.StepRequest) error {
			h.mu.Lock()
			defer h.mu.Unlock()

			h.steps = append(h.steps, task.UnitsGranted)

			return nil
		},
		End: func(_ context.Context, task *models.WorkerTask) {
			h.mu.Lock()
			defer h.mu.Unlock()

			h.ends = append(h.ends, task.Status)
		},
	}
}

func (h *hookLog) endStatuses() []models.WorkerTaskStatus {
	h.mu.Lock()
	defer h.mu.Unlock()

	return append([]models.WorkerTaskStatus(nil), h.ends...)
}

// testKindHooks covers the hooks a kind uses to run a crawl (spec
// 2026-10-22-03): the grant kept on the row, Step and End, a cancel from
// PrepareClaim, Materialize in the claim, and a deferral that resets the run
// clock of a run that never ran.
func testKindHooks(t *testing.T, e *env) {
	t.Helper()

	r := require.New(t)
	ctx := t.Context()

	log := &hookLog{cancel: map[string]bool{}, defers: map[string]bool{}}
	e.svc.RegisterKind(log.kind())

	org := e.org(t)
	region := "@hooks-" + uuid.NewString()[:6]
	identity := agent(org.UID, region)
	identity.Kinds = []string{hookedKind}

	newTask := func() *models.WorkerTask {
		t.Helper()

		check := models.NewCheck(org.UID, "c-"+uuid.NewString()[:8], "http")
		check.Enabled = false
		r.NoError(e.db.CreateCheck(ctx, check))

		task := models.NewWorkerTask(org.UID, hookedKind, region)
		task.CheckUID = &check.UID
		r.NoError(e.svc.Create(ctx, task))

		return task
	}

	// Grant kept on the row, Step sees it, the step clears it, End on finish.
	task := newTask()
	claim, err := e.svc.Claim(ctx, identity)
	r.NoError(err)
	r.NotNil(claim)
	r.Equal(7, claim.Units)
	r.Equal(7, e.row(t, task.UID).UnitsGranted, "the grant is kept on the row")

	r.NoError(e.svc.Step(ctx, task.UID, identity.UID, workertasks.StepRequest{State: json.RawMessage(`{}`), Units: 3}))
	r.Equal([]int{7}, log.steps)
	r.Zero(e.row(t, task.UID).UnitsGranted, "a recorded slice clears the grant")

	claim, err = e.svc.Claim(ctx, identity)
	r.NoError(err)
	r.NotNil(claim)
	r.NoError(e.svc.Finish(ctx, task.UID, identity.UID, json.RawMessage(`{}`)))
	r.Equal([]models.WorkerTaskStatus{models.WorkerTaskStatusDone}, log.endStatuses())

	// PrepareClaim answering ErrCancelTask cancels the task: no claim.
	canceled := newTask()

	log.mu.Lock()
	log.cancel[canceled.UID] = true
	log.mu.Unlock()

	claim, err = e.svc.Claim(ctx, identity)
	r.NoError(err)
	r.Nil(claim)
	r.Equal(models.WorkerTaskStatusCancelled, e.row(t, canceled.UID).Status)
	r.Equal(models.WorkerTaskStatusCancelled, log.endStatuses()[1])

	// Cancel runs End too.
	other := newTask()
	n, err := e.svc.Cancel(ctx, workertasks.CancelFilter{CheckUID: *other.CheckUID})
	r.NoError(err)
	r.Equal(1, n)
	r.Len(log.endStatuses(), 3)

	// A deferral of a run that never ran a slice resets its run clock.
	deferred := newTask()

	log.mu.Lock()
	log.defers[deferred.UID] = true
	log.mu.Unlock()

	claim, err = e.svc.Claim(ctx, identity)
	r.NoError(err)
	r.Nil(claim)

	row := e.row(t, deferred.UID)
	r.Nil(row.RunStartedAt, "a run deferred before its first slice has not started")
	r.Equal(0, row.Attempts)

	_, err = e.db.DB().NewDelete().Model((*models.WorkerTask)(nil)).Where("uid = ?", deferred.UID).Exec(ctx)
	r.NoError(err)

	// Materialize runs in the claim, and the claim can hand out its task.
	check := models.NewCheck(org.UID, "m-"+uuid.NewString()[:8], "http")
	check.Enabled = false
	r.NoError(e.db.CreateCheck(ctx, check))

	var created string

	log.materialize = func(ctx context.Context, tx bun.IDB, _ *workertasks.WorkerIdentity) error {
		if created != "" {
			return nil
		}

		task := models.NewWorkerTask(org.UID, hookedKind, region)
		task.CheckUID = &check.UID

		ok, err := e.svc.CreateIfNone(ctx, tx, task)
		if ok {
			created = task.UID
		}

		return err
	}

	claim, err = e.svc.Claim(ctx, identity)
	r.NoError(err)
	r.NotNil(claim)
	r.Equal(created, claim.UID, "the task materialized by the claim is claimed by it")

	// CreateIfNone on a check with a live run creates nothing, without error.
	again := models.NewWorkerTask(org.UID, hookedKind, region)
	again.CheckUID = &check.UID
	ok, err := e.svc.CreateIfNone(ctx, e.db.DB(), again)
	r.NoError(err)
	r.False(ok)
}
