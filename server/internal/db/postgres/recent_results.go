package postgres

import (
	"context"
	"strings"

	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect/pgdialect"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

// recentResultsPerCheckSQL builds the statement RecentResultsPerCheck runs, as
// SQL text plus its arguments, so the plan test can EXPLAIN the EXACT
// production statement instead of a transcription of it.
//
// Shape: the driving side unnests the requested check uids alongside their
// per-check row budgets; the LATERAL side is one bounded branch per tier,
// UNION ALLed. Each branch is an (organization_uid, check_uid) equality lookup
// ordered by period_start DESC with its own LIMIT — exactly the leading-column
// shape of results_raw_idx (organization_uid, check_uid, period_start desc)
// WHERE period_type = 'raw' and of results_aggregated_idx (organization_uid,
// check_uid, period_type, period_start desc) WHERE period_type != 'raw'. The
// index supplies both the filter and the ordering, and the LIMIT stops the
// descent, so the cost is per-check and bounded rather than proportional to the
// table.
//
// Every branch restates its side of the raw/rollup split as an explicit
// predicate ON TOP of the IN list. That restatement is what makes the partial
// index eligible; without it this shape is not a smaller scan but a LARGER one
// — one sequential scan of `results` per check (spec 2026-08-22-05: 12 274 ms
// for a 20-check page, against 662 ms for the query it replaces).
// models.RecentResultsPerCheckFilter.Validate rejects a mixed tier outright, so
// the "no side to restate" case cannot reach here.
//
// The projection deliberately omits metrics/output: this method exists for the
// status page's response-time chart, which plots duration/status only, and
// those two JSONB columns are by far the widest part of a results row (spec
// 2026-07-24-02 §5).
func recentResultsPerCheckSQL(filter *models.RecentResultsPerCheckFilter) (string, []any) {
	columns := strings.Join(models.ResultColumnsWithoutBlobs("res"), ", ")

	args := []any{
		pgdialect.Array(filter.CheckUIDs),
		pgdialect.Array(recentResultsLimits(filter)),
	}

	branches := make([]string, 0, len(filter.Tiers))

	for i := range filter.Tiers {
		tier := &filter.Tiers[i]

		branches = append(branches, `
			(SELECT `+columns+`
			 FROM results AS res
			 WHERE res.organization_uid = ?
				AND res.check_uid = cu.uid
				AND res.period_type IN (?)
				AND res.period_type `+recentResultsTierPredicate(tier)+`
				AND res.period_start >= ?
			 ORDER BY res.period_start DESC
			 LIMIT cu.lim)`)

		args = append(args, filter.OrganizationUID, bun.List(tier.PeriodTypes), tier.Since)
	}

	query := `
		SELECT r.*
		FROM unnest(?::uuid[], ?::int[]) AS cu(uid, lim)
		CROSS JOIN LATERAL (` + strings.Join(branches, "\n\t\t\tUNION ALL\n") + `
		) AS r`

	return query, args
}

// recentResultsTierPredicate is the restated side of the raw/rollup index split
// for one branch, as the operator+literal following `res.period_type`. Validate
// has already ruled out models.PeriodTierMixed; the default is there so a new
// tier side can never silently produce an unindexable branch.
func recentResultsTierPredicate(tier *models.RecentResultsTier) string {
	if models.PeriodTypesTierSide(tier.PeriodTypes) == models.PeriodTierRaw {
		return "= '" + models.PeriodTypeRaw + "'"
	}

	return "!= '" + models.PeriodTypeRaw + "'"
}

// recentResultsLimits materializes the per-check budgets in the same order as
// filter.CheckUIDs, so unnest() pairs each uid with its own LIMIT.
func recentResultsLimits(filter *models.RecentResultsPerCheckFilter) []int {
	limits := make([]int, len(filter.CheckUIDs))
	for i, checkUID := range filter.CheckUIDs {
		limits[i] = filter.LimitFor(checkUID)
	}

	return limits
}

// RecentResultsPerCheck returns the newest rows per check per tier. See the
// db.Service interface for the contract and recentResultsPerCheckSQL for why
// the query is shaped the way it is.
func (s *Service) RecentResultsPerCheck(
	ctx context.Context, filter *models.RecentResultsPerCheckFilter,
) ([]*models.Result, error) {
	if len(filter.CheckUIDs) == 0 {
		return nil, nil
	}

	if err := filter.Validate(); err != nil {
		return nil, err
	}

	var results []*models.Result

	query, args := recentResultsPerCheckSQL(filter)

	if err := s.db.NewRaw(query, args...).Scan(ctx, &results); err != nil {
		return nil, err
	}

	return results, nil
}
