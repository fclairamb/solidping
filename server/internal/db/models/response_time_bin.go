package models

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"
)

// Errors returned by ResponseTimeBinFilter.Validate.
var (
	// ErrResponseTimeBinsNoOrganization is returned when the filter names no
	// organization — every index on `results` leads with organization_uid.
	ErrResponseTimeBinsNoOrganization = errors.New("response time bins: organization uid is required")
	// ErrResponseTimeBinsNoBinDuration is returned for a non-positive bin width,
	// which has no bin grid to group by.
	ErrResponseTimeBinsNoBinDuration = errors.New("response time bins: a positive bin duration is required")
	// ErrResponseTimeBinsNoSince is returned when the filter carries no lower
	// bound. An unbounded aggregate over the raw tier of `results` is exactly the
	// scan this query shape exists to avoid.
	ErrResponseTimeBinsNoSince = errors.New("response time bins: a period_start lower bound is required")
)

// ResponseTimeBinFilter describes the status page's response-time SEAM read: fold
// the raw probes of these checks into one row per (check, region, bin).
//
// It is deliberately not a ResultBucketFilter, even though both bin `results` on
// the same grid. That one answers the AVAILABILITY bar: it sums across regions,
// carries no percentile, and returns the eleven counters BucketStats folds. This
// one answers the RESPONSE-TIME chart: region is a GROUP BY key (each region is
// its own series on that chart), it carries a p95 and a status mix, and it never
// reads the rollup tier — the chart's rollup half is still a row fetch
// (RecentResultsPerCheck), because a rollup row already IS one point.
//
// Spec 2026-09-22-06. Before it, the seam half of that chart fetched every raw
// probe in the window — ~1 337 rows per check on a 7-day, 200-check page, 292 843
// in total — and Go kept roughly one in ninety of them, plotting single probes as
// if they were representative.
type ResponseTimeBinFilter struct {
	// OrganizationUID scopes the aggregate. Required.
	OrganizationUID string
	// CheckUIDs are the checks to fold. An empty list yields no bins.
	CheckUIDs []string
	// Since is the inclusive period_start lower bound. Required, and it must be
	// the SAME clamp the raw tier of every other reader uses
	// (uptimebar.RawTierStart): the clamp is what keeps the seam and the rollups
	// disjoint, so a wider bound here would double-count a bucket that has
	// already been rolled up but whose raw rows the aggregation job has not yet
	// deleted.
	Since time.Time
	// BinDuration is the bin width. Must be > 0. Probes are binned against
	// ProlepticEpochOffsetSeconds' origin, so the grid matches
	// time.Truncate(BinDuration) exactly — and therefore matches the grid the
	// availability aggregate (ResultBucketFilter) and the Go fold both use.
	BinDuration time.Duration
}

// Validate rejects a filter no dialect may execute.
func (f *ResponseTimeBinFilter) Validate() error {
	if f.OrganizationUID == "" {
		return ErrResponseTimeBinsNoOrganization
	}

	if f.BinDuration <= 0 {
		return fmt.Errorf("%w: %s", ErrResponseTimeBinsNoBinDuration, f.BinDuration)
	}

	if f.Since.IsZero() {
		return ErrResponseTimeBinsNoSince
	}

	return nil
}

// ResponseTimeBin is one (check, region, bin) group of the seam aggregate: the
// numbers a single response-time point on the status page's chart is made of.
//
// Every duration pointer is nil — not zero — when no probe in the bin carried a
// duration. A bin like that still comes back (a check that was down for fifteen
// minutes recorded probes and no response times), because the point's
// availability coloring is computed from Total/Up and must stay honest; it is
// the DURATION that is missing, and the chart drops a point with no duration on
// its own.
type ResponseTimeBin struct {
	// CheckUID, Region and BinStart are the group key. Region is nil for the
	// NULL/legacy region, exactly as on the raw rows. BinStart is UTC.
	CheckUID string
	Region   *string
	BinStart time.Time

	// Total is the countable probes in the bin (statuses excluded from
	// availability — created/running/abandoned — are dropped BEFORE binning, so
	// this is a plain row count). Up is those whose status CountsAsUp.
	Total int
	Up    int

	// DurationP95 is the NEAREST-RANK p95 over the bin's probes that carried a
	// duration, computed with the same index the aggregation job's
	// calculateRawMetrics uses for duration_p95 — deliberately, since a seam
	// point sits on the chart next to the hour rollups that will replace it.
	DurationP95 *float32
	// DurationAvg / DurationMin / DurationMax are the mean and the extremes over
	// the same set of probes.
	DurationAvg *float32
	DurationMin *float32
	DurationMax *float32

	// StatusCounts maps a status code to how many probes in the bin carried it,
	// over the same countable set as Total. The point's displayed status is
	// derived from it in Go by uptimebar.DominantStatus — the aggregation job's
	// own rule — rather than by a SQL guess at "the worst status", so a seam bin
	// and the hour rollup that replaces it classify identically.
	StatusCounts map[int]int
}

// ResponseTimeBinP95Index is the p95 nearest-rank index expressed as
// integer arithmetic: the 0-based index the aggregation job's calculateRawMetrics
// picks is `int(float64(n) * 0.95)`, and `(n * 19) / 20` is that same value for
// every n (pinned by TestResponseTimeBinP95IndexMatchesAggregationJob).
//
// The integer form is what the SQL uses, in both dialects, for two reasons:
// Postgres would evaluate `count * 0.95` in exact NUMERIC rather than in IEEE
// doubles (so "the same expression" would not be the same arithmetic), and a
// float multiplication that lands a hair below an integer silently shifts the
// chosen rank by one for exactly the sizes where n * 0.95 is a whole number.
func ResponseTimeBinP95Index(n int) int {
	if n <= 0 {
		return 0
	}

	index := (n * 19) / 20
	if index >= n {
		index = n - 1
	}

	return index
}

// DecodeStatusCounts parses the per-status probe counts the bin aggregate returns
// as a JSON object keyed by the status code (`{"3": 58, "4": 2}`). It is shared by
// both dialects: Postgres builds the object with jsonb_object_agg and SQLite with
// json_group_object, and both hand it over as text, so exactly one parser is
// needed and a drift between two copies is impossible.
//
// A nil or empty input is an empty map, not an error: a bin with no countable
// probe produces no group at all, so the only way to see one here is a LEFT JOIN
// miss, which must not fail the whole page.
func DecodeStatusCounts(raw *string) (map[int]int, error) {
	if raw == nil || *raw == "" {
		return map[int]int{}, nil
	}

	var byKey map[string]int
	if err := json.Unmarshal([]byte(*raw), &byKey); err != nil {
		return nil, fmt.Errorf("decode status counts %q: %w", *raw, err)
	}

	counts := make(map[int]int, len(byKey))

	for key, count := range byKey {
		status, err := strconv.Atoi(key)
		if err != nil {
			return nil, fmt.Errorf("decode status counts key %q: %w", key, err)
		}

		counts[status] = count
	}

	return counts, nil
}
