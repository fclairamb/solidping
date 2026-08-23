package postgres

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/uptrace/bun"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/handlers/results"
)

// Port distinct from every other _postgres_test.go file's embedded-Postgres
// port in this repo (see the port-numbering note in
// postgres_headroom_postgres_test.go).
const (
	portChartResultsPlan = 15492
	portChartCursorPlan  = 15493
)

// chartWithFields mirrors CHART_WITH_FIELDS in
// web/dash0/src/components/checks/response-time-chart.tsx — the `with` list the
// chart actually sends. It names no blob-backed field, which is what must make
// the service drop metrics/output from the projection.
func chartWithFields() []string {
	return []string{
		"durationMs", "region", "durationMinMs", "durationMaxMs",
		"durationAvgMs", "durationP95Ms", "totalChecks",
	}
}

// recordingResultsDB wraps the real Postgres service so the filter the results
// HANDLER builds can be captured while the query still executes for real.
// Capturing the filter from the production path — rather than hand-writing one
// here — is what makes the plan assertions below a regression guard: if a future
// edit stops setting SkipBlobs, or changes how period types reach the DB layer,
// the captured filter changes and these assertions move with it.
type recordingResultsDB struct {
	*Service

	filters []*models.ListResultsFilter
}

func (r *recordingResultsDB) ListResults(
	ctx context.Context, filter *models.ListResultsFilter,
) (*models.ListResultsResponse, error) {
	r.filters = append(r.filters, filter)

	return r.Service.ListResults(ctx, filter)
}

// captureChartFilter runs one real results-API request and returns the filter it
// produced at the DB boundary.
func captureChartFilter(
	ctx context.Context, t *testing.T, s *Service, orgSlug string, opts *results.ListResultsOptions,
) *models.ListResultsFilter {
	t.Helper()

	rec := &recordingResultsDB{Service: s}
	_, err := results.NewService(rec).ListResults(ctx, orgSlug, opts)
	require.NoError(t, err)
	require.Len(t, rec.filters, 1, "one API request must issue exactly one DB query")

	return rec.filters[0]
}

