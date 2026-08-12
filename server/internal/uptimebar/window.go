package uptimebar

import (
	"context"
	"time"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

// windowQueryLimit bounds the single window query. A 1-minute check over a full
// year touches at most raw≈1 440 (capped by RetentionRaw, ~24 h) + hourly≈168
// (capped by RetentionHour, 7 d default) + daily≈60 (capped by RetentionDay,
// 2 mo default) + monthly≈12/yr rows per check-region — a few thousand even for
// decade-long windows. We use a generous ceiling so the accumulation never
// silently truncates older buckets the way the old client-side size:1000 cap
// did. This data is summed server-side and never shipped to the browser.
const windowQueryLimit = 200_000

// WindowAvailability runs ONE raw+hour+day+month union query over [start, end)
// for all checks and accumulates every row into a single BucketStats per check.
// Unlike BucketAvailability (which keys per time-bucket for a tick strip), this
// folds the whole window into one aggregate — exactly what a per-period
// availability number needs.
//
// Because the aggregation job deletes source rows after each rollup (raw → hour →
// day → month), the four tiers cover non-overlapping age bands, so unioning them
// never double-counts. The month tier is terminal — never rolled further, never
// deleted — so the union covers the check's entire history regardless of how the
// raw/hour/day retention is tuned (the live defaults keep only 24 h / 7 d / 2 mo;
// see jobtypes' defaultRetention* constants). Without month in the union, a 365d
// window on a default deployment silently saw only ~2 months of data.
//
// Edge granularity: a rollup row is included iff its period_start falls inside
// [start, end) — the same rule for every tier — so a rollup straddling the
// window's left edge is excluded even though part of its span is wanted. For the
// month tier that means up to a month of the oldest edge may be missing from a
// duration window; calendar windows (mtd/ytd) start on month boundaries and are
// exact.
//
// Counting rules stay canonical: raw rows go through accumulateRaw (lifecycle
// markers excluded; up + warning count as success) and rollup rows through
// accumulateAgg (SuccessfulChecks already encodes the rule). A check absent from
// the returned map (or with BucketStats.Total == 0) had no data in the window —
// the caller renders that as "no data", not "100%".
func WindowAvailability(
	ctx context.Context, db ResultsLister, orgUID string, checkUIDs []string,
	start, end time.Time,
) (map[string]BucketStats, error) {
	out := make(map[string]BucketStats, len(checkUIDs))

	if len(checkUIDs) == 0 || !end.After(start) {
		return out, nil
	}

	startUTC := start.UTC()
	endUTC := end.UTC()

	filter := &models.ListResultsFilter{
		OrganizationUID: orgUID,
		CheckUIDs:       checkUIDs,
		PeriodTypes: []string{
			models.PeriodTypeRaw, models.PeriodTypeHour, models.PeriodTypeDay, models.PeriodTypeMonth,
		},
		PeriodStartAfter: &startUTC,
		// PeriodEndBefore filters on period_start < value (see ListResultsFilter),
		// so this bounds the upper edge of the window to [start, end).
		PeriodEndBefore: &endUTC,
		Limit:           windowQueryLimit,
		// Availability is computed from status/counts only — never from the
		// metrics/output blobs — and this query can pull thousands of rows per
		// request, so skip them entirely (spec 2026-07-24-02 §5).
		SkipBlobs: true,
	}

	resp, err := db.ListResults(ctx, filter)
	if err != nil {
		return nil, err
	}

	if resp == nil {
		return out, nil
	}

	for _, result := range resp.Results {
		acc := out[result.CheckUID]

		if result.PeriodType == models.PeriodTypeRaw {
			acc.accumulateRaw(result)
		} else {
			acc.accumulateAgg(result)
		}

		out[result.CheckUID] = acc
	}

	return out, nil
}
