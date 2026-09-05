// Package membench holds the measurement core of the memory bench harness: the
// sample shape, the aggregation, the inter-run spread, and the significance
// rule that decides whether a delta between two runs means anything.
//
// It is deliberately separate from cmd/membench (which starts servers, drives
// workloads and shells out to docker) so the part that turns numbers into
// conclusions is pure, unit-tested code with no clock, no network and no
// filesystem.
//
// The rule that matters: **a delta smaller than the baseline's own inter-run
// spread is not a result.** Repeating a run changes the numbers by a few
// percent all on its own; a change that moves them by less than that has not
// been shown to do anything. Everything here exists to make that judgement
// mechanical instead of hopeful.
package membench

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
)

// Metric keys. These are the fixed column schema of every report, so two runs —
// and two versions of this tool — diff cleanly.
const (
	// MetricCgroupUnreclaimable is the PRIMARY metric: cgroup anon + kernel,
	// the memory the kernel cannot reclaim and therefore the number an OOM kill
	// is decided on. Every ship/reject decision is made on this one.
	MetricCgroupUnreclaimable = "cgroupUnreclaimableBytes"
	// MetricCgroupPeak is the cgroup high-water mark — the peak metric, which
	// is what a transient burst (a docs crawl, a login burst) shows up in.
	MetricCgroupPeak = "cgroupPeakBytes"
	// MetricRssAnon is resident anonymous memory: the primary metric's
	// equivalent outside a container, and the only RSS component that can OOM.
	MetricRssAnon = "rssAnonBytes"
	// MetricPss and MetricRssFile move with binary size and with which pages
	// were touched. Reported so a binary-size change is VISIBLE, and kept
	// separate so it is never conflated with a heap change.
	MetricPss     = "pssBytes"
	MetricRssFile = "rssFileBytes"
	// MetricGoTotal is the runtime's own total; MetricHeapLive the live heap.
	MetricGoTotal  = "goTotalBytes"
	MetricHeapLive = "heapLiveBytes"
	// MetricGoroutines and MetricThreads are the leak-shaped counters.
	MetricGoroutines = "goroutines"
	MetricThreads    = "threads"
)

// MetricKeys is the report's column order. Ordered primary-metric-first so the
// number a decision is made on is the one a reader sees first.
//
//nolint:gochecknoglobals // immutable schema table
var MetricKeys = []string{
	MetricCgroupUnreclaimable,
	MetricCgroupPeak,
	MetricRssAnon,
	MetricPss,
	MetricRssFile,
	MetricGoTotal,
	MetricHeapLive,
	MetricGoroutines,
	MetricThreads,
}

// Sample is one reading of /api/mgmt/memory, reduced to the numbers the harness
// reports on.
type Sample struct {
	At     time.Time          `json:"at"`
	Values map[string]float64 `json:"values"`
}

// MetricStats is one metric's summary over a single repetition's samples.
type MetricStats struct {
	Median float64 `json:"median"`
	P95    float64 `json:"p95"`
	Max    float64 `json:"max"`
	// Samples is how many readings went into the summary. A repetition with too
	// few samples is not a measurement, and the report says so rather than
	// quietly averaging three points.
	Samples int `json:"samples"`
}

// RunStats is one repetition: every metric summarized over its sample window.
type RunStats struct {
	Metrics map[string]MetricStats `json:"metrics"`
}

// MetricAgg is one metric aggregated across the K repetitions of a scenario.
type MetricAgg struct {
	// Median is the median of the per-run medians — the number to quote.
	Median float64 `json:"median"`
	// P95 and Max are the worst per-run p95 / max seen across repetitions.
	P95 float64 `json:"p95"`
	Max float64 `json:"max"`
	// Spread is max−min of the per-run medians: how much this number moves when
	// nothing at all changes. It is the yardstick every delta is measured
	// against.
	Spread float64 `json:"spread"`
	// SpreadKnown is false with fewer than two repetitions. A single run has no
	// spread, so nothing measured against it can be called significant — the
	// tool refuses to guess rather than inventing a threshold.
	SpreadKnown bool `json:"spreadKnown"`
	// RunMedians is the raw per-run medians, so a reader can see the spread
	// rather than trust it.
	RunMedians []float64 `json:"runMedians"`
}

