package sqlite

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/uptrace/bun"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

// newResultsPlanService spins up an in-memory SQLite with one org and one check
// carrying `raw` rows at a 1-minute cadence plus hourly and daily rollups —
// enough shape for EXPLAIN QUERY PLAN to be meaningful, and the same tier
// layout the chart reads.
func newResultsPlanService(t *testing.T) (*Service, *models.Organization, *models.Check) {
	t.Helper()

	r := require.New(t)
	ctx := t.Context()

	s, err := New(ctx, Config{InMemory: true})
	r.NoError(err)
	r.NoError(s.Initialize(ctx))
	t.Cleanup(func() { _ = s.Close() })

	org := models.NewOrganization("chart-plan-org", "Chart Plan Org")
	r.NoError(s.CreateOrganization(ctx, org))

	check := models.NewCheck(org.UID, "chart-plan-check", "http")
	r.NoError(s.CreateCheck(ctx, check))

	now := time.Now().UTC().Truncate(time.Minute)

	for i := range 120 {
		raw := models.NewResult(org.UID, check.UID, models.ResultStatusUp, 42)
		raw.PeriodStart = now.Add(-time.Duration(i) * time.Minute)
		r.NoError(s.CreateResult(ctx, raw))
	}

	for i := range 48 {
		hour := models.NewResult(org.UID, check.UID, models.ResultStatusUp, 42)
		hour.PeriodType = models.PeriodTypeHour
		hour.PeriodStart = now.Truncate(time.Hour).Add(-time.Duration(i) * time.Hour)
		r.NoError(s.CreateResult(ctx, hour))
	}

	for i := range 30 {
		day := models.NewResult(org.UID, check.UID, models.ResultStatusUp, 42)
		day.PeriodType = models.PeriodTypeDay
		day.PeriodStart = now.Truncate(24*time.Hour).AddDate(0, 0, -i)
		r.NoError(s.CreateResult(ctx, day))
	}

	_, err = s.DB().ExecContext(ctx, "ANALYZE")
	r.NoError(err)

	return s, org, check
}

// explainResultsFilter returns SQLite's plan for the query ListResults would
// run for this filter, built through the very same helpers (ExcludeColumn +
// applyResultsFilter) so the plan describes the real production statement and
// cannot drift from it by transcription.
func explainResultsFilter(
	ctx context.Context, t *testing.T, s *Service, filter *models.ListResultsFilter,
) string {
	t.Helper()

	var rows []*models.Result

	query := s.DB().NewSelect().Model(&rows)
	if filter.SkipBlobs {
		query = query.ExcludeColumn("metrics", "output")
	}

	return explainSQLiteQuery(ctx, t, s, applyResultsFilter(query, filter).String())
}

func explainSQLiteQuery(ctx context.Context, t *testing.T, s *Service, sql string) string {
	t.Helper()

	// EXPLAIN QUERY PLAN emits (id, parent, notused, detail) — every column has
	// to be in the scan target for bun to accept the row.
	var plan []struct {
		ID      int    `bun:"id"`
		Parent  int    `bun:"parent"`
		NotUsed int    `bun:"notused"`
		Detail  string `bun:"detail"`
	}

	require.NoError(t, s.DB().NewRaw("EXPLAIN QUERY PLAN "+sql).Scan(ctx, &plan))

	var builder strings.Builder

	for i := range plan {
		builder.WriteString(plan[i].Detail)
		builder.WriteString("\n")
	}

	return builder.String()
}

