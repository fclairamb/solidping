package jobsvc

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/uptrace/bun"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/postgres"
	"github.com/fclairamb/solidping/server/internal/notifier"
	"github.com/fclairamb/solidping/server/internal/testsupport"
)

// portClaimRollbackPG is distinct from every other embedded-postgres port in
// the suite.
const portClaimRollbackPG = 15716

// cancelAfterClaimUpdate cancels the claim's context right after its
// pending -> running UPDATE, i.e. between the row being locked and COMMIT:
// what a runner sees when its worker context is canceled at shutdown mid-claim.
type cancelAfterClaimUpdate struct {
	db     *bun.DB
	cancel context.CancelFunc
	jobUID string
}

func (h *cancelAfterClaimUpdate) BeforeQuery(ctx context.Context, _ *bun.QueryEvent) context.Context {
	return ctx
}

func (h *cancelAfterClaimUpdate) AfterQuery(ctx context.Context, event *bun.QueryEvent) {
	if h.cancel == nil || !strings.HasPrefix(event.Query, `UPDATE "jobs"`) {
		return
	}

	h.cancel()

	// database/sql rolls the transaction back asynchronously on cancel. Wait
	// until the row lock is gone so COMMIT always finds the transaction
	// finished (ErrTxDone, as in CI) rather than racing the rollback.
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); {
		if h.rowUnlocked(context.WithoutCancel(ctx)) {
			return
		}

		time.Sleep(5 * time.Millisecond)
	}
}

func (h *cancelAfterClaimUpdate) rowUnlocked(ctx context.Context) bool {
	return h.db.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		var job models.Job

		return tx.NewSelect().Model(&job).Column("uid").
			Where("uid = ?", h.jobUID).For("UPDATE NOWAIT").Scan(ctx)
	}) == nil
}

// TestClaimRollbackDoesNotStrandDueJob_Postgres reproduces the lost
// escalation behind the flaky TestIncidentOpenAndClose scenario. Several
// scenario servers share one database. A server shutting down canceled its
// runner's context while that runner held a due job's row inside claimNextJob:
// database/sql rolled the transaction back, COMMIT failed with "transaction has
// already been committed or rolled back", and the job went back to pending.
// The surviving runners had woken on the same job.created signal, missed the
// locked row (SKIP LOCKED), and nextPendingWait ignored due rows, so they slept
// the 5-minute fallback with the job due in the queue.
func TestClaimRollbackDoesNotStrandDueJob_Postgres(t *testing.T) {
	t.Parallel()

	if testing.Short() {
		t.Skip("skipping embedded-postgres test in -short mode")
	}

	ctx := t.Context()

	dbSvc, err := postgres.New(ctx, &postgres.Config{
		Embedded: true,
		Port:     portClaimRollbackPG,
		RunMode:  "test",
	})
	if err != nil {
		testsupport.PostgresUnavailable(t, err)
	}

	t.Cleanup(func() { _ = dbSvc.Close() })

	if initErr := dbSvc.Initialize(ctx); initErr != nil {
		testsupport.PostgresInitFailed(t, initErr)
	}

	bus := notifier.NewLocalEventNotifier()
	t.Cleanup(func() { _ = bus.Close() })

	hook := &cancelAfterClaimUpdate{db: dbSvc.DB()}
	dbSvc.DB().AddQueryHook(hook)

	svc := &serviceImpl{db: dbSvc.DB(), dbSvc: dbSvc, notifier: bus}

	r := require.New(t)

	insertDueJob := func() *models.Job {
		job := models.NewJob(nil, "email")
		job.Status = models.JobStatusPending
		job.ScheduledAt = time.Now().Add(-time.Second)
		_, insErr := dbSvc.DB().NewInsert().Model(job).Exec(ctx)
		r.NoError(insErr)

		return job
	}

	// 1. A due job held by another transaction gets the floor.
	held := insertDueJob()

	holder, err := dbSvc.DB().BeginTx(ctx, nil)
	r.NoError(err)

	defer func() { _ = holder.Rollback() }()

	var locked models.Job
	r.NoError(holder.NewSelect().Model(&locked).
		Where("uid = ?", held.UID).For("UPDATE").Scan(ctx))

	_, err = svc.claimNextJob(ctx)
	r.ErrorIs(err, sql.ErrNoRows, "SKIP LOCKED must skip the held row")
	r.Equal(getJobWaitMinWait, svc.nextPendingWait(ctx),
		"a due job behind a lock must be retried at the floor, not after the 5-minute fallback")

	// The holder rolls back: nothing notifies, the floor alone brings the
	// runner back to the job.
	r.NoError(holder.Rollback())

	waitCtx, cancelWait := context.WithTimeout(ctx, 5*time.Second)
	defer cancelWait()

	claimed, err := svc.GetJobWait(waitCtx)
	r.NoError(err)
	r.Equal(held.UID, claimed.UID)

	// 2. A claim canceled mid-transaction reports the cancellation, and the
	// job stays pending for another runner.
	stranded := insertDueJob()

	claimCtx, cancelClaim := context.WithCancel(ctx)
	defer cancelClaim()

	hook.cancel, hook.jobUID = cancelClaim, stranded.UID
	_, err = svc.claimNextJob(claimCtx)
	hook.cancel = nil

	r.ErrorIs(err, context.Canceled,
		"a shutdown mid-claim is a clean stop for the runner, not a processing error")

	var after models.Job
	r.NoError(dbSvc.DB().NewSelect().Model(&after).Where("uid = ?", stranded.UID).Scan(ctx))
	r.Equal(models.JobStatusPending, after.Status)
}