// TestChartQueriesUseIndexes_Postgres is the plan regression for spec
// 2026-08-22-04. `results` has exactly two useful indexes and BOTH are partial:
// results_raw_idx WHERE period_type = 'raw', results_aggregated_idx WHERE
// period_type <> 'raw'. The chart used to send ONE query naming both sides of
// that split (`periodType=raw,hour` for a week, `raw,hour,day` for a month);
// neither partial predicate implies it, so the only plan left is a sequential
// scan of the largest table in the system — 663 ms on a 2 GB table versus 3 ms
// for the single-tier form, and it grows with total retained history forever.
//
// The fix is the tier split in chartFetchParams. This test pins it where it
// matters — the query PLAN — because a timing assertion would be flaky. Every
// "no Seq Scan" assertion is backed by a positive control that runs the pre-fix
// mixed predicate on the SAME fixture and requires that it DOES seq-scan;
// without those controls a fixture too small to bother the planner would make
// the whole test pass vacuously.
//
//nolint:paralleltest // embedded-postgres tests run sequentially in this package
func TestChartQueriesUseIndexes_Postgres(t *testing.T) {
	ctx := t.Context()
	r := require.New(t)

	s := newTier1ServicePG(t, portChartResultsPlan)

	org := models.NewOrganization("chart-plan-org", "Chart Plan Org")
	r.NoError(s.CreateOrganization(ctx, org))

	target := models.NewCheck(org.UID, "chart-plan-target", "http")
	r.NoError(s.CreateCheck(ctx, target))

	// A day of raw at a 10 s cadence plus 30 days of rollups — production
	// retention shape, for the check the chart is looking at.
	seedPlanRaw(ctx, t, s, org.UID, target.UID, 7_200)
	seedPlanRollups(ctx, t, s, org.UID, target.UID, 45)

	// Noise: nine other checks with the same volume. Without them a
	// single-check table is trivially covered by any plan; with them the
	// target's rows are ~10 % of the table, which is what makes an index scan
	// the planner's genuine choice rather than an accident of a tiny fixture.
	for i := range 9 {
		noise := models.NewCheck(org.UID, fmt.Sprintf("chart-plan-noise-%d", i), "http")
		r.NoError(s.CreateCheck(ctx, noise))
		seedPlanRaw(ctx, t, s, org.UID, noise.UID, 7_200)
		seedPlanRollups(ctx, t, s, org.UID, noise.UID, 45)
	}

	_, err := s.db.ExecContext(ctx, "ANALYZE results")
	r.NoError(err)

	monthStart := time.Now().UTC().AddDate(0, 0, -30)
	weekStart := time.Now().UTC().AddDate(0, 0, -7)

	// Exactly the tier lists chartFetchParams can emit
	// (web/dash0/src/components/checks/response-time-chart.tsx). They are
	// transcribed, not derived — nothing but this comment and the one on
	// chartFetchParams links the two languages, so ADDING A TIER LIST THERE
	// MEANS ADDING IT HERE, or its plan goes unverified. The TypeScript side
	// guards separately that no emitted list ever mixes raw with a rollup tier
	// (web/dash0/src/components/checks/chart-fetch-params.test.ts).
	chartTiers := []struct {
		name        string
		periodTypes []string
		startAfter  time.Time
	}{
		{"month rollups", []string{models.PeriodTypeHour, models.PeriodTypeDay}, monthStart},
		{"week rollups", []string{models.PeriodTypeHour}, weekStart},
		{"month raw", []string{models.PeriodTypeRaw}, monthStart},
		{"week raw", []string{models.PeriodTypeRaw}, weekStart},
	}

	for _, tier := range chartTiers {
		startAfter := tier.startAfter
		filter := captureChartFilter(ctx, t, s, org.Slug, &results.ListResultsOptions{
			Checks:           []string{target.UID},
			PeriodTypes:      tier.periodTypes,
			PeriodStartAfter: &startAfter,
			Size:             1000,
			With:             chartWithFields(),
		})

		r.True(filter.SkipBlobs,
			"%s: the chart's `with` list names no blob field, so metrics/output must be "+
				"dropped from the projection", tier.name)

		plan := explainListResults(ctx, t, s, filter)

		r.NotContains(plan, "Seq Scan on results",
			"%s must not sequentially scan results — that is the whole defect (plan:\n%s)", tier.name, plan)
		r.True(
			strings.Contains(plan, "Index Scan") || strings.Contains(plan, "Bitmap Index Scan"),
			"%s must be answered from an index (plan:\n%s)", tier.name, plan)
		r.True(
			strings.Contains(plan, "results_raw_idx") || strings.Contains(plan, "results_aggregated_idx"),
			"%s must use one of the two partial indexes on results (plan:\n%s)", tier.name, plan)
	}

	// Positive controls: the pre-fix mixed predicates, on the very same data,
	// through the very same production path. If these ever stop seq-scanning,
	// the assertions above prove nothing.
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
		filter := captureChartFilter(ctx, t, s, org.Slug, &results.ListResultsOptions{
			Checks:           []string{target.UID},
			PeriodTypes:      control.periodTypes,
			PeriodStartAfter: &startAfter,
			Size:             1000,
			With:             chartWithFields(),
		})

		plan := explainListResults(ctx, t, s, filter)
		r.Contains(plan, "Seq Scan on results",
			"%s MUST still seq-scan — otherwise this fixture proves nothing about the "+
				"split queries above (plan:\n%s)", control.name, plan)
	}
}

