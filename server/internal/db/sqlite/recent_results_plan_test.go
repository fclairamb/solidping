package sqlite

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/uptrace/bun"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

// seedRecentResultsFixture builds an org with `checks` checks, each carrying
// raw rows at a 1-minute cadence plus hourly and daily rollups, in two regions.
// Enough shape and enough noise that EXPLAIN QUERY PLAN's index choice is a
// real decision rather than an artefact of a table too small to bother with.
func seedRecentResultsFixture(t *testing.T, checkCount int) (*Service, *models.Organization, []*models.Check) {
	t.Helper()

	r := require.New(t)
	ctx := t.Context()

	s, err := New(ctx, Config{InMemory: true})
	r.NoError(err)
	r.NoError(s.Initialize(ctx))
	t.Cleanup(func() { _ = s.Close() })

	org := models.NewOrganization("recent-plan-org", "Recent Plan Org")
	r.NoError(s.CreateOrganization(ctx, org))

	now := time.Now().UTC().Truncate(time.Minute)
	checks := make([]*models.Check, 0, checkCount)

	for range checkCount {
		check := models.NewCheck(org.UID, "", "http")
		r.NoError(s.CreateCheck(ctx, check))
		checks = append(checks, check)

		for _, region := range []string{"eu2", "us1"} {
			for j := range 240 {
				raw := models.NewResult(org.UID, check.UID, models.ResultStatusUp, float32(40+j%7))
				raw.PeriodStart = now.Add(-time.Duration(j) * time.Minute)
				raw.Region = &region
				r.NoError(s.CreateResult(ctx, raw))
			}

			for j := range 168 {
				hour := models.NewResult(org.UID, check.UID, models.ResultStatusUp, 42)
				hour.PeriodType = models.PeriodTypeHour
				hour.PeriodStart = now.Truncate(time.Hour).Add(-time.Duration(j+1) * time.Hour)
				hour.Region = &region
				r.NoError(s.CreateResult(ctx, hour))
			}

			for j := range 60 {
				day := models.NewResult(org.UID, check.UID, models.ResultStatusUp, 42)
				day.PeriodType = models.PeriodTypeDay
				day.PeriodStart = now.Truncate(24*time.Hour).AddDate(0, 0, -(j + 1))
				day.Region = &region
				r.NoError(s.CreateResult(ctx, day))
			}
		}
	}

	_, err = s.DB().ExecContext(ctx, "ANALYZE")
	r.NoError(err)

	return s, org, checks
}

