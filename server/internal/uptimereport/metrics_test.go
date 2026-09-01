package uptimereport

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/uptimebar"
)

// upBucket builds a window aggregate: `total` probes, `up` of them successful,
// with `avg` milliseconds of average response time over every probe.
func upBucket(up, total int, avg float64) uptimebar.BucketStats {
	stats := uptimebar.BucketStats{Up: up, Total: total}
	if total > 0 && avg > 0 {
		stats.DurCnt = total
		stats.DurSum = avg * float64(total)
		stats.DurMin = avg / 2
		stats.DurMax = avg * 2
		stats.DurExtremaCnt = total
	}

	return stats
}

// TestDeltaColorIsNeverGreenAtZero is the spec's headline negative: the
// competitor report this work reacts to painted a "0.00% vs prev. month" green
// on a monitor that had been down all month. Zero is gray, always.
func TestDeltaColorIsNeverGreenAtZero(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	r.Equal(deltaNeutralColor, deltaColor(0))
	r.NotEqual(deltaGoodColor, deltaColor(0))

	// Positive controls: the same function does produce green and red, so the
	// assertion above could have failed.
	r.Equal(deltaGoodColor, deltaColor(0.001))
	r.Equal(deltaBadColor, deltaColor(-0.001))
}

// TestFormatSignedFloatSaysNoChangeAtZero pins the wording: a movement that
// rounds away at the precision we print reads as words, not as a signed zero
// that a skimming reader turns into "we improved".
func TestFormatSignedFloatSaysNoChangeAtZero(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name     string
		value    float64
		decimals int
		suffix   string
		want     string
	}{
		{"exact zero", 0, 3, " pts", noChangeText},
		{"rounds to zero", 0.0004, 3, " pts", noChangeText},
		{"negative rounds to zero", -0.0004, 1, "%", noChangeText},
		{"positive keeps its sign", 0.08, 3, " pts", "+0.080 pts"},
		{"negative keeps its sign", -8.44, 1, "%", "-8.4%"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tc.want, formatSignedFloat(tc.value, tc.decimals, tc.suffix))
		})
	}

	require.Equal(t, noChangeText, formatSignedCount(0))
	require.Equal(t, "+2", formatSignedCount(2))
	require.Equal(t, "-1", formatSignedCount(-1))
}

// TestApplyTrendOmitsEverythingWithoutABaseline is the "new org / first run"
// negative: no previous-period data must render NO delta at all — not
// "±0.00%", not a gray dash, not a color.
func TestApplyTrendOmitsEverythingWithoutABaseline(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	var data Data

	applyTrend(&data, trendInputs{
		current:          upBucket(1000, 1000, 150),
		previous:         uptimebar.BucketStats{}, // nothing recorded last month
		currentIncidents: 2,
	})

	r.False(data.HasPreviousData)
	r.False(data.ShowAvailabilityDelta)
	r.False(data.ShowIncidentDelta)
	r.False(data.ShowResponseDelta)
	r.Empty(data.AvailabilityDeltaText)
	r.Empty(data.AvailabilityDeltaColor)
	r.Empty(data.IncidentDeltaText)
	r.Empty(data.ResponseDeltaText)
	r.Empty(data.PreviousAvailabilityPct)

	// Positive control: the SAME call with a real baseline fills all three.
	var withBaseline Data

	applyTrend(&withBaseline, trendInputs{
		current:           upBucket(1000, 1000, 150),
		previous:          upBucket(990, 1000, 200),
		currentIncidents:  2,
		previousIncidents: 5,
	})

	r.True(withBaseline.HasPreviousData)
	r.True(withBaseline.ShowAvailabilityDelta)
	r.True(withBaseline.ShowIncidentDelta)
	r.True(withBaseline.ShowResponseDelta)
	r.Equal("+1.000 pts", withBaseline.AvailabilityDeltaText)
	r.Equal(deltaGoodColor, withBaseline.AvailabilityDeltaColor)
	r.Equal("-3", withBaseline.IncidentDeltaText)
	r.Equal(deltaGoodColor, withBaseline.IncidentDeltaColor)
	r.Equal("-25.0%", withBaseline.ResponseDeltaText)
	r.Equal(deltaGoodColor, withBaseline.ResponseDeltaColor)
}