// TestResultsCursorSeeksByRowValue_Postgres pins spec 2026-08-22-04 §4: the
// keyset cursor is a row-value comparison, `(period_start, uid) < (?, ?)`,
// which follows ORDER BY (period_start DESC, uid DESC) exactly and so becomes
// the START POINT of a single index range scan. The equivalent OR form —
// `(period_start < ?) OR (period_start = ? AND uid < ?)` — returns the same
// rows but cannot be an index bound: Postgres answers it by reading everything
// before the cursor and sorting, so page N gets more expensive as the window
// widens while the page size does not.
//
// The control below runs the OR form on the same data and requires that it does
// NOT seek on both cursor columns, which is the structural difference.
//
//nolint:paralleltest // embedded-postgres tests run sequentially in this package
func TestResultsCursorSeeksByRowValue_Postgres(t *testing.T) {
	ctx := t.Context()
	r := require.New(t)

	s := newTier1ServicePG(t, portChartCursorPlan)

	org := models.NewOrganization("cursor-plan-org", "Cursor Plan Org")
	r.NoError(s.CreateOrganization(ctx, org))

	target := models.NewCheck(org.UID, "cursor-plan-target", "http")
	r.NoError(s.CreateCheck(ctx, target))
	seedPlanRaw(ctx, t, s, org.UID, target.UID, 9_000)

	for i := range 9 {
		noise := models.NewCheck(org.UID, fmt.Sprintf("cursor-plan-noise-%d", i), "http")
		r.NoError(s.CreateCheck(ctx, noise))
		seedPlanRaw(ctx, t, s, org.UID, noise.UID, 9_000)
	}

	_, err := s.db.ExecContext(ctx, "ANALYZE results")
	r.NoError(err)

	// Page 1 through the real API, then page 2 with the cursor it handed back.
	const pageSize = 1000

	windowStart := time.Now().UTC().AddDate(0, 0, -1)
	page1, err := results.NewService(s).ListResults(ctx, org.Slug, &results.ListResultsOptions{
		Checks:           []string{target.UID},
		PeriodTypes:      []string{models.PeriodTypeRaw},
		PeriodStartAfter: &windowStart,
		Size:             pageSize,
		With:             chartWithFields(),
	})
	r.NoError(err)
	r.NotEmpty(page1.Pagination.Cursor, "a 1 000-row page of a 9 000-row window must have a next cursor")

	filter := captureChartFilter(ctx, t, s, org.Slug, &results.ListResultsOptions{
		Checks:           []string{target.UID},
		PeriodTypes:      []string{models.PeriodTypeRaw},
		PeriodStartAfter: &windowStart,
		Cursor:           page1.Pagination.Cursor,
		Size:             pageSize,
		With:             chartWithFields(),
	})
	r.NotNil(filter.CursorTimestamp, "the cursor must reach the DB layer")
	r.NotNil(filter.CursorUID)

	plan := explainListResults(ctx, t, s, filter)
	r.NotContains(plan, "Seq Scan on results", "a cursor page must not seq-scan (plan:\n%s)", plan)
	r.Contains(plan, "results_raw_idx", "a cursor page must seek the raw index (plan:\n%s)", plan)
	// The row-value comparison lets Postgres derive an UPPER bound on
	// period_start and fold it into the index seek, so the scan starts AT the
	// cursor: only rows sharing the cursor's exact period_start are read and
	// rejected. (Both forms still carry an Incremental Sort — uid is not in
	// results_raw_idx, and spec 2026-08-22-04 explicitly rejects adding an index
	// that would put it there.)
	r.Contains(indexCondLine(plan), "period_start <=",
		"the cursor must bound the index scan itself, not be re-checked afterwards (plan:\n%s)", plan)
	r.Less(rowsRemovedByFilter(plan), pageSize,
		"a cursor page must not re-read the pages before it (plan:\n%s)", plan)

	// Control: the OR form the row-value comparison replaced. Same rows, same
	// data, but uid cannot bound the scan.
	var controlRows []*models.Result

	orForm := s.db.NewSelect().Model(&controlRows).
		ExcludeColumn("metrics", "output").
		Where("result.organization_uid = ?", filter.OrganizationUID).
		Where("result.check_uid IN (?)", bun.List(filter.CheckUIDs)).
		Where("result.period_type IN (?)", bun.List(filter.PeriodTypes)).
		Where("result.period_start >= ?", *filter.PeriodStartAfter).
		Where("(result.period_start < ?) OR (result.period_start = ? AND result.uid < ?)",
			*filter.CursorTimestamp, *filter.CursorTimestamp, *filter.CursorUID).
		Order("result.period_start DESC").
		Order("result.uid DESC").
		Limit(filter.Limit)

	controlPlan := explainSQL(ctx, t, s, orForm.String())
	// Control: the OR form cannot become an index bound, so the scan restarts at
	// the window's start and discards the whole first page before emitting a
	// single row of the second — and that discarded prefix grows with the
	// window while the page size does not.
	r.NotContains(indexCondLine(controlPlan), "period_start <=",
		"the OR form MUST NOT bound the index — otherwise the assertion above proves "+
			"nothing (plan:\n%s)", controlPlan)
	r.GreaterOrEqual(rowsRemovedByFilter(controlPlan), pageSize,
		"the OR form MUST re-read and discard the whole preceding page (plan:\n%s)", controlPlan)
}

// indexCondLine returns the plan's "Index Cond:" line — the predicate that
// actually bounds the index scan, as opposed to a "Filter:" applied to rows the
// scan already read.
func indexCondLine(plan string) string {
	for _, line := range strings.Split(plan, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "Index Cond:") {
			return trimmed
		}
	}

	return ""
}

// rowsRemovedByFilter totals the rows the plan read and then threw away.
func rowsRemovedByFilter(plan string) int {
	const marker = "Rows Removed by Filter: "

	total := 0

	for _, line := range strings.Split(plan, "\n") {
		idx := strings.Index(line, marker)
		if idx < 0 {
			continue
		}

		var n int
		if _, err := fmt.Sscanf(strings.TrimSpace(line[idx+len(marker):]), "%d", &n); err == nil {
			total += n
		}
	}

	return total
}

// explainSQL runs EXPLAIN over a literal statement and returns the plan text.
func explainSQL(ctx context.Context, t *testing.T, s *Service, sql string) string {
	t.Helper()

	rows, err := s.db.QueryContext(ctx, "EXPLAIN (ANALYZE, BUFFERS) "+sql)
	require.NoError(t, err)

	defer func() { _ = rows.Close() }()

	var plan strings.Builder

	for rows.Next() {
		var line string

		require.NoError(t, rows.Scan(&line))
		plan.WriteString(line)
		plan.WriteString("\n")
	}

	require.NoError(t, rows.Err())

	return plan.String()
}
