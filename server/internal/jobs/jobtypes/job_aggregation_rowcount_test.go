package jobtypes

import (
	"context"
	"errors"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/jobs/jobdef"
	"github.com/fclairamb/solidping/server/internal/prommetrics"
)

// errTestBoom is a static test-only error (err113 forbids dynamic
// errors.New at the call site).
var errTestBoom = errors.New("boom")

// stubResultsCountDB satisfies db.Service by embedding a nil interface (any
// method besides CountResultsByPeriodType panics if called — none of the
// tests below exercise anything else).
type stubResultsCountDB struct {
	db.Service
	counts map[string]int64
	err    error
}

func (s *stubResultsCountDB) CountResultsByPeriodType(_ context.Context) (map[string]int64, error) {
	return s.counts, s.err
}

// TestMaybeSampleResultsRowCountThrottles proves the sampler runs at most
// once per resultsRowCountSampleInterval, process-wide, regardless of how
// many times (or how many different orgs') aggregation runs call it — the
// whole point of tying a table-wide COUNT(*) to a throttle rather than
// running it on every job invocation.
func TestMaybeSampleResultsRowCountThrottles(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	var calls atomic.Int32

	restore := sampleResultsRowCountFunc
	sampleResultsRowCountFunc = func(_ context.Context, _ *jobdef.JobContext) {
		calls.Add(1)
	}

	t.Cleanup(func() { sampleResultsRowCountFunc = restore })

	resultsRowCountThrottle.mu.Lock()
	resultsRowCountThrottle.last = time.Time{}
	resultsRowCountThrottle.mu.Unlock()

	jctx := &jobdef.JobContext{Logger: slog.Default()}
	ctx := t.Context()

	maybeSampleResultsRowCount(ctx, jctx) // due: zero value "last"
	maybeSampleResultsRowCount(ctx, jctx) // not due: just sampled
	maybeSampleResultsRowCount(ctx, jctx) // still not due

	r.Equal(int32(1), calls.Load(), "throttled sampler must run exactly once inside the interval")

	// Force the interval to have elapsed and confirm it fires again.
	resultsRowCountThrottle.mu.Lock()
	resultsRowCountThrottle.last = time.Now().Add(-2 * resultsRowCountSampleInterval)
	resultsRowCountThrottle.mu.Unlock()

	maybeSampleResultsRowCount(ctx, jctx)
	r.Equal(int32(2), calls.Load(), "sampler must run again once the interval has elapsed")
}

// TestDefaultSampleResultsRowCount covers both the zero-fill behavior (a
// drained tier absent from CountResultsByPeriodType's map must still land on
// the gauge as 0, not go stale/absent) and DB-error tolerance (a sampling
// failure must be logged, never panic or propagate) in one sequential test —
// both touch the shared solidping_results_rows gauge, so they must not
// run concurrently with each other.
func TestDefaultSampleResultsRowCount(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	stub := &stubResultsCountDB{counts: map[string]int64{
		periodRaw:  123,
		periodHour: 45,
		// periodDay and periodMonth deliberately absent (drained tiers).
	}}
	jctx := &jobdef.JobContext{DBService: stub, Logger: slog.Default()}

	defaultSampleResultsRowCount(t.Context(), jctx)

	r.InDelta(123.0, testutil.ToFloat64(prommetrics.ResultsRowCount.WithLabelValues(periodRaw)), 0.001)
	r.InDelta(45.0, testutil.ToFloat64(prommetrics.ResultsRowCount.WithLabelValues(periodHour)), 0.001)
	r.InDelta(0.0, testutil.ToFloat64(prommetrics.ResultsRowCount.WithLabelValues(periodDay)), 0.001)
	r.InDelta(0.0, testutil.ToFloat64(prommetrics.ResultsRowCount.WithLabelValues(periodMonth)), 0.001)

	// A DB error must be logged and swallowed, never panic or propagate —
	// this gauge sample must never fail the aggregation job itself.
	errStub := &stubResultsCountDB{err: errTestBoom}
	errJctx := &jobdef.JobContext{DBService: errStub, Logger: slog.Default()}

	r.NotPanics(func() {
		defaultSampleResultsRowCount(t.Context(), errJctx)
	})
}
