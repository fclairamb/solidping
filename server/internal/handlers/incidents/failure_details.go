package incidents

import (
	"encoding/json"
	"sort"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/db/models"
)

// Keys written into incident.Details. failureReasonDetailsKey and
// firstResultDetailsKey are read back by the Slack notification formatters
// (internal/notifications/slack.go, internal/integrations/slack/interactions.go)
// via getFailureReason — keep the strings in sync if either side changes.
const (
	failureReasonDetailsKey = "failure_reason"
	firstResultDetailsKey   = "first_result"
	lastFailureDetailsKey   = "last_failure"
)

// Keys inside a first_result / last_failure snapshot object.
const (
	snapshotKeyResultUID   = "resultUid"
	snapshotKeyStatus      = "status"
	snapshotKeyRegion      = "region"
	snapshotKeyDuration    = "duration"
	snapshotKeyPeriodStart = "periodStart"
	snapshotKeyOutput      = "output"
)

// maxFailureSnapshotBytes bounds the marshaled size of a first_result /
// last_failure snapshot kept on the incident row. Results are produced by
// checkers and never contain check-config secrets (decrypt/merge happens at
// dispatch and the plaintext stays in the worker), so this is purely a size
// concern — an oversized per-checker output map (long response bodies,
// verbose DNS records, ...) must not blow up the incidents table.
const maxFailureSnapshotBytes = 8 * 1024

// maxOutputFieldStringLen bounds an individual output string value before the
// map-level drop pass runs, so one giant field doesn't force everything else
// out.
const maxOutputFieldStringLen = 512

// failureDetails builds the incident.Details snapshot written when an
// incident opens: failure_reason (the exact key the Slack formatters already
// read) plus a first_result snapshot of the triggering result, copied (not
// referenced) because aggregation retention deletes raw results.
func failureDetails(result *models.Result) models.JSONMap {
	return models.JSONMap{
		failureReasonDetailsKey: failureReasonFromResult(result),
		firstResultDetailsKey:   resultSnapshot(result),
	}
}

// lastFailureDetails returns a copy of the incident's existing Details with a
// last_failure snapshot of the relapse's failing result added, preserving the
// original first_result and failure_reason untouched. Safe to call with a nil
// existing map (e.g. an incident predating this feature).
func lastFailureDetails(existing models.JSONMap, result *models.Result) models.JSONMap {
	merged := make(models.JSONMap, len(existing)+1)
	for k, v := range existing {
		merged[k] = v
	}
	merged[lastFailureDetailsKey] = resultSnapshot(result)

	return merged
}

// failureReasonFromResult returns Output[checkerdef.OutputKeyError] when
// present, else a status-derived fallback. This is what makes the existing
// (previously dead) Slack getFailureReason path light up.
func failureReasonFromResult(result *models.Result) string {
	if result != nil && result.Output != nil {
		if errStr, ok := result.Output[checkerdef.OutputKeyError].(string); ok && errStr != "" {
			return errStr
		}
	}

	if result == nil || result.Status == nil {
		return "down"
	}

	switch models.ResultStatus(*result.Status) {
	case models.ResultStatusTimeout:
		return "timeout"
	case models.ResultStatusError:
		return "error"
	default:
		return "down"
	}
}

// resultSnapshot captures enough of a raw result to diagnose an incident
// after the fact: resultUid, status, region, duration, periodStart, and the
// (size-capped) output map. Retention deletes the raw row, so this is the
// only place these fields survive past the retention window.
func resultSnapshot(result *models.Result) models.JSONMap {
	if result == nil {
		return models.JSONMap{}
	}

	status := ""
	if result.Status != nil {
		status = models.StatusToString(*result.Status)
	}

	region := ""
	if result.Region != nil {
		region = *result.Region
	}

	var duration float32
	if result.Duration != nil {
		duration = *result.Duration
	}

	return models.JSONMap{
		snapshotKeyResultUID:   result.UID,
		snapshotKeyStatus:      status,
		snapshotKeyRegion:      region,
		snapshotKeyDuration:    duration,
		snapshotKeyPeriodStart: result.PeriodStart,
		snapshotKeyOutput:      cappedOutput(result.Output),
	}
}

// cappedOutput returns a copy of output bounded to maxFailureSnapshotBytes
// once marshaled, always keeping checkerdef.OutputKeyError intact. Oversized
// string values are truncated first; if that alone isn't enough, the largest
// remaining keys are dropped (deterministic: largest marshaled size first)
// until the map fits.
func cappedOutput(output models.JSONMap) models.JSONMap {
	if len(output) == 0 {
		return models.JSONMap{}
	}

	if raw, err := json.Marshal(output); err == nil && len(raw) <= maxFailureSnapshotBytes {
		return output
	}

	capped := make(models.JSONMap, len(output))

	type sizedKey struct {
		key  string
		size int
	}

	others := make([]sizedKey, 0, len(output))

	for k, v := range output {
		if k == checkerdef.OutputKeyError {
			capped[k] = v
			continue
		}

		if s, ok := v.(string); ok && len(s) > maxOutputFieldStringLen {
			v = s[:maxOutputFieldStringLen] + "…(truncated)"
		}

		size := 0
		if raw, err := json.Marshal(v); err == nil {
			size = len(raw)
		}

		capped[k] = v
		others = append(others, sizedKey{key: k, size: size})
	}

	sort.Slice(others, func(i, j int) bool { return others[i].size > others[j].size })

	for _, o := range others {
		raw, err := json.Marshal(capped)
		if err == nil && len(raw) <= maxFailureSnapshotBytes {
			break
		}

		delete(capped, o.key)
	}

	return capped
}
