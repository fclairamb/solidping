package uptimebar

import "github.com/fclairamb/solidping/server/internal/db/models"

// The per-bucket availability vocabulary. Every surface that paints a bucket
// green/amber/red/gray — the public status page, the badge uptime bar and the
// dash0 chart availability strip — speaks these exact words, so the front ends
// have a single set of values to map onto colors.
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
// package already owns the counting rules every strip shares, and the color
// mapping is the last place two strips could disagree about the same numbers.
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
