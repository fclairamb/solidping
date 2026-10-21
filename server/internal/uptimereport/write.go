package uptimereport

import (
	"context"
	"fmt"
	"math"
	"slices"
	"time"

	"github.com/fclairamb/solidping/server/internal/i18n"
	"github.com/fclairamb/solidping/server/internal/i18n/recipient"
	"github.com/fclairamb/solidping/server/internal/slo"
	"github.com/fclairamb/solidping/server/internal/uptimebar"
)

// pctDecimals is how many decimals an availability is printed with.
const pctDecimals = 3

// millisPerSecond is where a response time switches from "ms" to "s".
const millisPerSecond = 1000.0

// ForReader returns the report written for one recipient address, and the
// locale its mail is rendered in. The language comes from the address's
// account, else (an address with no account) from the organization owner,
// else English (recipient.Resolver.ForAddress).
//
// Recipients who resolve to the same language and timezone share one written
// copy. The returned value is a copy of it, so the caller can set its own
// UnsubscribeURL. Not safe for concurrent use.
func (d *Data) ForReader(ctx context.Context, orgUID, address string) (Data, i18n.Locale) {
	loc := i18n.Default
	if d.readers != nil {
		loc = d.readers.ForAddress(ctx, orgUID, address, recipient.Hint{})
	}

	key := loc.Lang() + " " + loc.Timezone()

	written, ok := d.written[key]
	if !ok {
		written = d.In(loc)

		if d.written == nil {
			d.written = make(map[string]*Data)
		}

		d.written[key] = written
	}

	return *written, loc
}

// In returns a copy of the report with its text written in loc's language.
// The rows are copied too: the report is shared by every recipient of a run.
func (d *Data) In(loc i18n.Locale) *Data {
	out := *d
	out.Checks = slices.Clone(d.Checks)
	out.SLOs = slices.Clone(d.SLOs)
	out.readers = nil
	out.written = nil
	out.write(loc)

	return &out
}

// write fills every text field from the flags and figures, in loc's language.
// A field whose flag is off is emptied.
func (d *Data) write(loc i18n.Locale) {
	fig := &d.Figures

	// The period is the schedule's calendar, whoever reads it: a July report
	// for Paris must not read "June" to a reader in New York.
	calendar := loc.InZone(d.Timezone)

	d.PeriodLabel = periodLabel(calendar, fig.Weekly, fig.Period)
	d.PreviousPeriodLabel = periodLabel(calendar, fig.Weekly, fig.Previous)
	d.ScopeLabel = scopeLabel(loc, fig.OrgWide, fig.ScheduleName, d.CheckCount)
	d.AvailabilityPct = pctIf(loc, d.HasData, fig.Availability)

	d.LongestIncident = spanIfAny(loc, fig.LongestIncident)
	d.AverageIncident = spanIfAny(loc, fig.AverageIncident)
	d.TotalDowntime = spanIfAny(loc, fig.TotalDowntime)

	d.writeTrend(loc)
	d.writeLatency(loc)

	for i := range d.Checks {
		row := &d.Checks[i]
		row.AvailabilityPct = pctIf(loc, row.HasData, row.Availability)
	}

	for i := range d.SLOs {
		d.SLOs[i].write(loc)
	}

	d.DayStripLabel = ""
	if d.StripsShown > 0 {
		d.DayStripLabel = dayStripPlan{start: fig.StripStart, days: fig.StripDays}.label(loc)
	}

	d.StripBudgetNote = ""
	if d.ShowStripBudgetNote {
		d.StripBudgetNote = loc.Tn("report.strip.budgetNote", d.StripsShown)
	}
}

func (d *Data) writeTrend(loc i18n.Locale) {
	fig := &d.Figures

	d.PreviousAvailabilityPct = pctIf(loc, d.HasPreviousData, fig.PreviousAvailability)

	d.AvailabilityDeltaText = ""
	if d.ShowAvailabilityDelta {
		d.AvailabilityDeltaText = signedDelta(loc, fig.AvailabilityDelta, availabilityDeltaPrecision, "report.delta.points")
	}

	d.IncidentDeltaText = ""
	if d.ShowIncidentDelta {
		d.IncidentDeltaText = signedCount(loc, fig.IncidentDelta)
	}

	d.ResponseDeltaText, d.PreviousAvgResponseTime = "", ""
	if d.ShowResponseDelta {
		d.ResponseDeltaText = signedDelta(loc, fig.ResponseDelta, responseDeltaPrecision, "report.delta.percent")
		d.PreviousAvgResponseTime = formatMillis(loc, fig.PreviousAvgResponse)
	}
}

func (d *Data) writeLatency(loc i18n.Locale) {
	fig := &d.Figures

	d.AvgResponseTime, d.MinResponseTime, d.MaxResponseTime = "", "", ""
	d.SlowLine, d.SlowNote, d.LatencyNote = "", "", ""

	if !d.HasLatency {
		return
	}

	d.AvgResponseTime = formatMillis(loc, fig.AvgResponse)

	if fig.HasResponseRange {
		d.MinResponseTime = formatMillis(loc, fig.MinResponse)
		d.MaxResponseTime = formatMillis(loc, fig.MaxResponse)
	}

	d.SlowLine = slowLine(loc, fig.SlowSamples, fig.SlowPeaks)

	if fig.SlowPeaks > 0 {
		d.SlowNote = loc.T("report.slow.peakNote", "threshold", slowThresholdLabel(loc))
	}

	// Failed samples are in these numbers: the rule uptimebar and the
	// aggregation job both apply.
	d.LatencyNote = loc.T("report.slow.includesFailures")
}

