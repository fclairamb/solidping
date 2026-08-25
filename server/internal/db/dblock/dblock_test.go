package dblock_test

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/dblock"
	"github.com/fclairamb/solidping/server/internal/db/postgres"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
)

// portDBLock is distinct from every other embedded-Postgres port in this repo
// (see the port-numbering note in postgres_headroom_postgres_test.go).
const portDBLock = 15491

// testKey is a throwaway key well outside the registry's allocated range, so
// a test can never collide with a real lock taken by something else on the
// same development database.
const testKey int64 = 0x5001_FF01

// TestRunExclusiveOnSQLiteRunsWithoutALock pins the single-process rule: a
// SQLite deployment has no shared lock manager, and refusing to run there
// would silently disable inbound email for every self-hosted install.
func TestRunExclusiveOnSQLiteRunsWithoutALock(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := t.Context()

	svc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(svc.Initialize(ctx))
	t.Cleanup(func() { _ = svc.Close() })

	var ran atomic.Int32

	runErr := dblock.RunExclusive(ctx, svc.DB(), testKey, time.Second, nil,
		func(context.Context) error {
			ran.Add(1)

			return nil
		})

	r.NoError(runErr)
	r.Equal(int32(1), ran.Load(), "the work must run on SQLite, lock or no lock")
}

func TestRunExclusiveRejectsNilDatabase(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	err := dblock.RunExclusive(t.Context(), nil, testKey, time.Second, nil,
		func(context.Context) error { return nil })
	r.ErrorIs(err, dblock.ErrNilDB)
}

// TestAdvisoryLockAdmitsOneHolderAndFailsOver is the layer-2 test: two
// contenders, one runs; when the holder goes away the other takes over within
// the retry interval. Failover is the reason this is an advisory lock rather
// than a static role flag, so it is asserted, not assumed.
//
//nolint:paralleltest // shares dev-machine resources (embedded-postgres-go's pwfile extraction) with its siblings
func TestAdvisoryLockAdmitsOneHolderAndFailsOver(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping embedded-postgres test in -short mode")
	}

	ctx := t.Context()
	r := require.New(t)

	svc, err := postgres.New(ctx, &postgres.Config{
		Embedded: true,
		Port:     portDBLock,
		RunMode:  "test",
	})
	if err != nil {
		t.Skipf("embedded postgres unavailable: %v", err)
	}

	t.Cleanup(func() { _ = svc.Close() })

	if initErr := svc.Initialize(ctx); initErr != nil {
		t.Skipf("embedded postgres init failed: %v", initErr)
	}

	const retry = 200 * time.Millisecond

	var running atomic.Int32

	// firstCtx is what we cancel to make the leader "die".
	firstCtx, killFirst := context.WithCancel(ctx)
	defer killFirst()

	secondCtx, stopSecond := context.WithCancel(ctx)
	defer stopSecond()

	firstStarted := make(chan struct{})
	secondStarted := make(chan struct{})

	var (
		firstOnce  atomic.Bool
		secondOnce atomic.Bool
	)

	go func() {
		_ = dblock.RunExclusive(firstCtx, svc.DB(), testKey, retry, nil,
			func(runCtx context.Context) error {
				running.Add(1)

				if firstOnce.CompareAndSwap(false, true) {
					close(firstStarted)
				}

				<-runCtx.Done()
				running.Add(-1)

				return runCtx.Err()
			})
	}()

	select {
	case <-firstStarted:
	case <-time.After(10 * time.Second):
		t.Fatal("the first contender never acquired the lock")
	}

	go func() {
		_ = dblock.RunExclusive(secondCtx, svc.DB(), testKey, retry, nil,
			func(runCtx context.Context) error {
				running.Add(1)

				if secondOnce.CompareAndSwap(false, true) {
					close(secondStarted)
				}

				<-runCtx.Done()
				running.Add(-1)

				return runCtx.Err()
			})
	}()

	// Give the second contender several retry intervals to (not) get in.
	time.Sleep(5 * retry)

	select {
	case <-secondStarted:
		t.Fatal("two holders ran at once: the advisory lock admitted a second consumer")
	default:
	}

	r.Equal(int32(1), running.Load(), "exactly one holder while the leader is alive")

	// The leader goes away; the lock must be released and picked up.
	killFirst()

	select {
	case <-secondStarted:
	case <-time.After(10 * time.Second):
		t.Fatal("the surviving contender never took over after the holder died")
	}

	stopSecond()
}

