package membench

import (
	"math"
	"strings"
	"testing"
	"time"
)

// sampleSeries builds one repetition's samples with a fixed value per metric,
// optionally jittered, so the aggregation can be checked against arithmetic
// done by hand.
func sampleSeries(values []float64) []Sample {
	out := make([]Sample, 0, len(values))

	for i, v := range values {
		out = append(out, Sample{
			At: time.Unix(int64(i), 0),
			Values: map[string]float64{
				MetricCgroupUnreclaimable: v,
				MetricRssAnon:             v,
				MetricGoroutines:          40,
			},
		})
	}

	return out
}

func TestPercentileNearestRank(t *testing.T) {
	t.Parallel()

	values := []float64{10, 20, 30, 40, 50, 60, 70, 80, 90, 100}

	cases := []struct {
		p    float64
		want float64
	}{
		{0.5, 50},
		{0.95, 100},
		{0, 10},
		{1, 100},
	}
	for _, c := range cases {
		if got := Percentile(values, c.p); got != c.want {
			t.Errorf("Percentile(%v) = %v, want %v", c.p, got, c.want)
		}
	}

	// An interpolated p95 would be a number the process never actually had;
	// nearest-rank must return an observed reading.
	if got := Percentile([]float64{1, 2, 3}, 0.95); got != 3 {
		t.Errorf("p95 of {1,2,3} = %v, want an observed value (3)", got)
	}

	if got := Percentile(nil, 0.5); got != 0 {
		t.Errorf("Percentile(nil) = %v, want 0", got)
	}
}

func TestPercentileDoesNotMutateInput(t *testing.T) {
	t.Parallel()

	values := []float64{3, 1, 2}
	_ = Percentile(values, 0.5)

	if values[0] != 3 || values[1] != 1 || values[2] != 2 {
		t.Errorf("input reordered: %v", values)
	}
}

func TestSummariseRun(t *testing.T) {
	t.Parallel()

	run := SummariseRun(sampleSeries([]float64{100, 110, 120, 130, 500}))

	stats := run.Metrics[MetricCgroupUnreclaimable]
	if stats.Median != 120 {
		t.Errorf("median = %v, want 120", stats.Median)
	}
	if stats.Max != 500 {
		t.Errorf("max = %v, want 500", stats.Max)
	}
	if stats.Samples != 5 {
		t.Errorf("samples = %d, want 5", stats.Samples)
	}

	// A metric absent from every sample must report zero samples, so the
	// aggregation can skip it instead of averaging nothing into a number.
	if got := run.Metrics[MetricPss].Samples; got != 0 {
		t.Errorf("absent metric reported %d samples", got)
	}
}

func TestAggregateSpread(t *testing.T) {
	t.Parallel()

	runs := []RunStats{
		SummariseRun(sampleSeries([]float64{100, 100, 100})),
		SummariseRun(sampleSeries([]float64{110, 110, 110})),
		SummariseRun(sampleSeries([]float64{104, 104, 104})),
	}

	agg := Aggregate(runs)[MetricCgroupUnreclaimable]

	if agg.Median != 104 {
		t.Errorf("median of run medians = %v, want 104", agg.Median)
	}
	if !agg.SpreadKnown || agg.Spread != 10 {
		t.Errorf("spread = %v (known=%v), want 10", agg.Spread, agg.SpreadKnown)
	}
	if math.Abs(agg.SpreadRatio()-10.0/104.0) > 1e-9 {
		t.Errorf("spread ratio = %v", agg.SpreadRatio())
	}
}

// TestAggregateSingleRunHasNoSpread pins the refusal to invent a threshold: one
// repetition cannot tell you how much the number moves on its own.
func TestAggregateSingleRunHasNoSpread(t *testing.T) {
	t.Parallel()

	agg := Aggregate([]RunStats{SummariseRun(sampleSeries([]float64{100, 100}))})[MetricCgroupUnreclaimable]

	if agg.SpreadKnown {
		t.Error("a single repetition must not claim a known spread")
	}
	if agg.Spread != 0 {
		t.Errorf("spread = %v, want 0 when unknown", agg.Spread)
	}
}

