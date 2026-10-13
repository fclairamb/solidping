package db

import (
	"context"
	"database/sql"
	"time"

	"github.com/uptrace/bun"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

// requestCheckRunAttempts bounds the retries of RequestCheckRunOn. A retry is
// only needed when the job's lease ends between the two updates, which takes
// a release landing in a window of a few microseconds; three tries is plenty.
const requestCheckRunAttempts = 3

// RequestCheckRunOn is the shared body of Service.RequestCheckRun for both
// engines (specs 2026-10-04-01 and 2026-10-12-05).
//
// An idle job is made due at requestedAt (scheduled_at and
// effective_scheduled_at). A job a worker holds, or one carrying a multi-step
// run in progress, cannot take a due time (the release rewrites scheduled_at),
// so the request is parked in run_requested_at instead and every release makes
// the job due at it. The earliest pending request is kept.
//
// The two updates are each guarded by the lease state they expect. When a
// release lands between them, neither matches and the loop goes round again,
// so a request is never parked on a job that has no release coming.
func RequestCheckRunOn(ctx context.Context, idb bun.IDB, jobUID string, requestedAt time.Time) error {
	for range requestCheckRunAttempts {
		res, err := idb.NewUpdate().
			Model((*models.CheckJob)(nil)).
			Set("scheduled_at = ?", requestedAt).
			Set("effective_scheduled_at = ?", requestedAt).
			Set("updated_at = ?", requestedAt).
			Where("uid = ?", jobUID).
			Where("step_run_uid IS NULL").
			WhereGroup(" AND ", func(q *bun.UpdateQuery) *bun.UpdateQuery {
				return q.Where("lease_expires_at IS NULL").WhereOr("lease_expires_at < ?", requestedAt)
			}).
			Exec(ctx)
		if err != nil {
			return err
		}

		if updated(res) {
			return nil
		}

		res, err = idb.NewUpdate().
			Model((*models.CheckJob)(nil)).
			Set("run_requested_at = COALESCE(run_requested_at, ?)", requestedAt).
			Set("updated_at = ?", requestedAt).
			Where("uid = ?", jobUID).
			WhereGroup(" AND ", func(q *bun.UpdateQuery) *bun.UpdateQuery {
				return q.Where("step_run_uid IS NOT NULL").WhereOr("lease_expires_at >= ?", requestedAt)
			}).
			Exec(ctx)
		if err != nil {
			return err
		}

		if updated(res) {
			return nil
		}

		exists, err := idb.NewSelect().
			Model((*models.CheckJob)(nil)).
			Where("uid = ?", jobUID).
			Exists(ctx)
		if err != nil {
			return err
		}

		if !exists {
			return sql.ErrNoRows
		}
	}

	return ErrCheckJobBusy
}

func updated(res sql.Result) bool {
	rows, err := res.RowsAffected()

	return err == nil && rows > 0
}
