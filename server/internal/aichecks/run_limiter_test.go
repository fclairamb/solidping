package aichecks

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkers/checkbrowser"
	"github.com/fclairamb/solidping/server/internal/testruns"
)

// TestAuthoringLoopRunsWaitForTheSharedLimiter: with the server's limiter,
// every run of the authoring loop waits for one of the org's slots (spec
// 2026-11-19-03) and its browsers count against the test-run budget.
func TestAuthoringLoopRunsWaitForTheSharedLimiter(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	svc := NewService(&Options{})
	r.Nil(svc.loopAdmit("org-a"), "no limiter, no admission")

	limiter := testruns.New()
	svc.SetRunLimiter(limiter)

	rec := &runRecorder{admit: svc.loopAdmit("org-a")}

	// Org A already has a run in flight (a REST test run, say).
	held, err := limiter.Acquire("org-a")
	r.NoError(err)

	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()

	_, _, err = rec.admitted(ctx)
	r.Error(err, "the loop's run waits, then gives up with its context")
	r.Contains(err.Error(), "no run slot free")

	held()

	ctx, release, err := rec.admitted(t.Context())
	r.NoError(err)
	r.True(checkbrowser.IsTestRun(ctx))
	r.Equal(1, limiter.BusyOrgs())

	release()
	r.Zero(limiter.BusyOrgs())

	// Without admit, the recorder runs unrestricted, as before.
	ctx, release, err = (&runRecorder{}).admitted(t.Context())
	r.NoError(err)
	r.False(checkbrowser.IsTestRun(ctx))
	release()
}
