package sqlite

import (
	"context"
	"strings"

	"github.com/uptrace/bun"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

// recentResultsChunk bounds how many checks go into one statement. SQLite has
// neither unnest() nor LATERAL, so the per-check descent is spelled out as one
// bounded subquery per (check, tier) — a compound SELECT whose arm count grows
// with the batch. SQLITE_MAX_COMPOUND_SELECT defaults to 500; chunking well
// under it keeps a status page with an unusually large check group from
// compiling into a statement the engine refuses.
const recentResultsChunk = 100

// recentResultsPerCheckSQL builds the statement RecentResultsPerCheck runs for
// ONE chunk of checks, as SQL text plus its arguments, so the plan test can
// EXPLAIN QUERY PLAN the EXACT production statement rather than a
// transcription of it.
//
// Shape: one bounded subquery per (check, tier), UNION ALLed. Each arm is an
// (organization_uid, check_uid) equality lookup ordered by period_start DESC
// with its own LIMIT — the leading-column shape of results_raw_idx
// (organization_uid, check_uid, period_start desc) WHERE period_type = 'raw'
// and of results_aggregated_idx (organization_uid, check_uid, period_type,
// period_start desc) WHERE period_type != 'raw'. This mirrors the Postgres
// LATERAL form row for row (sync-pg-to-sqlite convention); SQLite simply has to
// enumerate what unnest() supplies there.
//
// Every arm restates its side of the raw/rollup split explicitly. On SQLite
// that restatement is not merely helpful, it is mandatory: SQLite does NOT
// derive a partial index's predicate from an IN list, so even
// `period_type IN ('raw')` scans the whole table without it (spec
// 2026-08-22-04). Without it this shape becomes one full scan PER CHECK.
//
// The projection omits metrics/output — the response-time chart reads neither,
// and they are the widest columns in the table (spec 2026-07-24-02 §5).
func recentResultsPerCheckSQL(
	filter *models.RecentResultsPerCheckFilter, checkUIDs []string,
) (string, []any) {
	columns := recentResultsColumns()

	arms := make([]string, 0, len(checkUIDs)*len(filter.Tiers))
	args := make([]any, 0, len(checkUIDs)*len(filter.Tiers)*4)

	for _, checkUID := range checkUIDs {
		for i := range filter.Tiers {
			tier := &filter.Tiers[i]

			arms = append(arms, `
				SELECT * FROM (
					SELECT `+columns+`
					FROM results AS res
					WHERE res.organization_uid = ?
						AND res.check_uid = ?
						AND res.period_type IN (?)
						AND res.period_type `+recentResultsTierPredicate(tier)+`
						AND res.period_start >= ?
					ORDER BY res.period_start DESC
					LIMIT ?
				)`)

			args = append(args, filter.OrganizationUID, checkUID,
				bun.List(tier.PeriodTypes), tier.Since, filter.LimitFor(checkUID))
		}
	}

	return strings.Join(arms, "\n\t\t\tUNION ALL\n"), args
}

// recentResultsColumns is the projection, each column explicitly aliased to its
// bare name. The alias is load-bearing: the arms are wrapped in
// `SELECT * FROM (...)` so each can carry its own ORDER BY/LIMIT, and bun scans
// the outer result set by column name.
func recentResultsColumns() string {
	qualified := models.ResultColumnsWithoutBlobs("res")
	bare := models.ResultColumnsWithoutBlobs("")

	aliased := make([]string, len(qualified))
	for i := range qualified {
		aliased[i] = qualified[i] + " AS " + bare[i]
	}

	return strings.Join(aliased, ", ")
}

// recentResultsTierPredicate is the restated side of the raw/rollup index split
// for one arm, as the operator+literal following `res.period_type`. Validate
// has already ruled out models.PeriodTierMixed; the default is there so a new
// tier side can never silently produce an unindexable arm.
func recentResultsTierPredicate(tier *models.RecentResultsTier) string {
	if models.PeriodTypesTierSide(tier.PeriodTypes) == models.PeriodTierRaw {
		return "= '" + models.PeriodTypeRaw + "'"
	}

	return "!= '" + models.PeriodTypeRaw + "'"
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

	var all []*models.Result

	for start := 0; start < len(filter.CheckUIDs); start += recentResultsChunk {
		end := min(start+recentResultsChunk, len(filter.CheckUIDs))

		query, args := recentResultsPerCheckSQL(filter, filter.CheckUIDs[start:end])

		var chunk []*models.Result

		if err := s.db.NewRaw(query, args...).Scan(ctx, &chunk); err != nil {
			return nil, err
		}

		all = append(all, chunk...)
	}

	return all, nil
}
