package uptimebar

import (
	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

// DominantStatus selects the status that represents a window of probes.
//
// It lived in jobs/jobtypes/job_aggregation.go as calculateDominantStatus, which
// is still its only persisting caller: this is the status the aggregation job
// writes onto an hour/day/month rollup row. It moved here — verbatim, with its
// rules intact — because a second reader now needs exactly the same answer: the
// status page's response-time seam bins raw probes in SQL and classifies each bin
// (spec 2026-09-22-06). A seam bin sits on the chart immediately next to the hour
// rollups that will replace it as raw is compacted away, so the two must
// classify identically; a transcription of these three rules next to the seam
// would be a slow-motion divergence between a point and the point that succeeds
// it.
//
//  1. A dominating hard failure (Down/Timeout/Error, by most-frequent with a
//     Severity() tie-break) wins outright — a tie between a failure and
//     anything else resolves to the failure, never to Warning/Degraded.
//  2. Otherwise, if the window contained anything "to report" — a raw Warning
//     (raw rollups) or an already-promoted Degraded child (hour→day→month
//     rollups) — the row is promoted to the aggregated Degraded status
//     ("there was something to report in this window") — Decision D,
//     promotion rule "any warning".
//  3. Otherwise the dominant non-failing status (Up, or a lifecycle marker
//     when that is all there was).
func DominantStatus(statusCounts map[int]int) int {
	if len(statusCounts) == 0 {
		return 0
	}

	dominantStatus := 0
	maxCount := 0
	hasSomethingToReport := false

	for status, count := range statusCounts {
		if status == int(checkerdef.StatusWarning) || status == int(checkerdef.StatusDegraded) {
			hasSomethingToReport = true
		}

		// Most occurrences wins; ties broken by gravity (Severity()), not by
		// raw numeric value — numbers no longer encode severity once
		// Degraded=7 / Warning=8 exist.
		if count > maxCount ||
			(count == maxCount && checkerdef.Status(status).Severity() > checkerdef.Status(dominantStatus).Severity()) {
			dominantStatus = status
			maxCount = count
		}
	}

	// A dominating hard failure always wins — including a tie that resolved to
	// the failure above (Severity ranks failures highest).
	if checkerdef.Status(dominantStatus).Severity() >= checkerdef.StatusDown.Severity() {
		return dominantStatus
	}

	// Non-failing window containing a raw Warning or a Degraded child →
	// promote/carry the aggregated Degraded status.
	if hasSomethingToReport {
		return int(checkerdef.StatusDegraded)
	}

	return dominantStatus
}