// SpreadRatio is the spread as a fraction of the median — the number that
// decides whether this measurement is precise enough to be worth wiring into
// CI. Zero when the median is zero.
func (a MetricAgg) SpreadRatio() float64 {
	if a.Median == 0 {
		return 0
	}

	return a.Spread / a.Median
}

// ScenarioResult is everything the harness learned about one scenario.
type ScenarioResult struct {
	Scenario string               `json:"scenario"`
	Mode     string               `json:"mode"`
	Runs     []RunStats           `json:"runs"`
	Metrics  map[string]MetricAgg `json:"metrics"`
	// Notes carries anything that qualifies the numbers (a skipped workload, a
	// short sample window). Never silently dropped: a caveat that does not
	// reach the report is a caveat that does not exist.
	Notes []string `json:"notes,omitempty"`
}

// Report is the whole run: metadata plus one result per scenario. Serialized to
// JSON so `--compare` can diff two runs mechanically.
type Report struct {
	// Label names the build under test ("baseline", "gogc-50", …).
	Label string `json:"label"`
	// Mode is "docker" (authoritative: real cgroup v2, real Linux, the shipped
	// image shape) or "local" (host binary — fast, and NOT authoritative).
	Mode      string `json:"mode"`
	Host      string `json:"host"`
	GoVersion string `json:"goVersion"`
	Image     string `json:"image,omitempty"`
	Memory    string `json:"memory,omitempty"`
	// Env records any -env overrides, so a run tuned by an environment knob can
	// never be mistaken for an untuned one.
	Env string `json:"env,omitempty"`
	// SampleMode is "steady" or "floor" (?gc=1 before each reading). The two
	// measure different things and a comparison across them is meaningless, so
	// it travels with every report and is flagged in CompareMarkdown.
	SampleMode string           `json:"sampleMode,omitempty"`
	CPUs       string           `json:"cpus,omitempty"`
	StartedAt  time.Time        `json:"startedAt"`
	Protocol   Protocol         `json:"protocol"`
	Scenarios  []ScenarioResult `json:"scenarios"`
}

// Protocol records the measurement parameters, because a number without its
// warm-up and window is not reproducible.
type Protocol struct {
	WarmUp   time.Duration `json:"warmUp"`
	Interval time.Duration `json:"interval"`
	Duration time.Duration `json:"duration"`
	Reps     int           `json:"reps"`
}

// SummariseRun reduces one repetition's samples to per-metric stats.
func SummariseRun(samples []Sample) RunStats {
	run := RunStats{Metrics: make(map[string]MetricStats, len(MetricKeys))}

	for _, key := range MetricKeys {
		values := make([]float64, 0, len(samples))

		for i := range samples {
			if v, ok := samples[i].Values[key]; ok {
				values = append(values, v)
			}
		}

		run.Metrics[key] = MetricStats{
			Median:  Median(values),
			P95:     Percentile(values, 0.95),
			Max:     Max(values),
			Samples: len(values),
		}
	}

	return run
}

// Aggregate folds K repetitions into the per-metric aggregate, including the
// inter-run spread that every significance judgement depends on.
func Aggregate(runs []RunStats) map[string]MetricAgg {
	out := make(map[string]MetricAgg, len(MetricKeys))

	for _, key := range MetricKeys {
		medians := make([]float64, 0, len(runs))
		p95s := make([]float64, 0, len(runs))
		maxes := make([]float64, 0, len(runs))

		for _, run := range runs {
			stats, ok := run.Metrics[key]
			if !ok || stats.Samples == 0 {
				continue
			}

			medians = append(medians, stats.Median)
			p95s = append(p95s, stats.P95)
			maxes = append(maxes, stats.Max)
		}

		agg := MetricAgg{
			Median:     Median(medians),
			P95:        Max(p95s),
			Max:        Max(maxes),
			RunMedians: medians,
		}
		if len(medians) >= 2 {
			agg.Spread = Max(medians) - Min(medians)
			agg.SpreadKnown = true
		}

		out[key] = agg
	}

	return out
}