// report builds a two-scenario report where the "touched" scenario's values can
// be shifted and the "untouched" one is held constant — the negative-control
// shape the spec demands.
func report(label string, touched, untouched [][]float64) *Report {
	build := func(name string, runs [][]float64) ScenarioResult {
		stats := make([]RunStats, 0, len(runs))
		for _, values := range runs {
			stats = append(stats, SummariseRun(sampleSeries(values)))
		}

		return ScenarioResult{Scenario: name, Mode: "local", Runs: stats, Metrics: Aggregate(stats)}
	}

	return &Report{
		Label:     label,
		Mode:      "local",
		Scenarios: []ScenarioResult{build("checks-500", touched), build("idle-all-sqlite", untouched)},
	}
}

// baselineRuns is a scenario measured three times with a ±5-unit spread.
//
//nolint:gochecknoglobals // shared fixture
var baselineRuns = [][]float64{{100}, {105}, {102}}

// TestCompareNoOpRerunIsNotSignificant is the first negative control the spec
// requires: re-running the baseline against itself, with only the ordinary
// inter-run jitter, must produce "not significant" everywhere. A harness that
// reports a win here would report a win for anything.
func TestCompareNoOpRerunIsNotSignificant(t *testing.T) {
	t.Parallel()

	base := report("baseline", baselineRuns, baselineRuns)
	// Same scenario, re-run: different individual numbers, same distribution.
	rerun := report("baseline-rerun", [][]float64{{103}, {99}, {104}}, [][]float64{{101}, {104}, {100}})

	for _, d := range Compare(base, rerun) {
		if d.Significant {
			t.Errorf("no-op rerun reported a significant delta on %s/%s: %+v", d.Scenario, d.Metric, d)
		}
	}
}

// TestCompareRealChangeOnlyOnTouchedScenario is the second negative control: a
// change large enough to clear the noise floor is flagged on the scenario it
// targets and NOT on the untouched one.
func TestCompareRealChangeOnlyOnTouchedScenario(t *testing.T) {
	t.Parallel()

	base := report("baseline", baselineRuns, baselineRuns)
	after := report("candidate", [][]float64{{60}, {64}, {62}}, baselineRuns)

	var touchedSignificant, untouchedSignificant bool

	for _, d := range Compare(base, after) {
		if d.Metric != MetricCgroupUnreclaimable {
			continue
		}

		switch d.Scenario {
		case "checks-500":
			touchedSignificant = d.Significant

			if d.Delta >= 0 {
				t.Errorf("expected a reduction, got delta %v", d.Delta)
			}
		case "idle-all-sqlite":
			untouchedSignificant = d.Significant
		}
	}

	if !touchedSignificant {
		t.Error("a 40-unit drop against a 5-unit spread must be significant")
	}
	if untouchedSignificant {
		t.Error("the untouched scenario must show no change — that is the negative control")
	}
}

// TestCompareBelowNoiseFloor pins the exact boundary: a delta equal to the
// spread is not significant, one larger is.
func TestCompareBelowNoiseFloor(t *testing.T) {
	t.Parallel()

	base := report("baseline", baselineRuns, baselineRuns) // medians 100/105/102 → spread 5

	atFloor := report("at-floor", [][]float64{{97}, {97}, {97}}, baselineRuns) // delta −5, spread 0
	for _, d := range Compare(base, atFloor) {
		if d.Scenario == "checks-500" && d.Metric == MetricCgroupUnreclaimable {
			if d.Significant {
				t.Errorf("delta exactly at the noise floor must not be significant: %+v", d)
			}

			if !strings.Contains(d.Reason, "not significant") {
				t.Errorf("reason = %q", d.Reason)
			}
		}
	}

	overFloor := report("over-floor", [][]float64{{96}, {96}, {96}}, baselineRuns) // delta −6 > spread 5
	for _, d := range Compare(base, overFloor) {
		if d.Scenario == "checks-500" && d.Metric == MetricCgroupUnreclaimable && !d.Significant {
			t.Errorf("delta above the noise floor must be significant: %+v", d)
		}
	}
}

