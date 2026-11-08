package models

import (
	"math"
	"time"
)

// ExpectedProbes is a CAPACITY figure: how many results a check produces over
// `window` when every selected region runs it every period, window / period ×
// max(1, regions) — the same shape as the entitlements checks-per-minute rate
// (a check with no region still runs once). It is NOT a coverage denominator:
// the region set changes over a check's life (auto placement moves checks on
// its own), so applying today's count to a past window rewrote history (spec
// 2026-11-08-05). Coverage uses ExpectedPeriods.
func ExpectedProbes(window, period time.Duration, regionCount int) float64 {
	if regionCount < 1 {
		regionCount = 1
	}

	return ExpectedPeriods(window, period) * float64(regionCount)
}

// ExpectedPeriods is how many check periods fit in `window`: window / period,
// with no region multiplier. It is the denominator of data coverage, where a
// period counts as measured as soon as ANY region produced a result in it
// (spec 2026-11-08-05). A silent region is a region-health concern ("Region
// went dark", the per-region chart), not unmeasured time.
func ExpectedPeriods(window, period time.Duration) float64 {
	if window <= 0 || period <= 0 {
		return 0
	}

	return float64(window) / float64(period)
}

// ExpectedPeriodsBetween is the check's period count over [start, end),
// clamped to the check's lifetime so far — no probe is expected before the
// check existed, nor after `now`.
func (c *Check) ExpectedPeriodsBetween(start, end, now time.Time) float64 {
	if c.CreatedAt.After(start) {
		start = c.CreatedAt
	}

	if now.Before(end) {
		end = now
	}

	return ExpectedPeriods(end.Sub(start), time.Duration(c.Period))
}

// CoverageTally accumulates region-independent data coverage bucket by bucket
// (spec 2026-11-08-05).
//
// Expected is the period count over the whole monitored range — added once,
// independently of which buckets came back, so a bucket with no result at all
// counts as fully unmeasured. Covered is Σ over the buckets that did come back
// of min(results, ⌈periods in the bucket⌉): a bucket is measured in proportion
// to its results, capped at one result per period whatever the number of
// regions that produced them. The ceiling keeps a period longer than the
// bucket (a 6 h check read in 1 h buckets) or straddling two buckets from
// reading as partly unmeasured: one result in the bucket covers its period.
type CoverageTally struct {
	Covered  float64
	Expected float64
}

// AddExpected adds `periods` expected periods to the denominator.
func (t *CoverageTally) AddExpected(periods float64) {
	if periods > 0 {
		t.Expected += periods
	}
}

// AddBucket adds one bucket's results, capped at the periods that bucket
// spans. It does not touch Expected (see AddExpected).
func (t *CoverageTally) AddBucket(results int, bucketPeriods float64) {
	if results <= 0 || bucketPeriods <= 0 {
		return
	}

	t.Covered += math.Min(float64(results), math.Ceil(bucketPeriods))
}

// Add merges another tally (several checks behind one SLO or status resource).
func (t *CoverageTally) Add(other CoverageTally) {
	t.Covered += other.Covered
	t.Expected += other.Expected
}

// Coverage is Covered ÷ Expected, clamped to [0, 1]; ok is false when nothing
// was expected.
func (t CoverageTally) Coverage() (float64, bool) {
	return Coverage(t.Covered, t.Expected)
}

// Coverage is measured ÷ expected, clamped to [0, 1]: the share of the time a
// window was actually measured. ok is false when nothing was expected (a
// zero-length window), where coverage has no meaning. A measure above the
// expectation (jitter, a period change mid-window) reads as full coverage,
// never more.
func Coverage(measured, expected float64) (float64, bool) {
	if expected <= 0 {
		return 0, false
	}

	coverage := measured / expected
	if coverage > 1 {
		coverage = 1
	}

	if coverage < 0 {
		coverage = 0
	}

	return coverage, true
}