// Delta is one metric compared between a baseline and a current run.
type Delta struct {
	Scenario string  `json:"scenario"`
	Metric   string  `json:"metric"`
	Baseline float64 `json:"baseline"`
	Current  float64 `json:"current"`
	Delta    float64 `json:"delta"`
	// Threshold is the noise floor this delta had to clear: the larger of the
	// two runs' spreads.
	Threshold float64 `json:"threshold"`
	// Significant is true only when |delta| > threshold AND both spreads are
	// known. Anything else is reported as noise, however much one wanted the
	// change to work.
	Significant bool `json:"significant"`
	// Reason explains a non-significant verdict in words, so a report is
	// readable without re-deriving the rule.
	Reason string `json:"reason,omitempty"`
}

// PercentChange is the delta relative to the baseline. Zero when the baseline
// is zero.
func (d *Delta) PercentChange() float64 {
	if d.Baseline == 0 {
		return 0
	}

	return d.Delta / d.Baseline * 100
}

// Compare diffs two reports scenario by scenario. Scenarios present in only one
// of the two are skipped — silently comparing a scenario against a differently
// named one is how false results are manufactured.
func Compare(baseline, current *Report) []Delta {
	baseByName := make(map[string]ScenarioResult, len(baseline.Scenarios))
	for i := range baseline.Scenarios {
		baseByName[baseline.Scenarios[i].Scenario] = baseline.Scenarios[i]
	}

	var deltas []Delta

	for i := range current.Scenarios {
		cur := &current.Scenarios[i]

		base, ok := baseByName[cur.Scenario]
		if !ok {
			continue
		}

		for _, key := range MetricKeys {
			deltas = append(deltas, compareMetric(cur.Scenario, key, base.Metrics[key], cur.Metrics[key]))
		}
	}

	return deltas
}

// compareMetric applies the significance rule to one metric.
func compareMetric(scenario, key string, base, cur MetricAgg) Delta {
	delta := Delta{
		Scenario:  scenario,
		Metric:    key,
		Baseline:  base.Median,
		Current:   cur.Median,
		Delta:     cur.Median - base.Median,
		Threshold: math.Max(base.Spread, cur.Spread),
	}

	switch {
	case !base.SpreadKnown || !cur.SpreadKnown:
		delta.Reason = "spread unknown (need ≥2 repetitions on both sides)"
	case math.Abs(delta.Delta) <= delta.Threshold:
		delta.Reason = "not significant (delta ≤ inter-run spread)"
	default:
		delta.Significant = true
	}

	return delta
}

// Median returns the median of values; 0 for an empty slice. The input is not
// modified.
func Median(values []float64) float64 {
	return Percentile(values, 0.5)
}

// Percentile returns the quantile (0..1) using nearest-rank on a sorted copy.
// Nearest-rank rather than interpolation: these are observed readings, and an
// interpolated p95 is a number the process never actually had.
func Percentile(values []float64, quantile float64) float64 {
	if len(values) == 0 {
		return 0
	}

	sorted := make([]float64, len(values))
	copy(sorted, values)
	sort.Float64s(sorted)

	if quantile <= 0 {
		return sorted[0]
	}
	if quantile >= 1 {
		return sorted[len(sorted)-1]
	}

	rank := int(math.Ceil(quantile*float64(len(sorted)))) - 1
	if rank < 0 {
		rank = 0
	}
	if rank >= len(sorted) {
		rank = len(sorted) - 1
	}

	return sorted[rank]
}

// Max returns the largest value; 0 for an empty slice.
func Max(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}

	out := values[0]
	for _, v := range values[1:] {
		if v > out {
			out = v
		}
	}

	return out
}

