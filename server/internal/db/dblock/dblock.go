// Package dblock runs a piece of work on at most one process at a time, using
// a PostgreSQL session advisory lock as the mutual exclusion primitive.
//
// It exists for supervisors that are safe to run everywhere but wasteful to
// run everywhere — the JMAP inbox consumer being the first (spec
// 2026-08-22-01). Advisory locks are the right size of hammer here: every
// replica already shares one Postgres, so leader election needs no new
// component, no quorum and no lease bookkeeping. What it is NOT is a
// correctness mechanism: the holder can lose the lock at any instant (its
// connection dies, the process pauses), and a second holder can be running
// before the first has noticed. Whatever runs under RunExclusive must still be
// correct when two copies overlap.
//
// # Advisory-lock key registry
//
// Keys are hand-allocated from the table below rather than hashed from a
// string, so a collision is impossible by construction and `grep <key>` finds
// every user of it. All keys use the single-argument bigint form of
// pg_advisory_lock. The numbering is 0x5001_0000 + sequence, where 0x5001
// ("SP" 01) namespaces SolidPing's keys away from anything else that might
// share the database:
//
//	0x50010001  KeyJMAPInbox  single active JMAP inbox consumer
//
// To add one: take the next free sequence number, define the constant here
// with a one-line purpose, and leave the old numbers alone even if their
// feature is removed — reusing a retired key risks two unrelated features
// silently excluding each other during a rolling deploy.
//
// This convention is also written down in wiki/conventions/database.md.
package dblock

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect/pgdialect"
)

// KeyJMAPInbox is the advisory-lock key for the JMAP inbox consumer: only one
// replica should hold a JMAP session against the shared mailbox at a time.
const KeyJMAPInbox int64 = 0x5001_0001

// DefaultRetryInterval is how often a non-holder re-contends for the lock. It
// doubles as the failover delay when a holder dies, so it is short enough that
// inbound email is not stalled for long and long enough that a fleet of
// replicas is not hammering the database.
const DefaultRetryInterval = 30 * time.Second

// livenessInterval is how often the holder pings its own lock connection. A
// session advisory lock is released the moment its connection dies, so a
// holder that cannot detect that would keep running while another replica
// legitimately takes over.
const livenessInterval = 10 * time.Second

// ErrNilDB is returned when RunExclusive is handed no database handle.
var ErrNilDB = errors.New("dblock: nil database")

// RunExclusive runs work while this process holds the advisory lock for key,
// and blocks until ctx is canceled.
//
// On SQLite it simply runs work: a SQLite deployment is one process by
// construction, so there is nothing to exclude and no shared lock manager to
// do it with.
//
// On Postgres it acquires the lock on a DEDICATED connection — session
// advisory locks live and die with the connection that took them, so the lock
// must never be taken through the pool where a recycled connection would drop
// it invisibly — then runs work on a context derived from ctx. When the lock is
// unavailable it retries every retryInterval; that same loop is the failover
// path when a holder dies. If the holding connection breaks, work's context is
// canceled and the loop goes back to contending. The lock is released on
// graceful shutdown.
//
// work returning an error is not fatal to the loop's ownership: the error is
// logged, the lock released, and the loop re-contends after retryInterval, so
// a crashing supervisor hands the work to another replica instead of sitting
// on the lock.
func RunExclusive(
	ctx context.Context,
	database *bun.DB,
	key int64,
	retryInterval time.Duration,
	logger *slog.Logger,
	work func(context.Context) error,
) error {
	if database == nil {
		return ErrNilDB
	}

	if logger == nil {
		logger = slog.Default()
	}

	if retryInterval <= 0 {
		retryInterval = DefaultRetryInterval
	}

	if _, isPostgres := database.Dialect().(*pgdialect.Dialect); !isPostgres {
		logger.InfoContext(ctx, "advisory lock skipped on non-Postgres backend; running exclusively by construction",
			"key", key)

		return work(ctx)
	}

	return contend(ctx, database, key, retryInterval, logger, work)
}

