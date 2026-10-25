package uptimereport

import (
	"math"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/uptimebar"
)

// Delta colors. Green is reserved for a GENUINE improvement and gray is the
// default, not an afterthought: the competitor report this spec reacts to
// painted a monitor that was down all month with a green "0.00% vs prev.
// month". Zero is never green, and "not comparable" is never green either.
const (
	deltaGoodColor    = "#15803d"
	deltaBadColor     = "#b91c1c"
	deltaNeutralColor = "#6b7280"
)

// availabilityDeltaPrecision is how many decimals an availability movement is
// reported to, matching the availability figures (pctDecimals). A movement
// that rounds away at this precision IS "no change": reporting "+0.000 pts"
// would be noise dressed up as a trend.
const availabilityDeltaPrecision = pctDecimals

// responseDeltaPrecision is how many decimals a response-time movement is
// reported to, in percent.
const responseDeltaPrecision = 1

// deltaColor maps a movement to its color. `improvement` is the movement
// expressed so that POSITIVE means better, whatever the underlying metric's
// direction is (availability up is better, response time down is better).
// Exactly zero is neutral — never green.
func deltaColor(improvement float64) string {
	switch {
	case improvement > 0:
		return deltaGoodColor
	case improvement < 0:
		return deltaBadColor
	default:
		return deltaNeutralColor
	}
}

// roundTo rounds to `decimals` places, so a delta that is zero at the precision
// we PRINT is treated as zero everywhere — color included.
func roundTo(value float64, decimals int) float64 {
	factor := math.Pow(10, float64(decimals))

	return math.Round(value*factor) / factor
}

// isDownAllPeriod reports whether a scope measured data and every single probe
// failed. It is deliberately NOT "availability is low": the guards this drives
// (suppress the latency block, suppress the response-time delta) exist because
// response times of error responses are noise, and that only holds at exactly
// zero.
func isDownAllPeriod(stats uptimebar.BucketStats) bool {
	pct, ok := stats.AvailabilityPct()

	return ok && pct == 0
}

// trendInputs is everything the period-over-period block needs, so the
// suppression rules live in ONE place instead of being re-derived per stat.
type trendInputs struct {
	current  uptimebar.BucketStats
	previous uptimebar.BucketStats

	currentIncidents  int
	previousIncidents int
}

// applyTrend fills the delta flags and figures, or leaves every Show* flag
// false.
//
// The load-bearing rule: with no previous-period data (a new org, a new check,
// a schedule's first run) NOTHING is rendered — not "±0.00%", not a gray dash.
// An empty baseline is not a measurement of "no change".
func applyTrend(data *Data, inputs *trendInputs) {
	prevPct, hasPrevious := inputs.previous.AvailabilityPct()
	if !hasPrevious {
		return
	}

	data.HasPreviousData = true
	data.Figures.PreviousAvailability = prevPct
	data.PreviousIncidentCount = inputs.previousIncidents

	if curPct, ok := inputs.current.AvailabilityPct(); ok {
		delta := roundTo(curPct-prevPct, availabilityDeltaPrecision)
		data.ShowAvailabilityDelta = true
		data.Figures.AvailabilityDelta = delta
		data.AvailabilityDeltaColor = deltaColor(delta)
	}

	incidentDelta := inputs.currentIncidents - inputs.previousIncidents
	data.ShowIncidentDelta = true
	data.Figures.IncidentDelta = incidentDelta
	// Fewer incidents is better, so the improvement is the negated movement.
	data.IncidentDeltaColor = deltaColor(float64(-incidentDelta))

	applyResponseDelta(data, inputs)
}

// applyResponseDelta fills the response-time delta, or nothing.
//
// Suppressed when either period was down end to end: a "-13% response time"
// measured on error responses is the celebrated-nonsense failure mode this spec
// exists to avoid. Also suppressed when either period recorded no duration at
// all, or when the baseline is zero (nothing to take a percentage of).
func applyResponseDelta(data *Data, inputs *trendInputs) {
	if isDownAllPeriod(inputs.current) || isDownAllPeriod(inputs.previous) {
		return
	}

	currentAvg, hasCurrent := inputs.current.AvgDuration()
	previousAvg, hasPrevious := inputs.previous.AvgDuration()

	if !hasCurrent || !hasPrevious || previousAvg <= 0 {
		return
	}

	delta := roundTo((currentAvg-previousAvg)/previousAvg*100, responseDeltaPrecision)

	data.ShowResponseDelta = true
	data.Figures.ResponseDelta = delta
	// Faster is better, so the improvement is the negated movement.
	data.ResponseDeltaColor = deltaColor(-delta)
	data.Figures.PreviousAvgResponse = previousAvg
}

// applyLatency fills the response-time figures, or leaves HasLatency false.
//
// Suppressed for a scope that was down the whole period (see isDownAllPeriod)
// and for a scope that measured no duration at all. A grid of zeros is worse
// than no grid.
func applyLatency(data *Data, overall uptimebar.BucketStats) {
	if isDownAllPeriod(overall) {
		return
	}

	avg, ok := overall.AvgDuration()
	if !ok {
		return
	}

	data.HasLatency = true
	data.Figures.AvgResponse = avg

	if low, high, hasRange := overall.DurationRange(); hasRange {
		data.Figures.HasResponseRange = true
		data.Figures.MinResponse = low
		data.Figures.MaxResponse = high
	}

	data.Figures.SlowSamples = overall.SlowSamples
	data.Figures.SlowPeaks = overall.SlowPeaks
}

// Day-strip colors. Discrete states, not the continuous availabilityTextColor
// ramp: a 6-pixel cell cannot carry a gradient, and discrete colors also let
// identical neighbors collapse into one run-length-encoded cell, which is what
// keeps a 50-check monthly report far below Gmail's clipping limit.
const (
	dayNoDataColor = "#d1d5db"
	dayGoodColor   = "#15803d"
	dayWarnColor   = "#b45309"
	dayPoorColor   = "#c2410c"
	dayBadColor    = "#b91c1c"
)

// dayStripFloor is the availability below which a day cell reads fully red,
// matching availabilityTextColor's red floor.
const dayStripFloor = 95.0

// dayStripColor maps one day's availability to a cell color. A day with no
// rows at all is GRAY, never red: a check created mid-period, or a check whose
// data has aged out, did not fail on the days it did not exist.
func dayStripColor(stats uptimebar.BucketStats) string {
	pct, ok := stats.AvailabilityPct()
	if !ok {
		return dayNoDataColor
	}

	switch {
	case pct >= models.DefaultAvailabilityThresholdUp:
		return dayGoodColor
	case pct >= models.DefaultAvailabilityThresholdDegraded:
		return dayWarnColor
	case pct >= dayStripFloor:
		return dayPoorColor
	default:
		return dayBadColor
	}
}
