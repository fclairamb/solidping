package uptimereport

import (
	"fmt"
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

// noChangeText is the phrasing for a zero delta. It is words rather than
// "+0.00%" on purpose — a signed zero reads as a measurement, and readers
// round it into "we improved".
const noChangeText = "no change"

// availabilityDeltaPrecision is how many decimals an availability movement is
// reported to, matching formatPct. A movement that rounds away at this
// precision IS "no change" — reporting "+0.000 pts" would be noise dressed up
// as a trend.
const availabilityDeltaPrecision = 3

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

// formatSignedFloat renders a movement with an explicit sign, or the "no
// change" wording when it rounds to zero.
func formatSignedFloat(value float64, decimals int, suffix string) string {
	rounded := roundTo(value, decimals)
	if rounded == 0 {
		return noChangeText
	}

	return fmt.Sprintf("%+.*f%s", decimals, rounded, suffix)
}

// formatSignedCount renders an integer movement with an explicit sign, or the
// "no change" wording at zero.
func formatSignedCount(value int) string {
	if value == 0 {
		return noChangeText
	}

	return fmt.Sprintf("%+d", value)
}

// formatMillis renders a response time. Below a second it is whole
// milliseconds (a tenth of a millisecond is noise on a network probe); at or
// above, seconds with two decimals, because "1483 ms" is harder to read at a
// glance than "1.48 s".
func formatMillis(millis float64) string {
	if millis >= 1000 {
		return fmt.Sprintf("%.2f s", millis/1000)
	}

	return fmt.Sprintf("%.0f ms", millis)
}

// slowThresholdLabel renders uptimebar.SlowSampleThresholdMillis for copy. A
// whole number of seconds reads as "1 s", not as formatMillis' "1.00 s" — the
// threshold is a round configured value, not a measurement.
func slowThresholdLabel() string {
	const millisPerSecond = 1000.0

	if math.Mod(uptimebar.SlowSampleThresholdMillis, millisPerSecond) == 0 {
		return fmt.Sprintf("%g s", uptimebar.SlowSampleThresholdMillis/millisPerSecond)
	}

	return formatMillis(uptimebar.SlowSampleThresholdMillis)
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

// applyTrend fills the delta fields, or leaves every Show* flag false.
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
	data.PreviousAvailabilityPct = formatPct(prevPct)
	data.PreviousIncidentCount = inputs.previousIncidents

	if curPct, ok := inputs.current.AvailabilityPct(); ok {
		delta := roundTo(curPct-prevPct, availabilityDeltaPrecision)
		data.ShowAvailabilityDelta = true
		data.AvailabilityDeltaText = formatSignedFloat(delta, availabilityDeltaPrecision, " pts")
		data.AvailabilityDeltaColor = deltaColor(delta)
	}

	incidentDelta := inputs.currentIncidents - inputs.previousIncidents
	data.ShowIncidentDelta = true
	data.IncidentDeltaText = formatSignedCount(incidentDelta)
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
	data.ResponseDeltaText = formatSignedFloat(delta, responseDeltaPrecision, "%")
	// Faster is better, so the improvement is the negated movement.
	data.ResponseDeltaColor = deltaColor(-delta)
	data.PreviousAvgResponseTime = formatMillis(previousAvg)
}

// applyLatency fills the response-time block, or leaves HasLatency false.
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
	data.AvgResponseTime = formatMillis(avg)

	if low, high, hasRange := overall.DurationRange(); hasRange {
		data.MinResponseTime = formatMillis(low)
		data.MaxResponseTime = formatMillis(high)
	}

	data.SlowLine = slowLine(overall.SlowSamples, overall.SlowPeaks)
	if overall.SlowPeaks > 0 {
		data.SlowNote = fmt.Sprintf(
			"A peak is a rolled-up period whose slowest sample exceeded %s; "+
				"older data is stored as rollups, which cannot report an exact sample count.",
			slowThresholdLabel())
	}

	data.LatencyNote = "Response times include failed samples."
}

// slowLine phrases the slow-probe stat honestly across tiers: raw rows yield an
// exact SAMPLE count, rollups can only yield a PEAK count (a period that held
// at least one slow probe). A window that straddles the retention boundary has
// both, and the two are never summed into one misleading number.
func slowLine(samples, peaks int32) string {
	threshold := slowThresholdLabel()

	switch {
	case samples > 0 && peaks > 0:
		return fmt.Sprintf("%s and %s above %s",
			pluralize(samples, "sample"), pluralize(peaks, "peak"), threshold)
	case peaks > 0:
		return fmt.Sprintf("%s above %s", pluralize(peaks, "peak"), threshold)
	case samples > 0:
		return fmt.Sprintf("%s above %s", pluralize(samples, "sample"), threshold)
	default:
		return "none above " + threshold
	}
}

func pluralize(count int32, noun string) string {
	if count == 1 {
		return fmt.Sprintf("%d %s", count, noun)
	}

	return fmt.Sprintf("%d %ss", count, noun)
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
