package checkworker

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.uber.org/goleak"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

// leakIgnores lists goroutines that legitimately outlive individual tests and
// must not be flagged: process-lifetime background workers from dependencies
// (the modernc SQLite driver, the patrickmn/go-cache janitor). Matched narrowly
// by top function so a real worker-goroutine leak is still caught.
//
//nolint:gochecknoglobals // shared test fixture across TestMain and the lifecycle test
var leakIgnores = []goleak.Option{
	// modernc/SQLite (pure-Go driver) background goroutines.
	goleak.IgnoreTopFunction("modernc.org/sqlite.(*conn).step.func1"),
	goleak.IgnoreAnyFunction("modernc.org/libc.Xpthread_cond_wait"),
	goleak.IgnoreAnyFunction("modernc.org/libc.Xpthread_cond_timedwait"),
	// patrickmn/go-cache janitor: a process-lifetime cache-cleanup loop started
	// by a dependency, not owned by the worker lifecycle.
	goleak.IgnoreTopFunction("github.com/patrickmn/go-cache.(*janitor).Run"),
}

// TestMain runs the package's tests under goleak so a goroutine leaked by any
// checkworker test (e.g. the express runner or a notifier listener that
// outlives Run) fails CI rather than silently accumulating.
func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m, leakIgnores...)
}

// TestWorkerLifecycleNoGoroutineLeak asserts that a full Run → cancel cycle
// leaves no leaked goroutines once Run returns. This is the regression guard for
// the express-runner and notifier-listener leak hypotheses (B6): every goroutine
// the worker spawns is tracked by its WaitGroup and must exit on context cancel.
//
//nolint:paralleltest // goleak.VerifyNone inspects all process goroutines; must not run concurrently with other tests
func TestWorkerLifecycleNoGoroutineLeak(t *testing.T) {
	defer goleak.VerifyNone(t, leakIgnores...)

	runner, dbSvc, ctx := setupTestRunner(t)
	defer func() { _ = dbSvc.Close() }()

	org := models.NewOrganization("leak-org", "")
	require.NoError(t, dbSvc.CreateOrganization(ctx, org))

	runCtx, cancel := context.WithCancel(ctx)

	runDone := make(chan error, 1)
	go func() { runDone <- runner.Run(runCtx) }()

	// Let all worker goroutines (heartbeat, pool, fetcher, express) start.
	time.Sleep(200 * time.Millisecond)
	require.NotNil(t, runner.worker, "worker should be registered")

	cancel()

	select {
	case <-runDone:
		// Run returned; WaitGroup drained. goleak (deferred) now verifies no
		// worker goroutine outlived the cancel.
	case <-time.After(3 * time.Second):
		t.Fatal("worker Run did not return after cancel; a goroutine may be leaked")
	}
}
