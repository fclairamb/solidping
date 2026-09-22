package sqlite

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/uptrace/bun"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

// resultBucketOffset is models.ProlepticEpochOffsetSeconds as a SQL literal:
// the seconds from 0001-01-01 00:00:00 UTC (the origin Go's time.Truncate
// rounds down to) to the Unix epoch.
//
// SQLite has no date_bin, so the bucket grid is built in epoch seconds:
// shift onto the proleptic origin, floor by integer division, shift back. Using
// the Unix epoch as the origin instead — the obvious thing — silently disagrees
// with time.Truncate for every bucket width that does not divide 24 h, and the
// availability API accepts such widths (7 h, 90 min).
var resultBucketOffset = strconv.FormatInt(models.ProlepticEpochOffsetSeconds, 10)

// resultBucketExpr is the bucket-start expression, as EPOCH SECONDS, taking the
// bucket width in seconds twice (divide, then multiply back). The caller
// converts to a time.Time in Go, because `period_start` is ISO text here and
// re-rendering the bin as text only to re-parse it in Go would add a format
// round trip for nothing.
//
// Both `?`s are integers and unixepoch() returns an integer, so `/` is SQLite's
// integer division. The shifted value is positive for every storable timestamp,
// so truncation toward zero IS a floor.
//
// unixepoch() needs SQLite >= 3.38; both bundled drivers ship newer engines
// (TestResultBucketExprMatchesGoTruncate would fail loudly on an older one).
var resultBucketExpr = "(((unixepoch(result.period_start) + " + resultBucketOffset +
	") / ?) * ?) - " + resultBucketOffset

// resultBucketRow is the aggregate's wire shape. `bucket_start` and
// `oldest_period_start` arrive as epoch seconds: `period_start` is ISO TEXT in
// SQLite, so the aggregate reduces both through unixepoch() rather than relying
// on lexicographic ordering of timestamp strings.
type resultBucketRow struct {
	CheckUID          string   `bun:"check_uid"`
	BucketStart       int64    `bun:"bucket_start"`
	Total             int      `bun:"total"`
	Up                int      `bun:"up"`
	MaintTotal        int      `bun:"maint_total"`
	MaintUp           int      `bun:"maint_up"`
	DurCnt            int      `bun:"dur_cnt"`
	DurSum            float64  `bun:"dur_sum"`
	DurMin            *float32 `bun:"dur_min"`
	DurMax            *float32 `bun:"dur_max"`
	DurExtremaCnt     int32    `bun:"dur_extrema_cnt"`
	SlowSamples       int32    `bun:"slow_samples"`
	SlowPeaks         int32    `bun:"slow_peaks"`
	Rows              int      `bun:"row_count"`
	OldestPeriodStart int64    `bun:"oldest_period_start"`
}

// aggregateResultBucketsQuery builds the statement AggregateResultBuckets runs,
// as a bun query so the plan test can EXPLAIN QUERY PLAN the EXACT production
// statement instead of a transcription of it.
//
// It mirrors the Postgres form row for row (sync-pg-to-sqlite convention). Two
// dialect differences, both mechanical: the bucket expression above, and
// SUM(CASE WHEN … THEN 1 ELSE 0 END) in place of COUNT(*) FILTER (…).
//
// The restated tier side is not optional here. Postgres derives a partial
// index's predicate from an IN list on its own; SQLite does NOT, so even
// `period_type IN ('raw')` scans the whole table without the explicit
// `period_type = 'raw'` beside it (spec 2026-08-22-04).
//
// There is deliberately no ORDER BY: a bucket fold never needed one, and the
// row path it replaces inherited applyResultsFilter's ordering and paid for a
// sort of everything it read.
func aggregateResultBucketsQuery(db bun.IDB, filter *models.ResultBucketFilter) *bun.SelectQuery {
	bucketSeconds := int64(filter.BucketDuration / time.Second)

	query := db.NewSelect().
		Model((*models.Result)(nil)).
		ColumnExpr("result.check_uid AS check_uid").
		ColumnExpr(resultBucketExpr+" AS bucket_start", bucketSeconds, bucketSeconds)

	if filter.TierSide() == models.PeriodTierRaw {
		query = aggregateRawBucketColumns(query)
	} else {
		query = aggregateRollupBucketColumns(query)
	}

	query = query.
		ColumnExpr("COUNT(*) AS row_count").
		ColumnExpr("MIN(unixepoch(result.period_start)) AS oldest_period_start").
		Where("result.organization_uid = ?", filter.OrganizationUID).
		Where("result.check_uid IN (?)", bun.List(filter.CheckUIDs)).
		Where("result.period_type IN (?)", bun.List(filter.PeriodTypes)).
		Where("result.period_start >= ?", filter.PeriodStartAfter)

	// The restated tier side. Validate has ruled PeriodTierMixed out.
	if filter.TierSide() == models.PeriodTierRaw {
		query = query.Where("result.period_type = ?", models.PeriodTypeRaw)
	} else {
		query = query.Where("result.period_type != ?", models.PeriodTypeRaw)
	}

	if filter.PeriodStartBefore != nil {
		query = query.Where("result.period_start < ?", *filter.PeriodStartBefore)
	}

	if len(filter.Regions) > 0 {
		query = query.Where("result.region IN (?)", bun.List(filter.Regions))
	}

	return query.GroupExpr("1, 2")
}

