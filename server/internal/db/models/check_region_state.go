package models

import (
	"time"

	"github.com/uptrace/bun"
)

// CheckRegionState is the newest real reading of one check in one region
// (spec 2026-09-25-10). The incident pipeline keeps one row per (check,
// region) so a multi-region check can tell "one region failing" from "the
// check is failing", instead of treating every result as if it described the
// whole check.
//
// Rows are written for checks with two or more regions only. A row whose
// region the check has since left (an automatic re-placement) is never
// pruned: the quorum evaluation counts only the check's CURRENT regions, so a
// left-behind row is inert by construction rather than by cleanup.
type CheckRegionState struct {
	bun.BaseModel `bun:"table:check_region_states"`

	CheckUID        string `bun:"check_uid,pk"`
	Region          string `bun:"region,pk"`
	OrganizationUID string `bun:"organization_uid,notnull"`
	// Status is the ResultStatus of the region's newest real result (up, down,
	// timeout, error or warning).
	Status ResultStatus `bun:"status,notnull"`
	// StatusSince is when the region entered its current side (failing or
	// passing). Display only: "tokyo failing since 13:47".
	StatusSince time.Time `bun:"status_since,notnull"`
	// LastResultAt is the execution time of that newest result. A row older
	// than the check's staleness threshold does not count toward the quorum.
	LastResultAt time.Time `bun:"last_result_at,notnull"`
	UpdatedAt    time.Time `bun:"updated_at,notnull"`
}

// IsFailure reports whether a result status counts as a failing reading for
// the quorum: down, timeout or error. Warning is "up, but something to
// report" and counts as passing, exactly as it does for a single region.
func (s ResultStatus) IsFailure() bool {
	return s == ResultStatusDown || s == ResultStatusTimeout || s == ResultStatusError
}