// TestCompareUnknownSpreadNeverSignificant: with a single repetition there is
// no noise floor, so no verdict may be given, however large the delta looks.
func TestCompareUnknownSpreadNeverSignificant(t *testing.T) {
	t.Parallel()

	base := report("baseline", [][]float64{{100}}, [][]float64{{100}})
	after := report("candidate", [][]float64{{10}}, [][]float64{{100}})

	for _, d := range Compare(base, after) {
		if d.Significant {
			t.Errorf("verdict given without a known spread: %+v", d)
		}

		if !strings.Contains(d.Reason, "spread unknown") {
			t.Errorf("reason = %q, want the spread-unknown explanation", d.Reason)
		}
	}
}

// TestCompareIgnoresUnmatchedScenarios: comparing a scenario against a
// differently named one is how false results are manufactured.
func TestCompareIgnoresUnmatchedScenarios(t *testing.T) {
	t.Parallel()

	base := &Report{Label: "b", Scenarios: []ScenarioResult{{Scenario: "only-in-base", Metrics: map[string]MetricAgg{}}}}
	cur := &Report{Label: "c", Scenarios: []ScenarioResult{{Scenario: "only-in-current", Metrics: map[string]MetricAgg{}}}}

	if got := Compare(base, cur); len(got) != 0 {
		t.Errorf("expected no deltas across disjoint scenario sets, got %d", len(got))
	}
}

func TestMarkdownSchemaAndCaveats(t *testing.T) {
	t.Parallel()

	rep := report("baseline", baselineRuns, baselineRuns)
	rep.Protocol = Protocol{WarmUp: time.Minute, Interval: 5 * time.Second, Duration: 5 * time.Minute, Reps: 3}
	rep.Host = "test-host"

	md := rep.Markdown()

	for _, key := range MetricKeys {
		if !strings.Contains(md, "| "+key+" |") {
			t.Errorf("markdown missing a row for %s", key)
		}
	}

	// Counts must be rendered as counts: a goroutine count divided by 1 MiB
	// prints 0.0 for every row, and a table that hides a goroutine leak is
	// worse than no table.
	if got := FormatMetric(MetricGoroutines, 42); got != "42" {
		t.Errorf("FormatMetric(goroutines, 42) = %q, want \"42\"", got)
	}
	if got := FormatMetric(MetricRssAnon, 2*1024*1024); got != "2.0" {
		t.Errorf("FormatMetric(rssAnon, 2MiB) = %q", got)
	}
	if !strings.Contains(md, "NOT authoritative") {
		t.Error("a local-mode report must carry its non-authoritative caveat")
	}
	if !strings.Contains(md, "cgroupUnreclaimableBytes") {
		t.Error("the primary metric must be named in the header text")
	}
}

func TestCompareMarkdownFlagsModeMismatch(t *testing.T) {
	t.Parallel()

	base := report("baseline", baselineRuns, baselineRuns)
	cur := report("candidate", baselineRuns, baselineRuns)
	cur.Mode = "docker"

	md := CompareMarkdown(base, cur, Compare(base, cur))

	if !strings.Contains(md, "not comparable") {
		t.Error("comparing a local run against a containerized one must be flagged, not quietly tabulated")
	}
}

func TestFormatDeltaKeepsSign(t *testing.T) {
	t.Parallel()

	if got := FormatDelta(2 * 1024 * 1024); got != "+2.0" {
		t.Errorf("FormatDelta(+2MiB) = %q", got)
	}
	if got := FormatDelta(-2 * 1024 * 1024); got != "-2.0" {
		t.Errorf("FormatDelta(-2MiB) = %q", got)
	}
}
