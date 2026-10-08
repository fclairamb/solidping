package config

import (
	"slices"
	"strings"
)

// OutputKeyBaselineCapture is the result output key a dns run uses to hand
// the server a region's first normalized answer (spec 2026-10-03-04).
const OutputKeyBaselineCapture = "baseline_capture"

// NormalizeValues is the one normalization of an answer, applied both to a
// fresh answer and to a stored baseline before they are compared: trim spaces,
// strip a trailing dot, lower-case (except TXT, whose content is
// case-sensitive), dedupe and sort. MX values keep their "priority host"
// shape, so two records differing only by priority stay distinct.
func NormalizeValues(recordType string, values []string) []string {
	keepCase := strings.EqualFold(recordType, RecordTypeTXT)
	out := make([]string, 0, len(values))

	for _, value := range values {
		value = strings.TrimSpace(value)
		if !keepCase {
			value = strings.ToLower(strings.TrimSuffix(value, "."))
		}

		if value == "" {
			continue
		}

		out = append(out, value)
	}

	slices.Sort(out)

	return slices.Compact(out)
}
