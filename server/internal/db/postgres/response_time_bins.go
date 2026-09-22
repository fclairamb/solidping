package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/uptrace/bun"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

// responseTimeBinRow is the seam aggregate's wire shape. Like resultBucketRow it
// is a local scan struct rather than models.ResponseTimeBin, because `bin_start`
// arrives as a timestamp here and as epoch seconds on SQLite.
type responseTimeBinRow struct {
	CheckUID     string    `bun:"check_uid"`
	Region       *string   `bun:"region"`
	BinStart     time.Time `bun:"bin_start"`
	Total        int       `bun:"total"`
	Up           int       `bun:"up"`
	DurationP95  *float32  `bun:"dur_p95"`
	DurationAvg  *float32  `bun:"dur_avg"`
	DurationMin  *float32  `bun:"dur_min"`
	DurationMax  *float32  `bun:"dur_max"`
	StatusCounts *string   `bun:"status_counts"`
}

// responseTimeBinsSQL builds the statement AggregateResponseTimeBins runs, as SQL
// text plus its arguments, so the plan test can EXPLAIN the EXACT production
// statement instead of a transcription of it (the recentResultsPerCheckSQL
// pattern).
//
// Shape: ONE statement, four CTEs, no LATERAL. There is no per-check branch to
// bound because the output is bounded by construction — checks x regions x bins,
// ~96 bins at the finest width this caller ever asks for — where the row fetch it
// replaces was bounded only by how many probes the window held (292 843 rows on
// the measured 200-check page).
//
// What matters about it, in order:
//
//   - `probes` restates `period_type = 'raw'` as an explicit equality. Both useful
//     indexes on `results` are PARTIAL and split on exactly that predicate, and an
//     index is eligible only when the WHERE implies the index's own predicate. It
//     also carries the org + check IN list + `period_start >=` bound, which is the
//     leading-column shape of results_raw_idx.
//   - The excluded statuses (created/running/abandoned) are dropped THERE, before
//     any aggregate, which is what makes COUNT(*) the availability denominator —
//     the same placement aggregateRawBucketColumns uses, and the same rule
//     uptimebar's accumulateRaw applies row by row.
//   - The bin is spec 2026-09-22-05's resultBucketExpr, reused rather than
//     rewritten: it bins against the 0001-01-01 origin Go's time.Truncate rounds
//     down to (see models.ProlepticEpochOffsetSeconds), so the seam's grid is the
//     same grid the availability aggregate and the Go fold use.
//   - `region` is a GROUP BY key here, unlike in the availability aggregate: each
//     region is its own series on the response-time chart. Window PARTITION BY and
//     the joins therefore all have to be NULL-safe, since the legacy/NULL region is
//     a real group — hence `IS NOT DISTINCT FROM` rather than `=`.
//   - There is NO ORDER BY. The caller maps the bins by (check, region); the row
//     path this replaces inherited an ORDER BY it never needed.
//
// The p95 is NEAREST-RANK, picked by ROW_NUMBER, and not percentile_cont. This is
// the single most load-bearing line in the file: percentile_cont INTERPOLATES
// between the two neighbouring samples, while the aggregation job's
// calculateRawMetrics sorts and takes the sample at index
// `int(float64(n) * 0.95)`. A seam point sits on the chart immediately next to the
// hour rollups that will replace it as raw is compacted away, so an interpolating
// seam would make the chart step every time the aggregation job ran.
// models.ResponseTimeBinP95Index is the same index in integer arithmetic, and the
// rank below is its 1-based form.
func responseTimeBinsSQL(filter *models.ResponseTimeBinFilter) (string, []any) {
	const rawPredicate = "result.period_type = '" + models.PeriodTypeRaw + "'"

	// (cnt * 19) / 20 + 1 is models.ResponseTimeBinP95Index's 0-based index as a
	// 1-based ROW_NUMBER. Integer division in both engines; never float.
	const p95Rank = "ranked.rn = (ranked.cnt * 19) / 20 + 1"

	const partition = "PARTITION BY check_uid, region, bin"

	query := `
		WITH probes AS (
			SELECT result.check_uid AS check_uid,
			       result.region AS region,
			       ` + resultBucketExpr + ` AS bin,
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
			       COUNT(*) FILTER (WHERE status IN (?)) AS up,
			       CAST(AVG(duration::double precision) AS real) AS dur_avg,
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
			SELECT check_uid, region, bin, jsonb_object_agg(status::text, n)::text AS status_counts
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
		             AND p95.region IS NOT DISTINCT FROM agg.region
		LEFT JOIN mix ON mix.check_uid = agg.check_uid
		             AND mix.bin = agg.bin
		             AND mix.region IS NOT DISTINCT FROM agg.region`

	args := []any{
		filter.BinDuration.Seconds(),
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
			BinStart:     row.BinStart.UTC(),
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