// Min returns the smallest value; 0 for an empty slice.
func Min(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}

	out := values[0]
	for _, v := range values[1:] {
		if v < out {
			out = v
		}
	}

	return out
}

// countMetrics are the metrics that are counts, not byte totals. Rendering a
// goroutine count in MiB would print "0.0" for every row — a table that quietly
// hides a goroutine leak is worse than no table.
//
//nolint:gochecknoglobals // immutable schema table
var countMetrics = map[string]bool{
	MetricGoroutines: true,
	MetricThreads:    true,
}

// FormatBytes renders a byte count in MiB with one decimal, the unit every byte
// column in this report is read in.
func FormatBytes(v float64) string {
	return fmt.Sprintf("%.1f", v/(1024*1024))
}

// FormatMetric renders a value in the unit its metric is measured in.
func FormatMetric(metric string, v float64) string {
	if countMetrics[metric] {
		return fmt.Sprintf("%.0f", v)
	}

	return FormatBytes(v)
}

// FormatMetricDelta is FormatMetric with the sign kept visible.
func FormatMetricDelta(metric string, v float64) string {
	if v > 0 {
		return "+" + FormatMetric(metric, v)
	}

	return FormatMetric(metric, v)
}

// MetricUnit names the unit a metric's columns are in.
func MetricUnit(metric string) string {
	if countMetrics[metric] {
		return "count"
	}

	return "MiB"
}

// FormatDelta renders a signed byte delta in MiB, keeping the sign visible so a
// regression cannot be mistaken for an improvement at a glance.
func FormatDelta(v float64) string {
	if v > 0 {
		return "+" + FormatBytes(v)
	}

	return FormatBytes(v)
}

// Markdown renders the report as a fixed-schema table. The schema is fixed on
// purpose: two runs of this tool must diff as text, not just as JSON.
func (r *Report) Markdown() string {
	var out strings.Builder

	fmt.Fprintf(&out, "# membench — %s\n\n", r.Label)
	fmt.Fprintf(&out, "- mode: **%s**%s\n", r.Mode, modeCaveat(r.Mode))
	fmt.Fprintf(&out, "- host: %s (%s)\n", r.Host, r.GoVersion)

	if r.Image != "" {
		fmt.Fprintf(&out, "- image: `%s`", r.Image)

		if r.Memory != "" {
			fmt.Fprintf(&out, ", `--memory=%s`", r.Memory)
		}

		if r.CPUs != "" {
			fmt.Fprintf(&out, ", `--cpus=%s`", r.CPUs)
		}

		out.WriteString("\n")
	}

	if r.SampleMode != "" {
		fmt.Fprintf(&out, "- sample mode: **%s**%s\n", r.SampleMode, sampleModeCaveat(r.SampleMode))
	}

	if r.Env != "" {
		fmt.Fprintf(&out, "- env overrides: `%s`\n", r.Env)
	}

	fmt.Fprintf(&out, "- protocol: warm-up %s, sample every %s for %s, %d repetitions\n",
		r.Protocol.WarmUp, r.Protocol.Interval, r.Protocol.Duration, r.Protocol.Reps)
	fmt.Fprintf(&out, "- started: %s\n\n", r.StartedAt.Format(time.RFC3339))
	out.WriteString("All byte columns are MiB. Primary metric (the one decisions are made on) " +
		"is `cgroupUnreclaimableBytes` = cgroup anon + kernel — what the OOM killer cannot reclaim. " +
		"`pssBytes` / `rssFileBytes` move with binary size and with which pages were touched; they are " +
		"reported so that is visible, never added to the heap numbers.\n\n")

	out.WriteString("| scenario | metric | unit | median | p95 | max | inter-run spread |\n")
	out.WriteString("|---|---|---|---:|---:|---:|---:|\n")

	for i := range r.Scenarios {
		scenario := &r.Scenarios[i]

		for _, key := range MetricKeys {
			agg := scenario.Metrics[key]

			spread := FormatMetric(key, agg.Spread)
			if !agg.SpreadKnown {
				spread = "n/a"
			}

			fmt.Fprintf(&out, "| %s | %s | %s | %s | %s | %s | %s |\n",
				scenario.Scenario, key, MetricUnit(key),
				FormatMetric(key, agg.Median), FormatMetric(key, agg.P95),
				FormatMetric(key, agg.Max), spread)
		}
	}

	for i := range r.Scenarios {
		for _, note := range r.Scenarios[i].Notes {
			fmt.Fprintf(&out, "> **%s**: %s\n", r.Scenarios[i].Scenario, note)
		}
	}

	return out.String()
}