// TestAdvisoryLockIsReleasedOnShutdown proves the release path: after the
// holder's context ends, a fresh contender must be able to take the key
// immediately rather than waiting for the connection to be reaped.
//
//nolint:paralleltest // shares dev-machine resources (embedded-postgres-go's pwfile extraction) with its siblings
func TestAdvisoryLockIsReleasedOnShutdown(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping embedded-postgres test in -short mode")
	}

	ctx := t.Context()
	r := require.New(t)

	svc, err := postgres.New(ctx, &postgres.Config{
		Embedded: true,
		Port:     portDBLock + 1,
		RunMode:  "test",
	})
	if err != nil {
		t.Skipf("embedded postgres unavailable: %v", err)
	}

	t.Cleanup(func() { _ = svc.Close() })

	if initErr := svc.Initialize(ctx); initErr != nil {
		t.Skipf("embedded postgres init failed: %v", initErr)
	}

	holderCtx, stopHolder := context.WithCancel(ctx)

	started := make(chan struct{})
	done := make(chan struct{})

	go func() {
		defer close(done)

		_ = dblock.RunExclusive(holderCtx, svc.DB(), testKey, time.Second, nil,
			func(runCtx context.Context) error {
				close(started)
				<-runCtx.Done()

				return runCtx.Err()
			})
	}()

	<-started
	stopHolder()
	<-done

	// The key must be free again straight away.
	takenCtx, stopTaken := context.WithCancel(ctx)
	defer stopTaken()

	taken := make(chan struct{})

	go func() {
		_ = dblock.RunExclusive(takenCtx, svc.DB(), testKey, 100*time.Millisecond, nil,
			func(runCtx context.Context) error {
				close(taken)
				<-runCtx.Done()

				return runCtx.Err()
			})
	}()

	select {
	case <-taken:
	case <-time.After(10 * time.Second):
		r.FailNow("the lock was not released when its holder shut down")
	}
}

// TestAdvisoryLockFailsOverWhenTheHolderConnectionDies is the spec's literal
// failure case: not a graceful shutdown, but the holder's connection being
// killed underneath it. Postgres drops a session advisory lock the instant its
// connection dies, so the danger is a holder that keeps working while another
// replica legitimately takes the key — two live JMAP consumers, which is the
// bug this layer exists to prevent. Both halves are asserted: the takeover
// happens, and the ex-holder stops.
//
//nolint:paralleltest // shares dev-machine resources (embedded-postgres-go's pwfile extraction) with its siblings
func TestAdvisoryLockFailsOverWhenTheHolderConnectionDies(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping embedded-postgres test in -short mode")
	}

	ctx := t.Context()
	r := require.New(t)

	svc, err := postgres.New(ctx, &postgres.Config{
		Embedded: true,
		Port:     portDBLock + 2,
		RunMode:  "test",
	})
	if err != nil {
		t.Skipf("embedded postgres unavailable: %v", err)
	}

	t.Cleanup(func() { _ = svc.Close() })

	if initErr := svc.Initialize(ctx); initErr != nil {
		t.Skipf("embedded postgres init failed: %v", initErr)
	}

	const retry = 200 * time.Millisecond

	holderCtx, stopHolder := context.WithCancel(ctx)
	defer stopHolder()

	contenderCtx, stopContender := context.WithCancel(ctx)
	defer stopContender()

	holderStarted := make(chan struct{})
	holderStopped := make(chan struct{})
	contenderStarted := make(chan struct{})

	var (
		holderOnce    atomic.Bool
		contenderOnce atomic.Bool
	)

	go func() {
		_ = dblock.RunExclusive(holderCtx, svc.DB(), testKey, time.Hour, nil,
			func(runCtx context.Context) error {
				if holderOnce.CompareAndSwap(false, true) {
					close(holderStarted)
				}

				<-runCtx.Done()

				close(holderStopped)

				return runCtx.Err()
			})
	}()

	select {
	case <-holderStarted:
	case <-time.After(10 * time.Second):
		t.Fatal("the holder never acquired the lock")
	}

	go func() {
		_ = dblock.RunExclusive(contenderCtx, svc.DB(), testKey, retry, nil,
			func(runCtx context.Context) error {
				if contenderOnce.CompareAndSwap(false, true) {
					close(contenderStarted)
				}

				<-runCtx.Done()

				return runCtx.Err()
			})
	}()

	time.Sleep(3 * retry)

	select {
	case <-contenderStarted:
		t.Fatal("the contender ran while the lock was held")
	default:
	}

	// Kill the holder's session from the server side.
	var terminated bool

	killErr := svc.DB().NewRaw(
		`SELECT pg_terminate_backend(pid) FROM pg_locks
		 WHERE locktype = 'advisory' AND ((classid::bigint << 32) | objid::bigint) = ?`,
		testKey,
	).Scan(ctx, &terminated)
	r.NoError(killErr)
	r.True(terminated, "the holder's backend must have been found and terminated")

	select {
	case <-contenderStarted:
	case <-time.After(10 * time.Second):
		t.Fatal("the contender never acquired the freed lock")
	}

	// And the ex-holder must notice it no longer owns the key.
	select {
	case <-holderStopped:
	case <-time.After(30 * time.Second):
		t.Fatal("the ex-holder kept running after losing its lock connection")
	}
}
