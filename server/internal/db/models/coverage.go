package models

import "time"

// ExpectedProbes is how many real results a check should produce over
// `window`: window / period × max(1, regions) — the same shape as the
// entitlements checks-per-minute rate (each selected region runs the check
// every period; a check with no region still runs once). Spec 2026-09-25-02.
func ExpectedProbes(window, period time.Duration, regionCount int) float64 {
	if window <= 0 || period <= 0 {
		return 0
	}

	if regionCount < 1 {
		regionCount = 1
	}

	return float64(window) / float64(period) * float64(regionCount)
}

// ExpectedProbes is the check's own expected result count over `window`.
func (c *Check) ExpectedProbes(window time.Duration) float64 {
	return ExpectedProbes(window, time.Duration(c.Period), len(c.Regions))
}

// ExpectedProbesBetween is the expected result count over [start, end),
// clamped to the check's lifetime so far — no probe is expected before the
// check existed, nor after `now`.
func (c *Check) ExpectedProbesBetween(start, end, now time.Time) float64 {
	if c.CreatedAt.After(start) {
		start = c.CreatedAt
	}

	if now.Before(end) {
		end = now
	}

	return c.ExpectedProbes(end.Sub(start))
}

// Coverage is measured ÷ expected, clamped to [0, 1]: the share of the time a
// window was actually measured. ok is false when nothing was expected (a
// zero-length window), where coverage has no meaning. A result count above the
// expectation (jitter, a period change mid-window, retries) reads as full
// coverage, never more.
func Coverage(measured int, expected float64) (float64, bool) {
	if expected <= 0 {
		return 0, false
	}

	coverage := float64(measured) / expected
	if coverage > 1 {
		coverage = 1
	}

	if coverage < 0 {
		coverage = 0
	}

	return coverage, true
}