// aggregateRawBucketColumns mirrors uptimebar's accumulateRaw field by field
// (and postgres.aggregateRawBucketColumns statement by statement).
func aggregateRawBucketColumns(query *bun.SelectQuery) *bun.SelectQuery {
	countsAsUp := bun.List(models.CountsAsUpStatuses())

	return query.
		ColumnExpr("COUNT(*) AS total").
		ColumnExpr("SUM(CASE WHEN result.status IN (?) THEN 1 ELSE 0 END) AS up", countsAsUp).
		ColumnExpr("SUM(CASE WHEN result.maintenance THEN 1 ELSE 0 END) AS maint_total").
		ColumnExpr("SUM(CASE WHEN result.maintenance AND result.status IN (?) THEN 1 ELSE 0 END) AS maint_up",
			countsAsUp).
		ColumnExpr("COUNT(result.duration) AS dur_cnt").
		// CAST to REAL: SQLite types a value, not a column, so COALESCE(NULL, 0)
		// on an empty sum yields an INTEGER the float64 scan refuses.
		ColumnExpr("CAST(COALESCE(SUM(result.duration), 0) AS REAL) AS dur_sum").
		ColumnExpr("MIN(result.duration) AS dur_min").
		ColumnExpr("MAX(result.duration) AS dur_max").
		// foldExtrema counts one contribution per row carrying a duration.
		ColumnExpr("COUNT(result.duration) AS dur_extrema_cnt").
		ColumnExpr("SUM(CASE WHEN result.duration > ? THEN 1 ELSE 0 END) AS slow_samples",
			models.SlowSampleThresholdMillis).
		ColumnExpr("0 AS slow_peaks").
		Where("result.status NOT IN (?)", bun.List(models.ExcludedFromAvailabilityStatuses()))
}

// aggregateRollupBucketColumns mirrors uptimebar's accumulateAgg field by field
// (and postgres.aggregateRollupBucketColumns statement by statement).
func aggregateRollupBucketColumns(query *bun.SelectQuery) *bun.SelectQuery {
	// accumulateAgg contributes a weighted duration only when duration_avg is
	// present AND total_checks is positive.
	const durable = "result.duration_avg IS NOT NULL AND result.total_checks > 0"

	return query.
		ColumnExpr("COALESCE(SUM(result.total_checks), 0) AS total").
		ColumnExpr("COALESCE(SUM(result.successful_checks), 0) AS up").
		ColumnExpr("COALESCE(SUM(result.maintenance_checks), 0) AS maint_total").
		ColumnExpr("COALESCE(SUM(result.maintenance_successful_checks), 0) AS maint_up").
		ColumnExpr("COALESCE(SUM(CASE WHEN "+durable+
			" THEN result.total_checks ELSE 0 END), 0) AS dur_cnt").
		// CAST to REAL for the same reason as the raw tier's dur_sum.
		ColumnExpr("CAST(COALESCE(SUM(CASE WHEN "+durable+
			" THEN result.duration_avg * result.total_checks ELSE 0 END), 0) AS REAL) AS dur_sum").
		ColumnExpr("MIN(COALESCE(result.duration_min, result.duration_max)) AS dur_min").
		ColumnExpr("MAX(COALESCE(result.duration_max, result.duration_min)) AS dur_max").
		ColumnExpr("SUM(CASE WHEN result.duration_min IS NOT NULL "+
			"OR result.duration_max IS NOT NULL THEN 1 ELSE 0 END) AS dur_extrema_cnt").
		ColumnExpr("0 AS slow_samples").
		ColumnExpr("SUM(CASE WHEN result.duration_max > ? THEN 1 ELSE 0 END) AS slow_peaks",
			models.SlowSampleThresholdMillis)
}

// AggregateResultBuckets folds result rows into per-(check, bucket) counters
// server-side. See the db.Service interface for the contract and
// aggregateResultBucketsQuery for why the statement is shaped the way it is.
func (s *Service) AggregateResultBuckets(
	ctx context.Context, filter *models.ResultBucketFilter,
) ([]models.ResultBucket, error) {
	if len(filter.CheckUIDs) == 0 {
		return nil, nil
	}

	if err := filter.Validate(); err != nil {
		return nil, err
	}

	var rows []resultBucketRow

	if err := aggregateResultBucketsQuery(s.db, filter).Scan(ctx, &rows); err != nil {
		return nil, fmt.Errorf("aggregate result buckets: %w", err)
	}

	buckets := make([]models.ResultBucket, 0, len(rows))

	for i := range rows {
		row := &rows[i]

		buckets = append(buckets, models.ResultBucket{
			CheckUID:          row.CheckUID,
			BucketStart:       time.Unix(row.BucketStart, 0).UTC(),
			Total:             row.Total,
			Up:                row.Up,
			MaintTotal:        row.MaintTotal,
			MaintUp:           row.MaintUp,
			DurCnt:            row.DurCnt,
			DurSum:            row.DurSum,
			DurMin:            row.DurMin,
			DurMax:            row.DurMax,
			DurExtremaCnt:     row.DurExtremaCnt,
			SlowSamples:       row.SlowSamples,
			SlowPeaks:         row.SlowPeaks,
			Rows:              row.Rows,
			OldestPeriodStart: time.Unix(row.OldestPeriodStart, 0).UTC(),
		})
	}

	return buckets, nil
}
