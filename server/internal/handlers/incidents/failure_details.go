package incidents

import (
	"encoding/json"
	"fmt"
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

// Status-derived failure_reason fallbacks, used when the result carries no
// checkerdef.OutputKeyError string.
const (
	reasonDown    = "down"
	reasonTimeout = "timeout"
	reasonError   = "error"
)

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
		return reasonDown
	}

	switch models.ResultStatus(*result.Status) {
	case models.ResultStatusTimeout:
		return reasonTimeout
	case models.ResultStatusError:
		return reasonError
	// ResultStatusAbandoned sits here rather than beside Error on purpose: a
	// reaped attempt is not a failure the monitored service produced, and it
	// can never reach this path anyway (the reaper writes the row directly and
	// incident processing skips statuses that are neither success, failure nor
	// warning). Listed explicitly so the exhaustive linter stays a real check.
	case models.ResultStatusCreated, models.ResultStatusRunning, models.ResultStatusUp,
		models.ResultStatusDown, models.ResultStatusDegraded, models.ResultStatusWarning,
		models.ResultStatusAbandoned:
		return reasonDown
	default:
		return reasonDown
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
// once marshaled. checkerdef.OutputKeyError is always present and is never
// dropped by the ordinary passes below, but it is not exempt from the cap
// itself: oversized string values (other keys) are truncated first, then the
// largest remaining keys are dropped (deterministic: largest marshaled size
// first) until the map fits, and — as a last resort, if the error value
// alone still exceeds the budget once it's the only key left — the error
// string itself is truncated by truncateErrorToFit so the result never
// exceeds maxFailureSnapshotBytes.
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

	for key, fieldValue := range output {
		if key == checkerdef.OutputKeyError {
			capped[key] = fieldValue
			continue
		}

		if s, ok := fieldValue.(string); ok && len(s) > maxOutputFieldStringLen {
			fieldValue = s[:maxOutputFieldStringLen] + "…(truncated)"
		}

		size := 0
		if raw, err := json.Marshal(fieldValue); err == nil {
			size = len(raw)
		}

		capped[key] = fieldValue
		others = append(others, sizedKey{key: key, size: size})
	}

	sort.Slice(others, func(i, j int) bool { return others[i].size > others[j].size })

	for _, entry := range others {
		raw, err := json.Marshal(capped)
		if err == nil && len(raw) <= maxFailureSnapshotBytes {
			break
		}

		delete(capped, entry.key)
	}

	// Last resort: every other key has been dropped and the map (now
	// dominated by checkerdef.OutputKeyError alone) still doesn't fit —
	// truncate the error value itself rather than exceed the cap.
	if raw, err := json.Marshal(capped); err != nil || len(raw) > maxFailureSnapshotBytes {
		truncateErrorToFit(capped)
	}

	return capped
}

// truncateErrorToFit shrinks capped[checkerdef.OutputKeyError] down to the
// longest prefix (plus a "…(truncated)" suffix) that keeps the whole map
// within maxFailureSnapshotBytes once marshaled. Called only after every
// other output key has already been dropped, so in practice this is
// binary-searching against a map containing just that one field — cheap
// even for a very large error string. Truncates on rune boundaries (not raw
// bytes) so the result stays valid UTF-8; non-string error values are
// stringified first so the cap still applies to them.
func truncateErrorToFit(capped models.JSONMap) {
	raw, ok := capped[checkerdef.OutputKeyError]
	if !ok {
		return
	}

	original, isString := raw.(string)
	if !isString {
		original = fmt.Sprintf("%v", raw)
	}

	const suffix = "…(truncated)"

	runes := []rune(original)
	lowRunes, highRunes, best := 0, len(runes), suffix

	for lowRunes <= highRunes {
		mid := (lowRunes + highRunes) / 2

		candidate := string(runes[:mid])
		if mid < len(runes) {
			candidate += suffix
		}

		capped[checkerdef.OutputKeyError] = candidate

		if marshaled, err := json.Marshal(capped); err == nil && len(marshaled) <= maxFailureSnapshotBytes {
			best = candidate
			lowRunes = mid + 1
		} else {
			highRunes = mid - 1
		}
	}

	capped[checkerdef.OutputKeyError] = best
}