// TestChartResultsQueriesUseIndexes_SQLite is the SQLite half of spec
// 2026-08-22-04's plan regression. Both useful indexes on `results` are partial
// and split on `period_type = 'raw'`, so a single query naming raw AND a rollup
// tier is satisfied by neither and SQLite can only answer it by scanning the
// whole table. Each single-tier assertion is paired with a positive control
// running the pre-fix mixed predicate on the SAME data: without them a fixture
// too small to interest the planner would make this pass vacuously.
func TestChartResultsQueriesUseIndexes_SQLite(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := t.Context()

	s, org, check := newResultsPlanService(t)

	weekStart := time.Now().UTC().AddDate(0, 0, -7)
	monthStart := time.Now().UTC().AddDate(0, 0, -30)

	// Exactly the tier lists chartFetchParams can emit
	// (web/dash0/src/components/checks/response-time-chart.tsx). They are
	// transcribed, not derived — nothing but this comment and the one on
	// chartFetchParams links the two languages, so ADDING A TIER LIST THERE
	// MEANS ADDING IT HERE, or its plan goes unverified. The TypeScript side
	// guards separately that no emitted list ever mixes raw with a rollup tier
	// (web/dash0/src/components/checks/chart-fetch-params.test.ts).
	tiers := []struct {
		name        string
		periodTypes []string
		startAfter  time.Time
		wantIndex   string
	}{
		{
			"month rollups",
			[]string{models.PeriodTypeHour, models.PeriodTypeDay},
			monthStart,
			"results_aggregated_idx",
		},
		{"week rollups", []string{models.PeriodTypeHour}, weekStart, "results_aggregated_idx"},
		{"raw", []string{models.PeriodTypeRaw}, weekStart, "results_raw_idx"},
	}

	for _, tier := range tiers {
		startAfter := tier.startAfter
		plan := explainResultsFilter(ctx, t, s, &models.ListResultsFilter{
			OrganizationUID:  org.UID,
			CheckUIDs:        []string{check.UID},
			PeriodTypes:      tier.periodTypes,
			PeriodStartAfter: &startAfter,
			Limit:            1001,
			SkipBlobs:        true,
		})

		r.Contains(plan, tier.wantIndex,
			"%s must ride the matching partial index:\n%s", tier.name, plan)
		r.NotContains(plan, "SCAN result",
			"%s must not scan the results table — that is the whole defect:\n%s", tier.name, plan)
	}

	// Positive controls: the pre-fix mixed predicates on the same data.
	mixed := []struct {
		name        string
		periodTypes []string
		startAfter  time.Time
	}{
		{"pre-fix week (raw,hour)", []string{models.PeriodTypeRaw, models.PeriodTypeHour}, weekStart},
		{
			"pre-fix month (raw,hour,day)",
			[]string{models.PeriodTypeRaw, models.PeriodTypeHour, models.PeriodTypeDay},
			monthStart,
		},
	}

	for _, control := range mixed {
		startAfter := control.startAfter
		plan := explainResultsFilter(ctx, t, s, &models.ListResultsFilter{
			OrganizationUID:  org.UID,
			CheckUIDs:        []string{check.UID},
			PeriodTypes:      control.periodTypes,
			PeriodStartAfter: &startAfter,
			Limit:            1001,
			SkipBlobs:        true,
		})

		r.Contains(plan, "SCAN result",
			"%s MUST still scan — otherwise the assertions above prove nothing:\n%s", control.name, plan)
	}

	// A cursor page: the row-value comparison gives SQLite an upper bound on
	// period_start it can fold into the same index range scan, so page N reads
	// from the cursor rather than from the window start.
	cursorAt := time.Now().UTC().Add(-30 * time.Minute)
	cursorUID := "ffffffff-ffff-ffff-ffff-ffffffffffff"
	cursorPlan := explainResultsFilter(ctx, t, s, &models.ListResultsFilter{
		OrganizationUID:  org.UID,
		CheckUIDs:        []string{check.UID},
		PeriodTypes:      []string{models.PeriodTypeRaw},
		PeriodStartAfter: &weekStart,
		CursorTimestamp:  &cursorAt,
		CursorUID:        &cursorUID,
		Limit:            1001,
		SkipBlobs:        true,
	})

	r.Contains(cursorPlan, "results_raw_idx", "a cursor page must ride the raw index:\n%s", cursorPlan)
	r.NotContains(cursorPlan, "SCAN result", "a cursor page must not scan:\n%s", cursorPlan)
	r.Contains(cursorPlan, "period_start<?",
		"the cursor must bound the index range, not be re-checked afterwards:\n%s", cursorPlan)
}