// sampleModeCaveat says what a floor reading is and is not.
func sampleModeCaveat(mode string) string {
	if mode == "floor" {
		return " — a GC and FreeOSMemory ran before every reading, so this is the LIVE FLOOR, " +
			"not what the process costs in production. Compare a floor run only with another floor run."
	}

	return " — the process as a monitoring system would see it."
}

// modeCaveat spells out, in the report itself, what a number from this mode is
// worth. Every mode gets its own sentence and none falls through to another's:
// a report that claims cgroup accounting it did not have is exactly the failure
// this harness exists to prevent, and "attach" — which points at a process the
// harness neither started nor configured — is the mode where that would bite.
func modeCaveat(mode string) string {
	switch mode {
	case "docker":
		return " — containerized: real cgroup v2 accounting, the shipped image shape, a fresh process per repetition."
	case "local":
		return " — host binary, **NOT authoritative**: no cgroup accounting, a different OS and a different " +
			"GOMAXPROCS from the shipped container. Useful for iteration and for relative comparisons on the " +
			"same host; never quote it as the production number."
	case "attach":
		return " — attached to a process this harness did not start: **provenance unknown**. Nothing about the " +
			"build, the environment, the warm-up or the workload was controlled here, and the cgroup section is " +
			"populated only if whatever you pointed at happens to be containerized (on a macOS `make dev` server " +
			"it is absent). Read it as an observation of that one process, never as a measurement of SolidPing."
	default:
		return " — unrecognized mode: provenance unknown, treat every number below as unverified."
	}
}

// CompareMarkdown renders a comparison table, non-significant rows included and
// labeled. Rejections are results too, and hiding them is how the same dead end
// gets retried a year later.
func CompareMarkdown(baseline, current *Report, deltas []Delta) string {
	var out strings.Builder

	fmt.Fprintf(&out, "# membench comparison — `%s` → `%s`\n\n", baseline.Label, current.Label)
	fmt.Fprintf(&out, "- baseline mode: %s; current mode: %s\n", baseline.Mode, current.Mode)

	if baseline.SampleMode != current.SampleMode {
		out.WriteString("- ⚠️ **sample modes differ (steady vs floor) — these two runs are not comparable.**\n")
	}

	if baseline.Mode != current.Mode {
		out.WriteString("- ⚠️ **modes differ — these two runs are not comparable.** " +
			"A local number and a containerized number measure different things.\n")
	}

	out.WriteString("\nA delta is only significant when it exceeds the larger of the two runs' " +
		"own inter-run spreads (MiB).\n\n")
	out.WriteString("| scenario | metric | unit | baseline | current | delta | % | noise floor | verdict |\n")
	out.WriteString("|---|---|---|---:|---:|---:|---:|---:|---|\n")

	for i := range deltas {
		delta := &deltas[i]

		verdict := "**significant**"
		if !delta.Significant {
			verdict = delta.Reason
		}

		fmt.Fprintf(&out, "| %s | %s | %s | %s | %s | %s | %+.1f%% | %s | %s |\n",
			delta.Scenario, delta.Metric, MetricUnit(delta.Metric),
			FormatMetric(delta.Metric, delta.Baseline), FormatMetric(delta.Metric, delta.Current),
			FormatMetricDelta(delta.Metric, delta.Delta),
			delta.PercentChange(), FormatMetric(delta.Metric, delta.Threshold), verdict)
	}

	return out.String()
}
