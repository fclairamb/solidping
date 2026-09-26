package checkbrowser

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

// TestScreenshotStarvesWithoutTheWorkersExtraBudget pins the root cause behind
// spec 2026-09-25-35, "A browser check that fails by timing out never keeps
// its screenshot".
//
// Execute's own session/probe split (see the "TWO nested budgets" comment
// above) is correct and untouched by that fix — the session already outlives
// the probe by cfg.ExtraBudget. The bug lived entirely in the WORKER, which
// used to hand Execute a parent context capped at checkTimeout + 1s and left
// no room for that extra session budget: sessionCtx is a CHILD of the
// caller's context, so a stingy parent caps it away regardless of what this
// checker asks for.
//
// This test proves the contract directly, with no worker involved: feed
// Execute the OLD worker shape (checkTimeout + a flat margin, nothing more)
// and a capture realistic enough to need more than that margin starves; feed
// it the FIXED shape (the same margin PLUS cfg.ExtraBudget — exactly what
// checkworker.resolveExtraBudget now adds to the real execCtx, see
// worker.go) and the identical capture succeeds.
//
// It cannot compile before checkerdef.ExtraBudgeter / BrowserConfig.
// ExtraBudget exist (this is the regression test the spec asks for: it fails
// before the fix), and the "old shape" subtest keeps failing at runtime for
// as long as this checker's own budget math has anything to give that a
// stingy caller does not pass through — which is exactly the property the
// worker-side fix (checkworker.resolveExtraBudget) depends on.
func TestScreenshotStarvesWithoutTheWorkersExtraBudget(t *testing.T) {
	t.Parallel()

	// Scaled down ~100x from production (probe timeout up to 30s, the
	// worker's flat "+1s" margin, a 5s screenshot budget, a real capture
	// measured around 0.34s) so the test runs in well under a second while
	// keeping the one ratio that matters: the simulated capture takes longer
	// than the flat margin alone, but comfortably less than margin + the
	// checker's own ExtraBudget.
	const (
		probeTimeout   = 30 * time.Millisecond
		flatMargin     = 150 * time.Millisecond // stands in for the worker's old flat "+1s"
		captureLatency = 300 * time.Millisecond // stands in for a real ~0.34s capture
	)

	spec := &BrowserConfig{URL: testPageURL, Timeout: probeTimeout, Screenshot: true}

	run := func(t *testing.T, parentBudget time.Duration) *checkerdef.Result {
		t.Helper()

		r := require.New(t)

		parentCtx, cancel := context.WithTimeout(t.Context(), parentBudget)
		defer cancel()

		checker := &BrowserChecker{
			session: func(
				ctx context.Context, _ *BrowserConfig, start time.Time, metrics, output map[string]any,
			) *checkerdef.Result {
				// Burn the whole probe budget, exactly like a waitSelector
				// that never matches or a page that hangs — the single most
				// common capture-worthy failure, per the spec.
				<-ctx.Done()

				return &checkerdef.Result{
					Status: checkerdef.StatusTimeout, Duration: time.Since(start),
					Metrics: metrics, Output: output,
				}
			},
			screenshot: func(ctx context.Context) (Capture, error) {
				select {
				case <-time.After(captureLatency):
					return fakeCapture(), nil
				case <-ctx.Done():
					return Capture{}, ctx.Err()
				}
			},
		}

		result, err := checker.Execute(parentCtx, spec)
		r.NoError(err)
		r.Equal(checkerdef.StatusTimeout, result.Status, "the capture must never change the verdict")

		return result
	}

	t.Run("old worker shape: checkTimeout plus a flat margin only", func(t *testing.T) {
		t.Parallel()

		result := run(t, probeTimeout+flatMargin)
		require.Nil(t, result.Diagnostics,
			"the capture starves: this IS the bug spec 2026-09-25-35 reports")
	})

	t.Run("fixed worker shape: flat margin plus the checker's own ExtraBudget", func(t *testing.T) {
		t.Parallel()

		r := require.New(t)

		result := run(t, probeTimeout+flatMargin+spec.ExtraBudget(false))
		r.NotNil(result.Diagnostics)
		r.NotNil(result.Diagnostics.Screenshot,
			"granting ExtraBudget on the hard deadline, as checkworker.resolveExtraBudget now does, is the fix")
	})
}
