package models

// RollupGroupStatus derives a single, read-time status for a check group from
// the per-status counts of its considered member checks (spec
// 2026-08-01-01). Callers must pre-filter counts to enabled, non-deleted
// members — this function has no opinion on which checks are "in" the group,
// only on how to combine their statuses.
//
// Deliberately Better-Stack-shaped: partial failure reads as "degraded" (not
// a blanket "down"), and a warning-only member doesn't paint the whole group
// red. Rules, in priority order:
//
//  1. No considered members (or the map only contains CheckStatusCreated
//     entries) → CheckStatusCreated.
//  2. All considered members down → CheckStatusDown.
//  3. Some but not all down → CheckStatusDegraded.
//  4. No down, at least one warning → CheckStatusWarning.
//  5. No down/warning, at least one validating → CheckStatusValidating.
//  6. Otherwise, at least one up → CheckStatusUp.
//  7. Falls back to CheckStatusCreated (e.g. members whose status doesn't
//     match any of the above — in practice only CheckStatusCreated, since
//     CheckStatusDegraded is never a live per-check status).
func RollupGroupStatus(counts map[CheckStatus]int) CheckStatus {
	total := 0
	for _, n := range counts {
		total += n
	}

	if total == 0 {
		return CheckStatusCreated
	}

	down := counts[CheckStatusDown]

	switch {
	case down == total:
		return CheckStatusDown
	case down > 0:
		return CheckStatusDegraded
	case counts[CheckStatusWarning] > 0:
		return CheckStatusWarning
	case counts[CheckStatusValidating] > 0:
		return CheckStatusValidating
	case counts[CheckStatusUp] > 0:
		return CheckStatusUp
	default:
		return CheckStatusCreated
	}
}
