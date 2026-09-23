package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/uptrace/bun"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

// resultBucketOrigin is date_bin's origin: the zero time.Time, which is the
// origin Go's time.Truncate rounds down to. See
// models.ProlepticEpochOffsetSeconds for why binning against the Unix epoch
// instead would be a silent behavior change for any bucket width that does not
// divide 24 h. Postgres uses the proleptic Gregorian calendar, as Go does, so
// the two grids coincide for every width.
const resultBucketOrigin = `TIMESTAMPTZ '0001-01-01 00:00:00+00'`

// resultBucketExpr is the bucket-start expression, with the bucket width in
// seconds as its single argument. date_bin needs Postgres 14+.
const resultBucketExpr = `date_bin(make_interval(secs => ?), result.period_start, ` + resultBucketOrigin + `)`

// resultBucketRow is the aggregate's wire shape. It is a local scan struct
// rather than models.ResultBucket because `bucket_start` arrives as a timestamp
// here and as epoch seconds on SQLite, so the conversion belongs per dialect.
type resultBucketRow struct {
	CheckUID          string    `bun:"check_uid"`
	BucketStart       time.Time `bun:"bucket_start"`
	Total             int       `bun:"total"`
	Up                int       `bun:"up"`
	MaintTotal        int       `bun:"maint_total"`
	MaintUp           int       `bun:"maint_up"`
	DurCnt            int       `bun:"dur_cnt"`
	DurSum            float64   `bun:"dur_sum"`
	DurMin            *float32  `bun:"dur_min"`
	DurMax            *float32  `bun:"dur_max"`
	DurExtremaCnt     int32     `bun:"dur_extrema_cnt"`
	SlowSamples       int32     `bun:"slow_samples"`
	SlowPeaks         int32     `bun:"slow_peaks"`
	Rows              int       `bun:"row_count"`
	OldestPeriodStart time.Time `bun:"oldest_period_start"`
}

