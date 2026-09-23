package sqlite

import (
	"context"
	"fmt"
	"time"

	"github.com/uptrace/bun"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

// responseTimeBinRow is the seam aggregate's wire shape. `bin_start` arrives as
// epoch seconds: `period_start` is ISO TEXT in SQLite, so the bin expression
// reduces through unixepoch() rather than relying on lexicographic ordering of
// timestamp strings (see resultBucketExpr).
type responseTimeBinRow struct {
	CheckUID     string   `bun:"check_uid"`
	Region       *string  `bun:"region"`
	BinStart     int64    `bun:"bin_start"`
	Total        int      `bun:"total"`
	Up           int      `bun:"up"`
	DurationP95  *float32 `bun:"dur_p95"`
	DurationAvg  *float32 `bun:"dur_avg"`
	DurationMin  *float32 `bun:"dur_min"`
	DurationMax  *float32 `bun:"dur_max"`
	StatusCounts *string  `bun:"status_counts"`
}

// responseTimeBinsSQL builds the statement AggregateResponseTimeBins runs, as SQL
// text plus its arguments, so the plan test can EXPLAIN QUERY PLAN the EXACT
// production statement.
//
// It mirrors the Postgres form CTE for CTE (sync-pg-to-sqlite convention). Four
// dialect differences, all mechanical:
//
//   - the bin expression (resultBucketExpr, epoch seconds, two bound widths);
//   - SUM(CASE WHEN … THEN 1 ELSE 0 END) in place of COUNT(*) FILTER (…);
//   - json_group_object in place of jsonb_object_agg, with the status CAST to
//     TEXT because json_group_object's first argument is an object key;
//   - `IS` in place of `IS NOT DISTINCT FROM` — SQLite's NULL-safe equality, which
//     the NULL/legacy region group needs since region is a GROUP BY key here.
//
// The restated `period_type = 'raw'` is not optional. Postgres derives a partial
// index's predicate from an IN list on its own; SQLite does NOT, so even
// `period_type IN ('raw')` scans the whole table without the explicit equality
// beside it (spec 2026-08-22-04).
//
// The p95 is nearest-rank by ROW_NUMBER, for the reason spelled out in the
// Postgres twin: it must equal the index the aggregation job's calculateRawMetrics
// picks, `models.ResponseTimeBinP95Index`, or a seam point and the hour rollup that
// replaces it would disagree on the same probes.
func responseTimeBinsSQL(filter *models.ResponseTimeBinFilter) (string, []any) {
	const rawPredicate = "result.period_type = '" + models.PeriodTypeRaw + "'"

	// (cnt * 19) / 20 + 1 is models.ResponseTimeBinP95Index's 0-based index as a
	// 1-based ROW_NUMBER. Both operands are integers, so `/` is SQLite's integer
	// division; never float.
	const p95Rank = "ranked.rn = (ranked.cnt * 19) / 20 + 1"

	const partition = "PARTITION BY check_uid, region, bin"

	binSeconds := int64(filter.BinDuration / time.Second)

	query := `
		WITH probes AS (
			SELECT result.check_uid AS check_uid,
			       result.region AS region,
			       ` + resultBucketExpr() + ` AS bin,
			       result.status AS status,
			       result.duration AS duration
			FROM results AS result
			WHERE result.organization_uid = ?
			  AND result.check_uid IN (?)
			  AND ` + rawPredicate + `
			  AND result.period_start >= ?
			  AND result.status IS NOT NULL
			  AND result.status NOT IN (?)
		),
		agg AS (
			SELECT check_uid, region, bin,
			       COUNT(*) AS total,
			       SUM(CASE WHEN status IN (?) THEN 1 ELSE 0 END) AS up,
			       CAST(AVG(duration) AS REAL) AS dur_avg,
			       MIN(duration) AS dur_min,
			       MAX(duration) AS dur_max
			FROM probes
			GROUP BY 1, 2, 3
		),
		ranked AS (
			SELECT check_uid, region, bin, duration,
			       ROW_NUMBER() OVER (` + partition + ` ORDER BY duration) AS rn,
			       COUNT(*) OVER (` + partition + `) AS cnt
			FROM probes
			WHERE duration IS NOT NULL
		),
		p95 AS (
			SELECT check_uid, region, bin, duration AS dur_p95
			FROM ranked
			WHERE ` + p95Rank + `
		),
		mix AS (
			SELECT check_uid, region, bin, json_group_object(CAST(status AS TEXT), n) AS status_counts
			FROM (
				SELECT check_uid, region, bin, status, COUNT(*) AS n
				FROM probes
				GROUP BY 1, 2, 3, 4
			) AS per_status
			GROUP BY 1, 2, 3
		)
		SELECT agg.check_uid, agg.region, agg.bin AS bin_start, agg.total, agg.up,
		       p95.dur_p95, agg.dur_avg, agg.dur_min, agg.dur_max, mix.status_counts
		FROM agg
		LEFT JOIN p95 ON p95.check_uid = agg.check_uid
		             AND p95.bin = agg.bin
		             AND p95.region IS agg.region
		LEFT JOIN mix ON mix.check_uid = agg.check_uid
		             AND mix.bin = agg.bin
		             AND mix.region IS agg.region`

	args := []any{
		binSeconds, binSeconds,
		filter.OrganizationUID,
		bun.List(filter.CheckUIDs),
		filter.Since,
		bun.List(models.ExcludedFromAvailabilityStatuses()),
		bun.List(models.CountsAsUpStatuses()),
	}

	return query, args
}

// AggregateResponseTimeBins bins raw probes per (check, region, bin) for the
// status page's response-time seam. See the db.Service interface for the contract
// and responseTimeBinsSQL for why the statement is shaped the way it is.
func (s *Service) AggregateResponseTimeBins(
	ctx context.Context, filter *models.ResponseTimeBinFilter,
) ([]models.ResponseTimeBin, error) {
	if len(filter.CheckUIDs) == 0 {
		return nil, nil
	}

	if err := filter.Validate(); err != nil {
		return nil, err
	}

	var rows []responseTimeBinRow

	query, args := responseTimeBinsSQL(filter)

	if err := s.db.NewRaw(query, args...).Scan(ctx, &rows); err != nil {
		return nil, fmt.Errorf("aggregate response time bins: %w", err)
	}

	bins := make([]models.ResponseTimeBin, 0, len(rows))

	for i := range rows {
		row := &rows[i]

		counts, err := models.DecodeStatusCounts(row.StatusCounts)
		if err != nil {
			return nil, fmt.Errorf("aggregate response time bins: %w", err)
		}

		bins = append(bins, models.ResponseTimeBin{
			CheckUID:     row.CheckUID,
			Region:       row.Region,
			BinStart:     time.Unix(row.BinStart, 0).UTC(),
			Total:        row.Total,
			Up:           row.Up,
			DurationP95:  row.DurationP95,
			DurationAvg:  row.DurationAvg,
			DurationMin:  row.DurationMin,
			DurationMax:  row.DurationMax,
			StatusCounts: counts,
		})
	}

	return bins, nil
}
