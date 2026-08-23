package postgres

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect/pgdialect"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

// Port distinct from every other _postgres_test.go file's embedded-Postgres
// port in this repo (see the port-numbering note in
// postgres_headroom_postgres_test.go).
const portRecentResultsPlan = 15494

// seedRecentResultsChecks creates `count` checks, each with a day of raw at a
// 10 s cadence plus 45 days of hour+day rollups — production retention shape.
// The checks NOT on the page under test are the noise that makes an index scan
// the planner's genuine choice rather than an artefact of a one-check table.
func seedRecentResultsChecks(
	ctx context.Context, t *testing.T, s *Service, orgUID, prefix string, count int,
) []string {
	t.Helper()

	uids := make([]string, 0, count)

	for i := range count {
		check := models.NewCheck(orgUID, fmt.Sprintf("%s-%d", prefix, i), "http")
		require.NoError(t, s.CreateCheck(ctx, check))
		seedPlanRaw(ctx, t, s, orgUID, check.UID, 7_200)
		seedPlanRollups(ctx, t, s, orgUID, check.UID, 45)
		uids = append(uids, check.UID)
	}

	return uids
}

// recentResultsPageFilter is the filter statuspages.fetchRecentResults builds:
// a raw branch clamped to raw retention plus a rollup branch covering every
// aggregated tier, with a per-check budget.
func recentResultsPageFilter(orgUID string, checkUIDs []string) *models.RecentResultsPerCheckFilter {
	now := time.Now().UTC()

	return &models.RecentResultsPerCheckFilter{
		OrganizationUID: orgUID,
		CheckUIDs:       checkUIDs,
		Tiers: []models.RecentResultsTier{
			{PeriodTypes: []string{models.PeriodTypeRaw}, Since: now.Add(-26 * time.Hour)},
			{
				PeriodTypes: []string{models.PeriodTypeHour, models.PeriodTypeDay, models.PeriodTypeMonth},
				Since:       now.AddDate(0, 0, -200),
			},
		},
		DefaultPerCheckLimit: 300,
	}
}

// TestRecentResultsPerCheckUsesIndexes_Postgres is the plan regression for spec
// 2026-08-22-05. The status page's response-time fetch used to send ONE query
// with no period-type filter and no time bound, so neither partial index on
// `results` was eligible: a parallel sequential scan of the whole table plus an
// external merge sort to disk, 662 ms on every public, unauthenticated page
// view, reading ~40 000 rows to keep ~6 000.
//
// The replacement is a per-check LATERAL with one tier-aligned branch per side
// of the raw/rollup split. The tier predicate is not a refinement of that
// rewrite, it IS the rewrite: the same LATERAL with the tier left open measures
// 12 274 ms — a sequential scan PER CHECK, 18x WORSE than the query it
// replaces, and invisible to every functional test. Hence the controls below.
//
//nolint:paralleltest // embedded-postgres tests run sequentially in this package
func TestRecentResultsPerCheckUsesIndexes_Postgres(t *testing.T) {
	ctx := t.Context()
	r := require.New(t)

	s := newTier1ServicePG(t, portRecentResultsPlan)

	org := models.NewOrganization("recent-plan-org", "Recent Plan Org")
	r.NoError(s.CreateOrganization(ctx, org))

	pageChecks := seedRecentResultsChecks(ctx, t, s, org.UID, "recent-plan-page", 20)
	seedRecentResultsChecks(ctx, t, s, org.UID, "recent-plan-noise", 10)

	_, err := s.db.ExecContext(ctx, "ANALYZE results")
	r.NoError(err)

	// A 1-check page and a 20-check page: the spec's two plan cases.
	for _, size := range []int{1, 20} {
		filter := recentResultsPageFilter(org.UID, pageChecks[:size])
		plan := explainRecentResults(ctx, t, s, filter)

		r.NotContains(plan, "Seq Scan on results",
			"a %d-check page must not sequentially scan results — that is the whole defect "+
				"(plan:\n%s)", size, plan)
		r.NotContains(plan, "Parallel Seq Scan on results",
			"a %d-check page must not parallel-seq-scan results either (plan:\n%s)", size, plan)
		r.Contains(plan, "results_raw_idx",
			"the raw branch must ride results_raw_idx (%d checks, plan:\n%s)", size, plan)
		r.Contains(plan, "results_aggregated_idx",
			"the rollup branch must ride results_aggregated_idx (%d checks, plan:\n%s)", size, plan)

		// The bound the index actually seeks on, as opposed to a filter applied
		// to rows already read: check_uid comes from the LATERAL's driving row,
		// which is what makes the cost per-check instead of per-table.
		r.Contains(indexCondLine(plan), "check_uid",
			"the per-check descent must bound the index itself (%d checks, plan:\n%s)", size, plan)

		// And the fetch really is bounded: the whole point is that ~6 000 rows
		// reach Go instead of 40 000. Two branches x 300 x size is the ceiling.
		r.LessOrEqual(planActualRows(plan), 2*300*size,
			"the per-check budget must bound what the query returns (%d checks, plan:\n%s)", size, plan)
	}

	// Positive control 1: the SAME per-check LATERAL with NO tier predicate —
	// literally "just add the LATERAL". It must seq-scan, per check.
	//
	// This is the control that gives the assertions above their teeth, and it
	// was verified by reverting: deleting the period_type predicate from
	// recentResultsPerCheckSQL makes the 1-check assertion fail with
	// "Seq Scan on results ... Rows Removed by Filter: 242554" per branch.
	//
	// Note which half of the predicate Postgres needs. It DOES derive
	// results_raw_idx's own `period_type = 'raw'` from `period_type IN
	// ('raw')`, so removing only the restated side leaves this plan unchanged;
	// the restatement is there for SQLite, which does not (spec 2026-08-22-04,
	// pinned by TestRecentResultsPerCheckRestatedTierPredicateIsLoadBearing in
	// the sqlite package). What Postgres cannot survive is having NO tier
	// predicate at all — which is exactly what this control runs.
	controlPlan := explainSQL(ctx, t, s, tierlessRecentResultsSQL(s, org.UID, pageChecks))
	r.Contains(controlPlan, "Seq Scan on results",
		"the tier-less per-check shape MUST still seq-scan — otherwise the assertions above "+
			"prove nothing about this fixture (plan:\n%s)", controlPlan)

	// Positive control 2: the pre-fix query itself — no period type, no time
	// bound, one global limit. This is what shipped, and it must seq-scan too.
	prefixPlan := explainSQL(ctx, t, s, prefixRecentResultsSQL(s, org.UID, pageChecks))
	r.Contains(prefixPlan, "Seq Scan on results",
		"the pre-fix global-limit query MUST seq-scan on this fixture (plan:\n%s)", prefixPlan)
}

