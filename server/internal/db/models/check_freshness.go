package models

import "time"

// CheckLiveState is the slice of a check row the incident pipeline decides
// from: status, streak and the two incident clocks. It is re-read from the
// database at the top of every real result (db.Service.TouchCheckLastResult)
// because the *models.Check handed to ProcessCheckResult may be a claim-time
// snapshot — and between the claim and the result the freshness sweep may have
// moved the row to CheckStatusStale and cleared both clocks (spec 2026-09-25-02).
// Deciding against the snapshot would miss that stale→up flip entirely and
// would resurrect the clocks the gap was meant to discard.
type CheckLiveState struct {
	Status                     CheckStatus `bun:"status"`
	StatusStreak               int         `bun:"status_streak"`
	StatusChangedAt            *time.Time  `bun:"status_changed_at"`
	FirstFailureAt             *time.Time  `bun:"first_failure_at"`
	FirstSuccessSinceFailureAt *time.Time  `bun:"first_success_since_failure_at"`
}

// Apply copies the live state onto an in-memory check.
func (s *CheckLiveState) Apply(check *Check) {
	check.Status = s.Status
	check.StatusStreak = s.StatusStreak
	check.StatusChangedAt = s.StatusChangedAt
	check.FirstFailureAt = s.FirstFailureAt
	check.FirstSuccessSinceFailureAt = s.FirstSuccessSinceFailureAt
}

// RegionLastResult is the newest real result of one check in one region
// (spec 2026-09-25-02 per-region freshness). Region is "" for results that
// carry no region.
type RegionLastResult struct {
	Region       string    `bun:"region"`
	LastResultAt time.Time `bun:"last_at"`
}

// StaleCheckPlacement is one (stale check, placement region) pair: a stale
// check joined to each of its check_jobs rows. Region is "" for an any-region
// job. The watchdog's stale-checks detector and the solidping_checks_stale
// gauge both read it.
type StaleCheckPlacement struct {
	CheckUID         string     `bun:"check_uid"`
	OrganizationUID  string     `bun:"organization_uid"`
	OrganizationSlug string     `bun:"organization_slug"`
	CheckSlug        *string    `bun:"check_slug"`
	CheckName        *string    `bun:"check_name"`
	Region           string     `bun:"region"`
	LastResultAt     *time.Time `bun:"last_result_at"`
	CreatedAt        time.Time  `bun:"created_at"`
}

// realResultStatuses are the result statuses that count as a real reading of
// the service for freshness: never the created/running lifecycle markers, the
// aggregated-only degraded, or the reaper-minted abandoned rows.
//
//nolint:gochecknoglobals // static lookup table, treated as a constant.
var realResultStatuses = []int{
	int(ResultStatusUp), int(ResultStatusDown), int(ResultStatusTimeout),
	int(ResultStatusError), int(ResultStatusWarning),
}

// RealResultStatuses returns the result status codes that count as a real
// reading for freshness (a fresh copy, safe to hand to bun.List).
func RealResultStatuses() []int {
	return append([]int(nil), realResultStatuses...)
}

// IsRealForFreshness reports whether a result status is a real reading.
func (s ResultStatus) IsRealForFreshness() bool {
	for _, real := range realResultStatuses {
		if int(s) == real {
			return true
		}
	}

	return false
}
