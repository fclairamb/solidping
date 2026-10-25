package plugins

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

// Result caps applied to every plugin transport (spec 2026-10-17-05 §10).
const (
	// MaxOutputBytes caps the JSON size of a result's Output.
	MaxOutputBytes = 16 * 1024
	// MaxMetrics caps the number of metrics a result carries.
	MaxMetrics = 32
	// OutputKeyTruncated lists what LimitResult cut from a result.
	OutputKeyTruncated = "truncated"
	outputKeyMessage   = "message"
)

// LimitResult enforces the plugin result contract: a known status, an Output
// under MaxOutputBytes and at most MaxMetrics numeric metrics. Anything over a
// cap is cut and listed under Output["truncated"], never dropped silently.
func LimitResult(result *checkerdef.Result) *checkerdef.Result {
	if result == nil {
		return &checkerdef.Result{
			Status: checkerdef.StatusError,
			Output: map[string]any{checkerdef.OutputKeyError: "plugin returned no result"},
		}
	}

	var cut []string

	if !knownResultStatus(result.Status) {
		cut = append(cut, fmt.Sprintf("status %d is not a known status", int(result.Status)))
		result.Status = checkerdef.StatusError
	}

	var metricCuts []string
	result.Metrics, metricCuts = limitMetrics(result.Metrics)
	cut = append(cut, metricCuts...)

	output, outputCut := limitOutput(result.Output)
	if outputCut {
		cut = append(cut, fmt.Sprintf("output over %d bytes", MaxOutputBytes))
	}

	if len(cut) > 0 {
		if output == nil {
			output = map[string]any{}
		}

		output[OutputKeyTruncated] = cut
	}

	result.Output = output

	return result
}

func knownResultStatus(status checkerdef.Status) bool {
	switch status {
	case checkerdef.StatusUp, checkerdef.StatusDown, checkerdef.StatusTimeout,
		checkerdef.StatusError, checkerdef.StatusWarning:
		return true
	case checkerdef.StatusRunning, checkerdef.StatusDegraded:
		return false
	default:
		return false
	}
}

// limitMetrics keeps the numeric, finite metrics, at most MaxMetrics of them
// in key order.
func limitMetrics(metrics map[string]any) (map[string]any, []string) {
	if len(metrics) == 0 {
		return metrics, nil
	}

	keys := make([]string, 0, len(metrics))
	for key := range metrics {
		keys = append(keys, key)
	}

	sort.Strings(keys)

	out := make(map[string]any, min(len(metrics), MaxMetrics))

	var cut []string

	dropped := 0

	for _, key := range keys {
		value, ok := numeric(metrics[key])
		if !ok {
			cut = append(cut, fmt.Sprintf("metric %q is not numeric", key))

			continue
		}

		if len(out) >= MaxMetrics {
			dropped++

			continue
		}

		out[key] = value
	}

	if dropped > 0 {
		cut = append(cut, fmt.Sprintf("%d metrics over the %d cap", dropped, MaxMetrics))
	}

	return out, cut
}

func numeric(value any) (float64, bool) {
	var number float64

	switch typed := value.(type) {
	case float64:
		number = typed
	case float32:
		number = float64(typed)
	case int:
		number = float64(typed)
	case int64:
		number = float64(typed)
	case int32:
		number = float64(typed)
	case uint64:
		number = float64(typed)
	case json.Number:
		parsed, err := typed.Float64()
		if err != nil {
			return 0, false
		}

		number = parsed
	default:
		return 0, false
	}

	if math.IsNaN(number) || math.IsInf(number, 0) {
		return 0, false
	}

	return number, true
}

// limitOutput keeps Output under MaxOutputBytes: keys are kept in order
// (message and error first), a string that does not fit is shortened, a value
// that does not fit at all is dropped.
func limitOutput(output map[string]any) (map[string]any, bool) {
	if len(output) == 0 || jsonSize(output) <= MaxOutputBytes {
		return output, false
	}

	keys := make([]string, 0, len(output))
	for key := range output {
		keys = append(keys, key)
	}

	sort.Slice(keys, func(i, j int) bool {
		return outputKeyRank(keys[i]) < outputKeyRank(keys[j]) ||
			(outputKeyRank(keys[i]) == outputKeyRank(keys[j]) && keys[i] < keys[j])
	})

	// Room kept for the truncation note itself.
	const reserve = 512

	budget := MaxOutputBytes - reserve
	out := make(map[string]any, len(output))

	for _, key := range keys {
		value := output[key]
		size := jsonSize(map[string]any{key: value})

		if size <= budget {
			out[key] = value
			budget -= size

			continue
		}

		if text, ok := value.(string); ok {
			room := budget - len(key) - 16
			if room > 0 {
				short := truncateUTF8(text, room)
				out[key] = short
				budget -= jsonSize(map[string]any{key: short})
			}
		}
	}

	return out, true
}

func outputKeyRank(key string) int {
	switch key {
	case outputKeyMessage:
		return 0
	case checkerdef.OutputKeyError:
		return 1
	default:
		return 2
	}
}

func jsonSize(value any) int {
	raw, err := json.Marshal(value)
	if err != nil {
		return math.MaxInt32
	}

	return len(raw)
}

// truncateUTF8 cuts text to at most n bytes without splitting a rune, and
// shrinks further when JSON escaping makes it longer than n.
func truncateUTF8(text string, n int) string {
	runes := []rune(text)

	// The JSON size of a prefix grows with its length: binary search the
	// longest prefix that fits.
	low, high := 0, min(len(runes), n)
	for low < high {
		mid := (low + high + 1) / 2
		if jsonSize(string(runes[:mid])) <= n {
			low = mid
		} else {
			high = mid - 1
		}
	}

	return string(runes[:low])
}