// explainRecentResults EXPLAINs the exact statement RecentResultsPerCheck
// executes for this filter — built by the production builder with the
// production arguments, then rendered to literal SQL by the same dialect, so it
// cannot drift from production by transcription.
func explainRecentResults(
	ctx context.Context, t *testing.T, s *Service, filter *models.RecentResultsPerCheckFilter,
) string {
	t.Helper()

	require.NoError(t, filter.Validate())

	query, args := recentResultsPerCheckSQL(filter)

	return explainSQL(ctx, t, s, s.db.NewRaw(query, args...).String())
}

// tierlessRecentResultsSQL is the forbidden variant: the identical per-check
// LATERAL, time-bounded, with NO period_type predicate. Production code cannot
// express it — models.RecentResultsPerCheckFilter.Validate rejects a mixed tier
// — so it lives here, as the control that gives the plan assertions their teeth.
func tierlessRecentResultsSQL(s *Service, orgUID string, checkUIDs []string) string {
	limits := make([]int, len(checkUIDs))
	for i := range limits {
		limits[i] = 300
	}

	query := `
		SELECT r.*
		FROM unnest(?::uuid[], ?::int[]) AS cu(uid, lim)
		CROSS JOIN LATERAL (
			SELECT ` + strings.Join(models.ResultColumnsWithoutBlobs("res"), ", ") + `
			FROM results AS res
			WHERE res.organization_uid = ?
				AND res.check_uid = cu.uid
				AND res.period_start >= ?
			ORDER BY res.period_start DESC
			LIMIT cu.lim
		) AS r`

	return s.db.NewRaw(query,
		pgdialect.Array(checkUIDs), pgdialect.Array(limits),
		orgUID, time.Now().UTC().AddDate(0, 0, -200),
	).String()
}

// prefixRecentResultsSQL reproduces what fetchRecentResults sent before this
// spec: every requested check, any period type, no time bound, one global row
// limit of responseTimeLimit x regionFanoutCap x len(checks).
func prefixRecentResultsSQL(s *Service, orgUID string, checkUIDs []string) string {
	var rows []*models.Result

	return s.db.NewSelect().Model(&rows).
		ExcludeColumn("metrics", "output").
		Where("result.organization_uid = ?", orgUID).
		Where("result.check_uid IN (?)", bun.List(checkUIDs)).
		Order("result.period_start DESC").
		Order("result.uid DESC").
		Limit(100 * 20 * len(checkUIDs)).
		String()
}

// planActualRows totals the rows every node of the plan actually emitted at the
// top level — the "rows=N" of the outermost node, which for this query is what
// the DB hands back to Go.
func planActualRows(plan string) int {
	const marker = "actual time="

	for _, line := range strings.Split(plan, "\n") {
		idx := strings.Index(line, marker)
		if idx < 0 {
			continue
		}

		rest := line[idx:]

		rowsIdx := strings.Index(rest, "rows=")
		if rowsIdx < 0 {
			continue
		}

		var rows int
		if _, err := fmt.Sscanf(rest[rowsIdx+len("rows="):], "%d", &rows); err == nil {
			return rows
		}
	}

	return 0
}
