package checkdns

import (
	"fmt"

	checkconfig "github.com/fclairamb/solidping/server/internal/checkers/checkdns/config"
	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

const (
	// OutputKeyBaselineCapture carries the normalized answer of a region that
	// has no baseline yet; the server stores it (spec 2026-10-03-04).
	OutputKeyBaselineCapture = checkconfig.OutputKeyBaselineCapture
	// OutputKeyChanges carries {"added": [...], "removed": [...]} when the
	// answer differs from the region's baseline.
	OutputKeyChanges = "changes"
	// MetricChanged is 1 when the answer differs from the baseline, else 0.
	MetricChanged = "changed"
)

// applyChangeDetection compares a successful answer with the selected region's
// baseline (spec 2026-10-03-04). Without a baseline it asks the server to
// capture one; with one, any added or removed value is a change. The worse of
// the expected-values status and the change status wins.
func applyChangeDetection(cfg *DNSConfig, recordType string, answer []string, result *checkerdef.Result) {
	normalized := checkconfig.NormalizeValues(recordType, answer)

	baseline, ok := cfg.RegionBaseline()
	if !ok {
		result.Output[OutputKeyBaselineCapture] = normalized
		result.Metrics[MetricChanged] = 0

		return
	}

	added, removed := diffValues(checkconfig.NormalizeValues(recordType, baseline), normalized)
	if len(added) == 0 && len(removed) == 0 {
		result.Metrics[MetricChanged] = 0

		return
	}

	result.Metrics[MetricChanged] = 1
	result.Output[OutputKeyChanges] = map[string]any{
		"added":   added,
		"removed": removed,
	}

	if cfg.EffectiveOnChange() == checkconfig.OnChangeWarning {
		if result.Status == checkerdef.StatusUp {
			result.Status = checkerdef.StatusWarning
		}

		return
	}

	result.Status = checkerdef.StatusDown

	if _, exists := result.Output[checkerdef.OutputKeyError]; !exists {
		result.Output[checkerdef.OutputKeyError] = fmt.Sprintf(
			"DNS records changed: +%d −%d", len(added), len(removed),
		)
	}
}

// diffValues returns the values of answer missing from baseline (added) and
// those of baseline missing from answer (removed). Both inputs are normalized,
// so the outputs are sorted.
func diffValues(baseline, answer []string) ([]string, []string) {
	inBaseline := make(map[string]bool, len(baseline))
	for _, v := range baseline {
		inBaseline[v] = true
	}

	inAnswer := make(map[string]bool, len(answer))
	for _, v := range answer {
		inAnswer[v] = true
	}

	added := []string{}

	for _, v := range answer {
		if !inBaseline[v] {
			added = append(added, v)
		}
	}

	removed := []string{}

	for _, v := range baseline {
		if !inAnswer[v] {
			removed = append(removed, v)
		}
	}

	return added, removed
}
