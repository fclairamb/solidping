package uptimebar

import (
	"context"
	"time"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

// windowQueryLimit bounds the single window query. A 1-minute check over a full
// year touches at most raw≈1 440 (capped by RetentionRaw, ~24 h) + hourly≈720 +
// daily≈365 rows per check ≈ 2.5 k. We use a generous ceiling so the accumulation
// never silently truncates older buckets the way the old client-side size:1000
// cap did. This data is summed server-side and never shipped to the browser.
const windowQueryLimit = 200_000

// WindowAvailability runs ONE raw+hour+day union query over [start, end) for all
// checks and accumulates every row into a single BucketStats per check. Unlike
// BucketAvailability (which keys per time-bucket for a tick strip), this folds the
// whole window into one aggregate — exactly what a per-period availability number
// needs.
//
// Because the aggregation job deletes source rows after each rollup (raw → hour →
// day → month), the three tiers cover non-overlapping age bands, so unioning them
// never double-counts. With the default retention tiers (raw 24 h, hour 30 d, day
// 12 mo) the union covers 0 → ~365 d exactly — every period the check-detail
// Availability table asks for.
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
		OrganizationUID:  orgUID,
		CheckUIDs:        checkUIDs,
		PeriodTypes:      []string{models.PeriodTypeRaw, models.PeriodTypeHour, models.PeriodTypeDay},
		PeriodStartAfter: &startUTC,
		// PeriodEndBefore filters on period_start < value (see ListResultsFilter),
		// so this bounds the upper edge of the window to [start, end).
		PeriodEndBefore: &endUTC,
		Limit:           windowQueryLimit,
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