// TestApplyTrendZeroDeltasAreNeutral pins the second failure mode: identical
// periods must read "no change" in gray, on every stat.
func TestApplyTrendZeroDeltasAreNeutral(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	var data Data

	same := upBucket(1000, 1000, 150)

	applyTrend(&data, trendInputs{
		current:           same,
		previous:          same,
		currentIncidents:  0,
		previousIncidents: 0,
	})

	r.True(data.ShowAvailabilityDelta)
	r.Equal(noChangeText, data.AvailabilityDeltaText)
	r.Equal(deltaNeutralColor, data.AvailabilityDeltaColor)

	r.True(data.ShowIncidentDelta)
	r.Equal(noChangeText, data.IncidentDeltaText)
	r.Equal(deltaNeutralColor, data.IncidentDeltaColor)

	r.True(data.ShowResponseDelta)
	r.Equal(noChangeText, data.ResponseDeltaText)
	r.Equal(deltaNeutralColor, data.ResponseDeltaColor)

	// No green anywhere in this report's trend block.
	r.NotContains(
		[]string{data.AvailabilityDeltaColor, data.IncidentDeltaColor, data.ResponseDeltaColor},
		deltaGoodColor)
}

// TestApplyResponseDeltaSuppressedForDegeneratePeriods is the "-13% faster,
// measured on error responses" guard. A period in which every probe failed
// cannot contribute a response-time comparison, in either direction.
func TestApplyResponseDeltaSuppressedForDegeneratePeriods(t *testing.T) {
	t.Parallel()

	downAllPeriod := upBucket(0, 1000, 20) // fast, because they were all errors
	healthy := upBucket(1000, 1000, 150)

	for _, tc := range []struct {
		name           string
		current        uptimebar.BucketStats
		previous       uptimebar.BucketStats
		wantShownAtAll bool
	}{
		{"current period down end to end", downAllPeriod, healthy, false},
		{"previous period down end to end", healthy, downAllPeriod, false},
		{"no durations this period", uptimebar.BucketStats{Up: 10, Total: 10}, healthy, false},
		{"no durations last period", healthy, uptimebar.BucketStats{Up: 10, Total: 10}, false},
		// Positive control: two comparable healthy periods DO produce a delta,
		// so every "false" above is a real suppression and not a broken call.
		{"two comparable periods", healthy, upBucket(1000, 1000, 300), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			r := require.New(t)

			var data Data

			applyTrend(&data, trendInputs{current: tc.current, previous: tc.previous})

			r.Equal(tc.wantShownAtAll, data.ShowResponseDelta)
			if !tc.wantShownAtAll {
				r.Empty(data.ResponseDeltaText)
				r.Empty(data.ResponseDeltaColor)
			}
		})
	}
}

// TestApplyLatencySuppressedWhenDownAllPeriod proves the latency block is
// absent — not zeroed — for a scope that was down end to end, and absent for a
// scope that measured no duration at all.
func TestApplyLatencySuppressedWhenDownAllPeriod(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	var downData Data

	applyLatency(&downData, upBucket(0, 1000, 20))
	r.False(downData.HasLatency)
	r.Empty(downData.AvgResponseTime)
	r.Empty(downData.MinResponseTime)
	r.Empty(downData.MaxResponseTime)
	r.Empty(downData.SlowLine)

	var noDurationData Data

	applyLatency(&noDurationData, uptimebar.BucketStats{Up: 10, Total: 10})
	r.False(noDurationData.HasLatency)
	r.Empty(noDurationData.AvgResponseTime)

	// Positive control.
	var healthy Data

	stats := upBucket(999, 1000, 150)
	stats.SlowSamples = 3

	applyLatency(&healthy, stats)
	r.True(healthy.HasLatency)
	r.Equal("150 ms", healthy.AvgResponseTime)
	r.Equal("75 ms", healthy.MinResponseTime)
	r.Equal("300 ms", healthy.MaxResponseTime)
	r.Equal("3 samples above 1 s", healthy.SlowLine)
	r.Empty(healthy.SlowNote, "no rollup peaks contributed, so there is nothing to explain")
	r.Contains(healthy.LatencyNote, "failed samples")
}

