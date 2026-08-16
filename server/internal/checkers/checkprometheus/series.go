package checkprometheus

import (
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"

	dto "github.com/prometheus/client_model/go"
)

// series is one flattened, addressable data point: a name, its label set and
// its value. Flattening is what lets the selector stay dumb — a histogram or
// summary family is expanded into the same `_sum` / `_count` / `_bucket{le=…}`
// / `{quantile=…}` series names Prometheus itself exposes, so selecting
// `http_request_duration_seconds_count` needs no special casing at all.
type series struct {
	Name   string
	Labels map[string]string
	Value  float64
}

// Reserved label names produced by flattening.
const (
	labelLE       = "le"
	labelQuantile = "quantile"
)

var errAggregationUnknown = errors.New("unknown match strategy")

// flattenFamilies expands parsed metric families into addressable series.
func flattenFamilies(families map[string]*dto.MetricFamily) []series {
	out := make([]series, 0, len(families))

	for name, family := range families {
		for _, metric := range family.GetMetric() {
			base := labelsOf(metric)

			switch family.GetType() {
			case dto.MetricType_GAUGE:
				out = append(out, series{Name: name, Labels: base, Value: metric.GetGauge().GetValue()})
			case dto.MetricType_COUNTER:
				out = append(out, series{Name: name, Labels: base, Value: metric.GetCounter().GetValue()})
			case dto.MetricType_UNTYPED:
				out = append(out, series{Name: name, Labels: base, Value: metric.GetUntyped().GetValue()})
			case dto.MetricType_SUMMARY:
				out = append(out, flattenSummary(name, base, metric.GetSummary())...)
			case dto.MetricType_HISTOGRAM:
				out = append(out, flattenHistogram(name, base, metric.GetHistogram())...)
			case dto.MetricType_GAUGE_HISTOGRAM:
				out = append(out, flattenHistogram(name, base, metric.GetHistogram())...)
			}
		}
	}

	return out
}

// labelsOf copies a metric's label pairs into a plain map.
func labelsOf(metric *dto.Metric) map[string]string {
	labels := make(map[string]string, len(metric.GetLabel()))
	for _, pair := range metric.GetLabel() {
		labels[pair.GetName()] = pair.GetValue()
	}

	return labels
}

// flattenSummary expands a summary into `_sum`, `_count` and per-quantile
// series, exactly as the exposition format renders them.
func flattenSummary(name string, base map[string]string, summary *dto.Summary) []series {
	// +2 for the _sum and _count series.
	out := make([]series, 0, len(summary.GetQuantile())+2)

	out = append(out,
		series{Name: name + "_sum", Labels: copyLabels(base), Value: summary.GetSampleSum()},
		series{Name: name + "_count", Labels: copyLabels(base), Value: float64(summary.GetSampleCount())},
	)

	for _, q := range summary.GetQuantile() {
		labels := copyLabels(base)
		labels[labelQuantile] = formatFloat(q.GetQuantile())
		out = append(out, series{Name: name, Labels: labels, Value: q.GetValue()})
	}

	return out
}

// flattenHistogram expands a histogram into `_sum`, `_count` and
// `_bucket{le=…}` series (including the `+Inf` bucket).
func flattenHistogram(name string, base map[string]string, histogram *dto.Histogram) []series {
	// The parsed bucket list already carries the +Inf bucket when the target
	// exposes one; synthesizing another would double-match `le="+Inf"`.
	// +2 for the _sum and _count series.
	out := make([]series, 0, len(histogram.GetBucket())+2)

	out = append(out,
		series{Name: name + "_sum", Labels: copyLabels(base), Value: histogram.GetSampleSum()},
		series{Name: name + "_count", Labels: copyLabels(base), Value: float64(histogram.GetSampleCount())},
	)

	for _, bucket := range histogram.GetBucket() {
		labels := copyLabels(base)
		labels[labelLE] = formatFloat(bucket.GetUpperBound())
		out = append(out, series{
			Name: name + "_bucket", Labels: labels, Value: float64(bucket.GetCumulativeCount()),
		})
	}

	return out
}

func copyLabels(src map[string]string) map[string]string {
	out := make(map[string]string, len(src))
	for k, v := range src {
		out[k] = v
	}

	return out
}

// formatFloat renders a bucket bound / quantile the way the text exposition
// format does, so `le: "0.5"` in a config matches.
func formatFloat(f float64) string {
	if math.IsInf(f, 1) {
		return "+Inf"
	}

	return strconv.FormatFloat(f, 'g', -1, 64)
}

// selectSeries returns the series whose name equals metric and whose labels
// are a superset of the wanted label set (exact value match).
func selectSeries(all []series, metric string, wanted map[string]string) []series {
	matched := make([]series, 0, 1)

	for idx := range all {
		if all[idx].Name != metric {
			continue
		}

		if !labelsMatch(all[idx].Labels, wanted) {
			continue
		}

		matched = append(matched, all[idx])
	}

	return matched
}

// labelsMatch reports whether have contains every wanted key with the wanted
// value (subset matching — extra labels on the series are fine).
func labelsMatch(have, wanted map[string]string) bool {
	for k, v := range wanted {
		if have[k] != v {
			return false
		}
	}

	return true
}

// aggregate reduces the matched series to a single value using the configured
// strategy. `single` never reaches here with more than one series — the caller
// turns that into an explicit ambiguity error.
func aggregate(matched []series, match string) (float64, error) {
	values := make([]float64, 0, len(matched))
	for idx := range matched {
		values = append(values, matched[idx].Value)
	}

	switch match {
	case MatchSingle:
		return values[0], nil
	case MatchMin:
		out := values[0]
		for _, v := range values[1:] {
			out = math.Min(out, v)
		}

		return out, nil
	case MatchMax:
		out := values[0]
		for _, v := range values[1:] {
			out = math.Max(out, v)
		}

		return out, nil
	case MatchSum:
		return sum(values), nil
	case MatchAvg:
		return sum(values) / float64(len(values)), nil
	default:
		return 0, fmt.Errorf("%w: %q", errAggregationUnknown, match)
	}
}

func sum(values []float64) float64 {
	var out float64
	for _, v := range values {
		out += v
	}

	return out
}

// describeLabels renders a label set deterministically for the result output.
func describeLabels(labels map[string]string) string {
	if len(labels) == 0 {
		return "{}"
	}

	keys := make([]string, 0, len(labels))
	for k := range labels {
		keys = append(keys, k)
	}

	sort.Strings(keys)

	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s=%q", k, labels[k]))
	}

	return "{" + strings.Join(parts, ",") + "}"
}