// aggregateResultBucketsQuery builds the statement AggregateResultBuckets runs,
// as a bun query so the plan test can EXPLAIN the EXACT production statement
// instead of a transcription of it (see explainListResults).
//
// Shape: one GROUP BY per (check_uid, bucket_start) over a single tier side.
// What matters about it, in order:
//
//   - It restates its side of the raw/rollup split as an explicit predicate on
//     top of the IN list, exactly as applyPeriodTypeFilter does. Both useful
//     indexes on `results` are PARTIAL on `period_type = 'raw'` / `!= 'raw'`,
//     and an index is eligible only when the WHERE implies the index's own
//     predicate. models.ResultBucketFilter.Validate rejects a straddling filter
//     outright, so there is always a side to restate.
//   - It has NO ORDER BY. That is the second half of the win: the row path
//     inherited applyResultsFilter's `ORDER BY period_start DESC, uid DESC`,
//     which a bucket fold never needed and which the planner answered with an
//     external merge sort to disk (12.8 MB measured on a 200-check page).
//   - It never touches `metrics` / `output`. A bucket is eleven numbers; the
//     two JSONB blobs were the widest part of every row shipped.
//
// The status sets come from models.CountsAsUpStatuses /
// models.ExcludedFromAvailabilityStatuses, never from literals here — the SQL
// and the Go accumulators must agree by construction, not by comment.
func aggregateResultBucketsQuery(db bun.IDB, filter *models.ResultBucketFilter) *bun.SelectQuery {
	query := db.NewSelect().
		Model((*models.Result)(nil)).
		ColumnExpr("result.check_uid AS check_uid").
		ColumnExpr(resultBucketExpr+" AS bucket_start", filter.BucketDuration.Seconds())

	if filter.TierSide() == models.PeriodTierRaw {
		query = aggregateRawBucketColumns(query)
	} else {
		query = aggregateRollupBucketColumns(query)
	}

	query = query.
		ColumnExpr("COUNT(*) AS row_count").
		ColumnExpr("MIN(result.period_start) AS oldest_period_start").
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

// aggregateRawBucketColumns mirrors uptimebar's accumulateRaw field by field.
//
// The excluded statuses are dropped in the WHERE, before the aggregate, which is
// what makes COUNT(*) the denominator. SlowPeaks is hard 0: a raw row is a
// sample, never a peak, and conflating the two is exactly what BucketStats keeps
// two counters to prevent.
func aggregateRawBucketColumns(query *bun.SelectQuery) *bun.SelectQuery {
	countsAsUp := bun.List(models.CountsAsUpStatuses())

	return query.
		ColumnExpr("COUNT(*) AS total").
		ColumnExpr("COUNT(*) FILTER (WHERE result.status IN (?)) AS up", countsAsUp).
		ColumnExpr("COUNT(*) FILTER (WHERE result.maintenance) AS maint_total").
		ColumnExpr("COUNT(*) FILTER (WHERE result.maintenance AND result.status IN (?)) AS maint_up",
			countsAsUp).
		ColumnExpr("COUNT(result.duration) AS dur_cnt").
		ColumnExpr("COALESCE(SUM(result.duration::double precision), 0) AS dur_sum").
		ColumnExpr("MIN(result.duration) AS dur_min").
		ColumnExpr("MAX(result.duration) AS dur_max").
		// foldExtrema counts one contribution per row carrying a duration, so
		// this must be COUNT(duration) and not COUNT(DISTINCT …) or COUNT(*).
		ColumnExpr("COUNT(result.duration) AS dur_extrema_cnt").
		ColumnExpr("COUNT(*) FILTER (WHERE result.duration > ?) AS slow_samples",
			models.SlowSampleThresholdMillis).
		ColumnExpr("0 AS slow_peaks").
		Where("result.status NOT IN (?)", bun.List(models.ExcludedFromAvailabilityStatuses()))
}

// aggregateRollupBucketColumns mirrors uptimebar's accumulateAgg field by field.
//
// SQL SUM ignores NULL, which IS the accumulator's "a nil counter contributes
// nothing" rule — a rollup written before maintenance tagging shipped must not
// read as a confident zero. The duration pair folds one-sidedly
// (COALESCE(min, max) / COALESCE(max, min)) for the same reason accumulateAgg
// does: a row carrying only one of the two folds it against itself rather than
// against a zero.
func aggregateRollupBucketColumns(query *bun.SelectQuery) *bun.SelectQuery {
	// The weighted-duration filter: accumulateAgg contributes only when
	// duration_avg is present AND total_checks is positive.
	const durable = "WHERE result.duration_avg IS NOT NULL AND result.total_checks > 0"

	return query.
		ColumnExpr("COALESCE(SUM(result.total_checks), 0) AS total").
		ColumnExpr("COALESCE(SUM(result.successful_checks), 0) AS up").
		ColumnExpr("COALESCE(SUM(result.maintenance_checks), 0) AS maint_total").
		ColumnExpr("COALESCE(SUM(result.maintenance_successful_checks), 0) AS maint_up").
		ColumnExpr("COALESCE(SUM(result.total_checks) FILTER ("+durable+"), 0) AS dur_cnt").
		ColumnExpr("COALESCE(SUM(result.duration_avg::double precision * result.total_checks) "+
			"FILTER ("+durable+"), 0) AS dur_sum").
		ColumnExpr("MIN(COALESCE(result.duration_min, result.duration_max)) AS dur_min").
		ColumnExpr("MAX(COALESCE(result.duration_max, result.duration_min)) AS dur_max").
		ColumnExpr("COUNT(*) FILTER (WHERE result.duration_min IS NOT NULL "+
			"OR result.duration_max IS NOT NULL) AS dur_extrema_cnt").
		ColumnExpr("0 AS slow_samples").
		ColumnExpr("COUNT(*) FILTER (WHERE result.duration_max > ?) AS slow_peaks",
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
			BucketStart:       row.BucketStart.UTC(),
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
			OldestPeriodStart: row.OldestPeriodStart.UTC(),
		})
	}

	return buckets, nil
}