// contend is the Postgres acquire → run → release → retry loop.
func contend(
	ctx context.Context,
	database *bun.DB,
	key int64,
	retryInterval time.Duration,
	logger *slog.Logger,
	work func(context.Context) error,
) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}

		conn, acquired, err := tryAcquire(ctx, database, key)
		if err != nil {
			logger.WarnContext(ctx, "advisory lock acquisition failed", "key", key, "error", err)
		}

		if !acquired {
			if !sleepOrDone(ctx, retryInterval) {
				return ctx.Err()
			}

			continue
		}

		logger.InfoContext(ctx, "advisory lock acquired; running exclusive work", "key", key)

		runErr := runHolding(ctx, conn, key, logger, work)

		// release deliberately builds its own context: on shutdown the
		// caller's is already canceled, and the unlock would be skipped.
		//nolint:contextcheck // see above
		release(conn, key, logger)

		if ctx.Err() != nil {
			return ctx.Err()
		}

		if runErr != nil && !errors.Is(runErr, context.Canceled) {
			logger.WarnContext(ctx, "exclusive work exited with error; will re-contend",
				"key", key, "error", runErr)
		}

		if !sleepOrDone(ctx, retryInterval) {
			return ctx.Err()
		}
	}
}

// runHolding runs work with a context that is canceled if the lock connection
// dies, so the work stops instead of continuing without the lock it believes
// it holds.
func runHolding(
	ctx context.Context,
	conn bun.Conn,
	key int64,
	logger *slog.Logger,
	work func(context.Context) error,
) error {
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	watchDone := make(chan struct{})

	go func() {
		defer close(watchDone)

		watchConnection(runCtx, conn, key, logger, cancel)
	}()

	err := work(runCtx)

	cancel()
	<-watchDone

	return err
}

// watchConnection pings the lock connection until it fails or the context
// ends, canceling the work on failure.
func watchConnection(
	ctx context.Context, conn bun.Conn, key int64, logger *slog.Logger, cancel context.CancelFunc,
) {
	ticker := time.NewTicker(livenessInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := conn.PingContext(ctx); err != nil {
				if ctx.Err() != nil {
					return
				}

				logger.WarnContext(ctx, "advisory lock connection lost; stopping exclusive work",
					"key", key, "error", err)
				cancel()

				return
			}
		}
	}
}

// tryAcquire pins a connection and attempts pg_try_advisory_lock on it. When
// the lock is held elsewhere the connection is handed straight back to the
// pool and (zero Conn, false, nil) is reported — a busy lock is an ordinary
// outcome, not an error, and holding the pinned connection while losing would
// cost one pool slot per contender.
func tryAcquire(ctx context.Context, database *bun.DB, key int64) (bun.Conn, bool, error) {
	conn, err := database.Conn(ctx)
	if err != nil {
		return bun.Conn{}, false, fmt.Errorf("dblock: pin connection: %w", err)
	}

	var acquired bool
	if err := conn.QueryRowContext(ctx, "SELECT pg_try_advisory_lock(?)", key).Scan(&acquired); err != nil {
		_ = conn.Close()

		return bun.Conn{}, false, fmt.Errorf("dblock: pg_try_advisory_lock: %w", err)
	}

	if !acquired {
		_ = conn.Close()

		return bun.Conn{}, false, nil
	}

	return conn, true, nil
}

// release unlocks and returns the dedicated connection to the pool. It uses a
// fresh short context because the caller's is typically already canceled by
// the time shutdown reaches here — without that, the unlock would be skipped
// and the lock would linger until the connection was reaped.
func release(conn bun.Conn, key int64, logger *slog.Logger) {
	releaseCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if _, err := conn.ExecContext(releaseCtx, "SELECT pg_advisory_unlock(?)", key); err != nil {
		logger.WarnContext(releaseCtx, "advisory lock release failed", "key", key, "error", err)
	}

	_ = conn.Close()
}

// sleepOrDone returns false if ctx was canceled before d elapsed.
func sleepOrDone(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
