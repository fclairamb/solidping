package uptimebar

import "github.com/fclairamb/solidping/server/internal/db/models"

// The per-bucket availability vocabulary spoken by the surfaces that classify
// server-side: the public status page, the bucketed availability endpoint
// behind dash0's chart strip, and (via that endpoint) dash0's uptime strips.
// Those three share Classify below, so identical numbers cannot be painted
// different colors on any of them.
//
// The badge SVG is deliberately NOT one of them — see Classify's doc comment.
//
// They are deliberately the same strings the status-page payload already
// shipped (statuspages' statusNoData/statusUp/statusDegraded/statusDownValue),
// so moving the classifier here changed no wire format.
const (
	// StatusNoData is a bucket with no rows in the shared raw+hour+day union.
	// It is NOT 100% and must never be rendered as green — the front ends paint
	// it gray.
	StatusNoData = "noData"
	// StatusUp is availability at or above the up threshold.
	StatusUp = "up"
	// StatusDegraded is availability below the up threshold but not bad enough
	// (or not sampled enough) to call red.
	StatusDegraded = "degraded"
	// StatusDown is availability below the degraded threshold with more than one
	// failed sample.
	StatusDown = "down"
)

// DefaultThresholds returns the platform's default up/degraded availability
// thresholds (99.9 / 99.0). Surfaces with no per-page configuration — the check
// detail chart's availability strip — classify against these, so a bucket that
// reads amber on a status page reads amber on the chart too.
func DefaultThresholds() (float64, float64) {
	return models.DefaultAvailabilityThresholdUp, models.DefaultAvailabilityThresholdDegraded
}

// Classify maps a bucket's availability percentage onto the wire vocabulary
// above, using the caller's effective thresholds (a status page resolves its own
// via models.StatusPageSettings.EffectiveThresholds; everything else uses
// DefaultThresholds).
//
// Small-bucket calibration guard (spec 2026-08-03-01 §4): a bucket with exactly
// ONE failed sample never renders "down", only at worst "degraded" — red
// requires >= 2 failed samples. failures is stats.Total - stats.Up. This fixes
// the hourly "one failed minute = red hour" cliff without changing
// percentage-threshold behavior on buckets large enough that one sample cannot
// swing the classification anyway.
//
// This lives in uptimebar rather than in one of its consumers on purpose: the
// package already owns the counting rules every strip shares, so the color
// mapping belongs next to them.
//
// EXCEPTION, and it is a real one: the badge SVG's uptime bar does NOT use this
// function. badges.uptimeBarColor (internal/handlers/badges/service.go) keeps
// its own FOUR-tier scale — green, yellow, an extra orange band at >= 98%, then
// red — and applies no small-bucket guard, so it genuinely disagrees with
// Classify on ordinary numbers (98.5% is orange there and "down" here; a single
// failed sample at 50% is red there and "degraded" here). That is deliberate,
// not drift: badges are check-scoped with no status-page context and stay on
// the global default thresholds (spec 2026-08-03-01 Decisions), and converging
// them would silently repaint every badge already embedded in the wild. What
// the badge shares with this package is the BUCKETING engine, not the color
// mapping. Do not "fix" one to match the other without a spec that asks for it.
func Classify(pct float64, failures int, upThreshold, degradedThreshold float64) string {
	switch {
	case pct >= upThreshold:
		return StatusUp
	case pct >= degradedThreshold:
		return StatusDegraded
	case failures <= 1:
		return StatusDegraded
	default:
		return StatusDown
	}
}

// ClassifyStats is Classify applied to a bucket straight from the engine: it
// resolves "no data" (Total == 0) to StatusNoData instead of dividing by zero,
// which is the single rule the spec insists on — an empty bucket is gray, never
// a manufactured 100%.
func ClassifyStats(stats BucketStats, upThreshold, degradedThreshold float64) string {
	pct, ok := stats.AvailabilityPct()
	if !ok {
		return StatusNoData
	}

	return Classify(pct, stats.Total-stats.Up, upThreshold, degradedThreshold)
}