// TestSlowLinePhrasesTiersHonestly: raw yields exact samples, rollups yield
// peaks, and the two are never summed into a single misleading number.
func TestSlowLinePhrasesTiersHonestly(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name    string
		samples int
		peaks   int
		want    string
	}{
		{"none", 0, 0, "none above 1 s"},
		{"one sample", 1, 0, "1 sample above 1 s"},
		{"samples", 4, 0, "4 samples above 1 s"},
		{"peaks only", 0, 2, "2 peaks above 1 s"},
		{"both tiers", 4, 2, "4 samples and 2 peaks above 1 s"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got := slowLine(tc.samples, tc.peaks)
			require.Equal(t, tc.want, got)

			if tc.samples > 0 && tc.peaks > 0 {
				require.NotContains(t, got, "6 ", "samples and peaks must never be summed")
			}
		})
	}
}

// TestFormatMillisReadsAtAGlance pins the unit switch.
func TestFormatMillisReadsAtAGlance(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	r.Equal("0 ms", formatMillis(0))
	r.Equal("42 ms", formatMillis(42))
	r.Equal("999 ms", formatMillis(999.4))
	r.Equal("1.00 s", formatMillis(1000))
	r.Equal("3.20 s", formatMillis(3200))
}

// TestDayStripColorNoDataIsGrayNotRed is the mid-period-creation guard: a day
// on which nothing was recorded is not a day the check failed.
func TestDayStripColorNoDataIsGrayNotRed(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	r.Equal(dayNoDataColor, dayStripColor(uptimebar.BucketStats{}))
	r.NotEqual(dayBadColor, dayStripColor(uptimebar.BucketStats{}))

	// Positive controls across the whole ramp.
	r.Equal(dayGoodColor, dayStripColor(upBucket(1000, 1000, 100)))
	r.Equal(dayWarnColor, dayStripColor(upBucket(995, 1000, 100)))
	r.Equal(dayPoorColor, dayStripColor(upBucket(970, 1000, 100)))
	r.Equal(dayBadColor, dayStripColor(upBucket(0, 1000, 100)))
}

// TestSortWorstFirst is the ordering the maxCheckRows cap depends on: the
// broken check has to survive the truncation, and a no-data check must not be
// treated as 0%.
func TestSortWorstFirst(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	rows := []CheckRow{
		{Name: "alpha", HasData: true, AvailabilityPct: formatPct(100)},
		{Name: "zulu", HasData: false},
		{Name: "bravo", HasData: true, AvailabilityPct: formatPct(9)},
		{Name: "charlie", HasData: true, AvailabilityPct: formatPct(10)},
		{Name: "delta", HasData: true, AvailabilityPct: formatPct(0)},
		{Name: "alpha-two", HasData: false},
		{Name: "echo", HasData: true, AvailabilityPct: formatPct(100)},
	}

	sortWorstFirst(rows)

	names := make([]string, 0, len(rows))
	for _, row := range rows {
		names = append(names, row.Name)
	}

	r.Equal([]string{
		"delta",     // 0%
		"bravo",     // 9% — numeric, not string, ordering
		"charlie",   // 10%
		"alpha",     // 100%, tie broken by name
		"echo",      // 100%
		"alpha-two", // no data goes last, however early its name sorts
		"zulu",
	}, names)
}

// TestSortWorstFirstKeepsTheWorstUnderTruncation is the reason the sort exists:
// with more checks than maxCheckRows, the failing one must be in what ships.
func TestSortWorstFirstKeepsTheWorstUnderTruncation(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	rows := make([]CheckRow, 0, maxCheckRows+10)
	for i := range maxCheckRows + 9 {
		rows = append(rows, CheckRow{
			Name: "aaa-healthy-" + strings.Repeat("x", i%3), HasData: true,
			AvailabilityPct: formatPct(100),
		})
	}

	// Alphabetically last, and the only broken one.
	rows = append(rows, CheckRow{Name: "zzz-broken", HasData: true, AvailabilityPct: formatPct(12.5)})

	sortWorstFirst(rows)
	r.Equal("zzz-broken", rows[0].Name)
	r.Contains(rows[:maxCheckRows], rows[0])
}