// TestResultsCursorParity_SQLite pins spec 2026-08-22-04 §4: the row-value
// keyset cursor `(period_start, uid) < (?, ?)` is a REWRITE of the OR form, not
// a behavior change. Both walks are run side by side over the same seed —
// including rows deliberately sharing a period_start so the uid tie-break is
// exercised — and must agree on the uid sequence, the page boundaries, and the
// full unpaginated ordering.
func TestResultsCursorParity_SQLite(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := t.Context()

	s, err := New(ctx, Config{InMemory: true})
	r.NoError(err)
	r.NoError(s.Initialize(ctx))
	t.Cleanup(func() { _ = s.Close() })

	org := models.NewOrganization("cursor-parity-org", "Cursor Parity Org")
	r.NoError(s.CreateOrganization(ctx, org))

	check := models.NewCheck(org.UID, "cursor-parity-check", "http")
	r.NoError(s.CreateCheck(ctx, check))

	base := time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC)

	// 30 rows over 10 distinct instants, three per instant — a check probed
	// from three regions produces exactly this, so equal period_start values
	// are the normal case and the tie-break is load-bearing.
	for i := range 10 {
		for range 3 {
			res := models.NewResult(org.UID, check.UID, models.ResultStatusUp, 42)
			res.PeriodStart = base.Add(-time.Duration(i) * time.Minute)
			r.NoError(s.CreateResult(ctx, res))
		}
	}

	windowStart := base.AddDate(0, 0, -1)

	baseFilter := func() *models.ListResultsFilter {
		return &models.ListResultsFilter{
			OrganizationUID:  org.UID,
			CheckUIDs:        []string{check.UID},
			PeriodTypes:      []string{models.PeriodTypeRaw},
			PeriodStartAfter: &windowStart,
			SkipBlobs:        true,
		}
	}

	// Reference: the whole window in one page.
	full := baseFilter()
	full.Limit = 1000
	all, err := s.ListResults(ctx, full)
	r.NoError(err)
	// 30 seeded rows plus the lifecycle-marker row CreateCheck mints.
	r.Len(all.Results, 31)

	wantUIDs := make([]string, 0, len(all.Results))
	for _, row := range all.Results {
		wantUIDs = append(wantUIDs, row.UID)
	}

	const pageSize = 7

	// Production walk: the row-value cursor, through ListResults itself.
	rowValuePages := walkPages(ctx, t, pageSize, func(
		cursorTS *time.Time, cursorUID *string,
	) []*models.Result {
		f := baseFilter()
		f.Limit = pageSize
		f.CursorTimestamp = cursorTS
		f.CursorUID = cursorUID

		page, listErr := s.ListResults(ctx, f)
		require.NoError(t, listErr)

		return page.Results
	})

	// Reference walk: the OR form this replaced, built by hand over the same data.
	orFormPages := walkPages(ctx, t, pageSize, func(
		cursorTS *time.Time, cursorUID *string,
	) []*models.Result {
		var rows []*models.Result

		query := s.DB().NewSelect().Model(&rows).
			ExcludeColumn("metrics", "output").
			Where("result.organization_uid = ?", org.UID).
			Where("result.check_uid IN (?)", bun.List([]string{check.UID})).
			Where("result.period_type IN (?)", bun.List([]string{models.PeriodTypeRaw})).
			Where("result.period_start >= ?", windowStart).
			Order("result.period_start DESC").
			Order("result.uid DESC").
			Limit(pageSize)

		if cursorTS != nil && cursorUID != nil {
			query = query.Where("(result.period_start < ?) OR (result.period_start = ? AND result.uid < ?)",
				*cursorTS, *cursorTS, *cursorUID)
		}

		require.NoError(t, query.Scan(ctx))

		return rows
	})

	r.Equal(wantUIDs, flattenUIDs(rowValuePages),
		"the row-value cursor must walk the whole window, in the unpaginated order")
	r.Equal(orFormPages, rowValuePages,
		"the row-value cursor must reproduce the OR form's pages and page boundaries exactly")
	r.Len(rowValuePages, 5, "31 rows at 7 per page is 5 pages")
}

// walkPages follows the keyset cursor until a short page ends the walk,
// returning the uid of every row, grouped by page.
func walkPages(
	_ context.Context, t *testing.T, pageSize int,
	fetch func(cursorTS *time.Time, cursorUID *string) []*models.Result,
) [][]string {
	t.Helper()

	var (
		pages     [][]string
		cursorTS  *time.Time
		cursorUID *string
	)

	for range 100 {
		rows := fetch(cursorTS, cursorUID)
		if len(rows) == 0 {
			break
		}

		page := make([]string, 0, len(rows))
		for _, row := range rows {
			page = append(page, row.UID)
		}

		pages = append(pages, page)

		last := rows[len(rows)-1]
		ts := last.PeriodStart
		uid := last.UID
		cursorTS = &ts
		cursorUID = &uid

		if len(rows) < pageSize {
			break
		}
	}

	return pages
}

func flattenUIDs(pages [][]string) []string {
	var out []string
	for _, page := range pages {
		out = append(out, page...)
	}

	return out
}