func (row *SLORow) write(loc i18n.Locale) {
	row.TargetPct = formatPct(loc, row.Target)
	row.StateLabel = stateLabel(loc, row.State)

	row.AttainmentPct = ""
	if row.Attainment != nil {
		row.AttainmentPct = formatPct(loc, *row.Attainment)
	}

	row.BudgetRemaining = ""
	if row.HasData {
		row.BudgetRemaining = formatSignedSpan(loc, row.Budget)
	}
}

// periodLabel names a report period: its month ("July 2026"), or for a
// weekly report its first and last day ("Jul 6 – Jul 12"). calendar is in the
// schedule's zone. A zero window (no previous period) gives "".
func periodLabel(calendar i18n.Locale, weekly bool, window slo.Window) string {
	if window.Start.IsZero() {
		return ""
	}

	if !weekly {
		return calendar.Month(window.Start)
	}

	return calendar.T("report.period.range",
		"start", calendar.Day(window.Start),
		"end", calendar.Day(window.End.Add(-time.Second)))
}

func scopeLabel(loc i18n.Locale, orgWide bool, name string, checkCount int) string {
	if orgWide {
		return loc.T("report.scope.all", "count", checkCount)
	}

	return loc.Tn("report.scope.named", checkCount, "name", name)
}

func stateLabel(loc i18n.Locale, state string) string {
	switch state {
	case slo.StateHealthy:
		return loc.T("report.slo.state.healthy")
	case slo.StateAtRisk:
		return loc.T("report.slo.state.atRisk")
	case slo.StateBreached:
		return loc.T("report.slo.state.breached")
	default:
		return loc.T("report.noData")
	}
}

// formatPct renders an availability: "99.950%", "99,950 %".
func formatPct(loc i18n.Locale, pct float64) string {
	return loc.Pct(roundTo(pct, pctDecimals), pctDecimals)
}

func pctIf(loc i18n.Locale, ok bool, pct float64) string {
	if !ok {
		return ""
	}

	return formatPct(loc, pct)
}

// spanIfAny renders a span ("21m 40s", "21 min 40 s"), or "" for none: an
// average of "0s" over zero incidents is not a fact. A unit that lands on
// zero is dropped ("42m", never "42m 0s").
func spanIfAny(loc i18n.Locale, span time.Duration) string {
	if span <= 0 {
		return ""
	}

	return loc.Duration(span)
}

// formatSignedSpan renders a possibly-negative budget. A breached budget must
// read as an overspend ("-12m 30s"), never silently clamp to zero.
func formatSignedSpan(loc i18n.Locale, span time.Duration) string {
	if span < 0 {
		return "-" + loc.Duration(-span)
	}

	return loc.Duration(span)
}

// signedDelta renders a movement with an explicit sign in the unit of key
// ("+0.080 pts", "+8.4%"), or "no change" when it rounds to zero at the
// precision printed: a signed zero reads as a measurement, and readers round
// it into "we improved".
func signedDelta(loc i18n.Locale, value float64, decimals int, key string) string {
	rounded := roundTo(value, decimals)
	if rounded == 0 {
		return loc.T("report.delta.noChange")
	}

	return loc.T(key, "value", loc.Signed(rounded, decimals))
}

// signedCount renders an integer movement with an explicit sign ("-2"), or
// "no change" at zero.
func signedCount(loc i18n.Locale, value int) string {
	if value == 0 {
		return loc.T("report.delta.noChange")
	}

	return fmt.Sprintf("%+d", value)
}

// formatMillis renders a response time. Below a second it is whole
// milliseconds (a tenth of a millisecond is noise on a network probe); from a
// second up, seconds with two decimals, because "1483 ms" is harder to read
// at a glance than "1.48 s". The switch is on the rounded value, so 999.6 ms
// reads "1.00 s", never "1,000 ms".
func formatMillis(loc i18n.Locale, millis float64) string {
	if math.Round(millis) >= millisPerSecond {
		return loc.T("report.units.s", "value", loc.Number(millis/millisPerSecond, 2))
	}

	return loc.T("report.units.ms", "value", loc.Number(millis, 0))
}

// slowThresholdLabel renders uptimebar.SlowSampleThresholdMillis for copy. A
// whole number of seconds reads as "1 s", not as formatMillis' "1.00 s": the
// threshold is a round configured value, not a measurement.
func slowThresholdLabel(loc i18n.Locale) string {
	if math.Mod(uptimebar.SlowSampleThresholdMillis, millisPerSecond) == 0 {
		return loc.T("report.units.s", "value", loc.Number(uptimebar.SlowSampleThresholdMillis/millisPerSecond, 0))
	}

	return formatMillis(loc, uptimebar.SlowSampleThresholdMillis)
}

// slowLine phrases the slow-probe stat honestly across tiers: raw rows yield an
// exact SAMPLE count, rollups can only yield a PEAK count (a period that held
// at least one slow probe). A window that straddles the retention boundary has
// both, and the two are never summed into one misleading number.
func slowLine(loc i18n.Locale, samples, peaks int32) string {
	threshold := slowThresholdLabel(loc)

	switch {
	case samples > 0 && peaks > 0:
		return loc.T("report.slow.both",
			"samples", loc.Tn("report.slow.samples", samples),
			"peaks", loc.Tn("report.slow.peaks", peaks),
			"threshold", threshold)
	case peaks > 0:
		return loc.T("report.slow.some", "counted", loc.Tn("report.slow.peaks", peaks), "threshold", threshold)
	case samples > 0:
		return loc.T("report.slow.some", "counted", loc.Tn("report.slow.samples", samples), "threshold", threshold)
	default:
		return loc.T("report.slow.none", "threshold", threshold)
	}
}