func recentResultsFilterFor(orgUID string, checks []*models.Check) *models.RecentResultsPerCheckFilter {
	now := time.Now().UTC()
	uids := make([]string, len(checks))

	for i, check := range checks {
		uids[i] = check.UID
	}

	return &models.RecentResultsPerCheckFilter{
		OrganizationUID: orgUID,
		CheckUIDs:       uids,
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

// TestRecentResultsPerCheckSeeksIndexes_SQLite is the SQLite plan regression for
// spec 2026-08-22-05. The statement EXPLAINed here is the very one
// RecentResultsPerCheck executes — same builder, same arguments — so it cannot
// drift from production by transcription.
//
// Both "no SCAN" assertions are backed by positive controls running the
// tier-less shape on the SAME fixture and requiring that it DOES scan. Without
// them, a fixture SQLite finds too small to index would make this pass
// vacuously — and the failure mode being guarded against (a per-check branch
// with no tier predicate) is a scan PER CHECK, invisible to every functional
// test.
func TestRecentResultsPerCheckSeeksIndexes_SQLite(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := t.Context()

	s, org, checks := seedRecentResultsFixture(t, 20)

	for _, size := range []int{1, 20} {
		filter := recentResultsFilterFor(org.UID, checks[:size])
		sql, args := recentResultsPerCheckSQL(filter, filter.CheckUIDs)
		plan := explainSQLiteQuery(ctx, t, s, bindSQLiteArgs(t, s, sql, args))

		r.Zero(countBaseTableScans(plan),
			"a %d-check page must not scan `results` — that is the whole defect:\n%s", size, plan)
		r.Contains(plan, "results_raw_idx",
			"the raw branch must ride results_raw_idx (%d checks):\n%s", size, plan)
		r.Contains(plan, "results_aggregated_idx",
			"the rollup branch must ride results_aggregated_idx (%d checks):\n%s", size, plan)

		// One index seek per (check, tier): the whole point of the per-check
		// shape is that the cost scales with the page, not with the table.
		r.Equal(size, strings.Count(plan, "results_raw_idx"),
			"one raw seek per check, not one shared scan (%d checks):\n%s", size, plan)
		r.Equal(size, strings.Count(plan, "results_aggregated_idx"),
			"one rollup seek per check (%d checks):\n%s", size, plan)
	}

	// Positive control: the SAME per-check shape with the tier predicate
	// dropped — the mistake this spec exists to forbid. It must scan, per
	// check, or the assertions above prove nothing.
	control := tierlessRecentResultsSQL(org.UID, checks[:20])
	controlPlan := explainSQLiteQuery(ctx, t, s, bindSQLiteArgs(t, s, control.sql, control.args))

	r.Equal(20, countBaseTableScans(controlPlan),
		"the tier-less per-check shape MUST scan `results` ONCE PER CHECK — otherwise the "+
			"plan assertions above prove nothing, and this is exactly why 'just add the "+
			"LATERAL' measured 18x worse than the query it would replace:\n%s", controlPlan)
}

// countBaseTableScans counts plan lines that read the `results` table itself
// rather than an index. The table is aliased `res` in this statement, so the
// line reads exactly "SCAN res" — matching on "SCAN result" would silently
// never fire, and matching on the "SCAN " prefix would also count the
// "SCAN (subquery-N)" lines every UNION ALL arm produces.
func countBaseTableScans(plan string) int {
	scans := 0

	for _, line := range strings.Split(plan, "\n") {
		if strings.TrimSpace(line) == "SCAN res" {
			scans++
		}
	}

	return scans
}

// TestRecentResultsPerCheckRestatedTierPredicateIsLoadBearing sharpens the
// control above. Dropping the tier predicate ENTIRELY is the obvious mistake;
// the subtle one is keeping `period_type IN ('raw')` and assuming SQLite will
// derive the partial index's own predicate from it. It does not (spec
// 2026-08-22-04), so the IN list alone still scans — once per check.
func TestRecentResultsPerCheckRestatedTierPredicateIsLoadBearing(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := t.Context()

	s, org, checks := seedRecentResultsFixture(t, 3)

	since := time.Now().UTC().Add(-26 * time.Hour)
	arms := make([]string, 0, len(checks))
	args := make([]any, 0, len(checks)*5)

	for _, check := range checks {
		arms = append(arms, `
			SELECT * FROM (
				SELECT `+recentResultsColumns()+`
				FROM results AS res
				WHERE res.organization_uid = ?
					AND res.check_uid = ?
					AND res.period_type IN (?)
					AND res.period_start >= ?
				ORDER BY res.period_start DESC
				LIMIT ?
			)`)
		args = append(args, org.UID, check.UID, bun.List([]string{models.PeriodTypeRaw}), since, 300)
	}

	plan := explainSQLiteQuery(ctx, t, s,
		bindSQLiteArgs(t, s, strings.Join(arms, "\n\t\t\tUNION ALL\n"), args))

	r.Equal(len(checks), countBaseTableScans(plan),
		"`period_type IN ('raw')` without the restated `period_type = 'raw'` MUST still "+
			"scan on SQLite — that restatement is what makes the partial index eligible:\n%s", plan)
}

type tierlessQuery struct {
	sql  string
	args []any
}

// tierlessRecentResultsSQL is the forbidden variant: the identical per-check
// UNION ALL, time-bounded, but with NO period_type predicate at all — exactly
// what "just add the LATERAL" produces. It exists only as the control for the
// test above and must never be reachable from production code (the filter type
// cannot express it: models.RecentResultsPerCheckFilter.Validate rejects a
// mixed tier).
func tierlessRecentResultsSQL(orgUID string, checks []*models.Check) tierlessQuery {
	since := time.Now().UTC().AddDate(0, 0, -200)
	arms := make([]string, 0, len(checks))
	args := make([]any, 0, len(checks)*4)

	for _, check := range checks {
		arms = append(arms, `
			SELECT * FROM (
				SELECT `+recentResultsColumns()+`
				FROM results AS res
				WHERE res.organization_uid = ?
					AND res.check_uid = ?
					AND res.period_start >= ?
				ORDER BY res.period_start DESC
				LIMIT ?
			)`)
		args = append(args, orgUID, check.UID, since, 300)
	}

	return tierlessQuery{sql: strings.Join(arms, "\n\t\t\tUNION ALL\n"), args: args}
}

// bindSQLiteArgs renders a parameterized statement into literal SQL, because
// EXPLAIN QUERY PLAN has to be handed one string. bun formats the arguments
// with the same dialect the query would run under.
func bindSQLiteArgs(t *testing.T, s *Service, sql string, args []any) string {
	t.Helper()

	return s.DB().NewRaw(sql, args...).String()
}

// TestRecentResultsPerCheck_Semantics pins WHAT the query returns, alongside the
// plan test that pins how it gets there: the per-check budget is honoured per
// check (a dense check cannot eat a sparse one's rows), each tier is bounded by
// its own `since`, and the blobs are never projected.
func TestRecentResultsPerCheck_Semantics(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := t.Context()

	s, org, checks := seedRecentResultsFixture(t, 3)

	filter := recentResultsFilterFor(org.UID, checks)
	filter.DefaultPerCheckLimit = 50
	filter.PerCheckLimits = map[string]int{checks[0].UID: 10}

	rows, err := s.RecentResultsPerCheck(ctx, filter)
	r.NoError(err)

	perCheck := make(map[string]int)
	for _, row := range rows {
		perCheck[row.CheckUID]++
		r.Nil(row.Metrics, "the response-time fetch must never project the blobs")
		r.Nil(row.Output)
		r.NotEmpty(row.UID, "every projected column must actually be scanned")
	}

	// Two branches each: the explicit budget for checks[0], the default for the
	// other two. Every branch has more matching rows than its budget, so these
	// are exact.
	r.Equal(2*10, perCheck[checks[0].UID], "an explicit per-check budget is honoured exactly")
	r.Equal(2*50, perCheck[checks[1].UID], "a dense neighbour cannot starve another check's budget")
	r.Equal(2*50, perCheck[checks[2].UID])

	// Each tier is bounded by ITS OWN since: the raw branch's 26 h bound must
	// not admit rollups, and the rollup branch must not admit raw.
	for _, row := range rows {
		if row.PeriodType == models.PeriodTypeRaw {
			r.False(row.PeriodStart.Before(filter.Tiers[0].Since),
				"a raw row older than the raw bound leaked in")
		} else {
			r.False(row.PeriodStart.Before(filter.Tiers[1].Since),
				"a rollup row older than the rollup bound leaked in")
		}
	}
}

// TestRecentResultsPerCheck_RejectsMixedTier pins the guard at the dialect
// boundary, not just in models: a caller that manages to build a tier-less
// filter gets an error instead of a statement that scans `results` once per
// check.
func TestRecentResultsPerCheck_RejectsMixedTier(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := t.Context()

	s, org, checks := seedRecentResultsFixture(t, 1)

	filter := recentResultsFilterFor(org.UID, checks)
	filter.Tiers = []models.RecentResultsTier{{
		PeriodTypes: []string{models.PeriodTypeRaw, models.PeriodTypeHour},
		Since:       time.Now().UTC().Add(-time.Hour),
	}}

	rows, err := s.RecentResultsPerCheck(ctx, filter)
	r.ErrorIs(err, models.ErrRecentResultsMixedTier)
	r.Nil(rows)
}
