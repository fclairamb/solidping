package postgres

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/uptimebar"
)

// Port distinct from every other _postgres_test.go file's embedded-Postgres
// port in this repo (see the port-numbering note in
// postgres_headroom_postgres_test.go).
const portUptimebarPlan = 15473

// planFilterRecorder is a uptimebar.ResultBucketAggregator that runs no query and
// only records the filters the availability engine builds. Capturing the filters
// from the REAL call path (rather than hand-writing them here) is what makes this
// test a regression guard: if a future edit puts 'raw' back into the rollup
// query's PeriodTypes, or drops the raw clamp, the captured filter changes and the
// plan assertions below fail.
type planFilterRecorder struct {
	filters []*models.ResultBucketFilter
}

func (p *planFilterRecorder) AggregateResultBuckets(
	_ context.Context, filter *models.ResultBucketFilter,
) ([]models.ResultBucket, error) {
	p.filters = append(p.filters, filter)

	return nil, nil
}

// explainAggregateResultBuckets returns the Postgres plan for the statement
// AggregateResultBuckets would run for this filter, built by the PRODUCTION
// builder with the production arguments, so the plan cannot drift from
// production by transcription.
func explainAggregateResultBuckets(
	ctx context.Context, t *testing.T, s *Service, filter *models.ResultBucketFilter,
) string {
	t.Helper()

	require.NoError(t, filter.Validate())

	return explainSQL(ctx, t, s, aggregateResultBucketsQuery(s.db, filter).String())
}

// explainListResults returns the Postgres plan for the query ListResults would
// run for this filter, built through the very same helpers (ExcludeColumn +
// applyResultsFilter) so the plan describes the real production statement. It
// serves the positive controls below: the row-shaped reads this spec replaced.
func explainListResults(
	ctx context.Context, t *testing.T, s *Service, filter *models.ListResultsFilter,
) string {
	t.Helper()

	var results []*models.Result

	query := s.db.NewSelect().Model(&results)
	if filter.SkipBlobs {
		query = query.ExcludeColumn("metrics", "output")
	}

	query = applyResultsFilter(query, filter)

	return explainSQL(ctx, t, s, query.String())
}

// seedPlanResults bulk-inserts raw rows for a check with a single INSERT …
// SELECT generate_series, which is orders of magnitude faster than row-by-row
// CreateResult calls and is what makes a 200 k-row fixture practical in a test.
func seedPlanRaw(ctx context.Context, t *testing.T, s *Service, orgUID, checkUID string, count int) {
	t.Helper()

	_, err := s.db.ExecContext(ctx,
		`INSERT INTO results (organization_uid, check_uid, period_type, period_start, region, status, duration)
		 SELECT ?, ?, 'raw', now() - (i * interval '10 seconds'), 'eu', 3, 42
		 FROM generate_series(1, ?) AS i`,
		orgUID, checkUID, count)
	require.NoError(t, err)
}

// seedPlanRollups inserts one hour rollup per hour and one day rollup per day
// over the given number of days.
func seedPlanRollups(ctx context.Context, t *testing.T, s *Service, orgUID, checkUID string, days int) {
	t.Helper()

	_, err := s.db.ExecContext(ctx,
		`INSERT INTO results
		   (organization_uid, check_uid, period_type, period_start, period_end, region, total_checks, successful_checks)
		 SELECT ?, ?, 'hour', date_trunc('hour', now()) - (i * interval '1 hour'),
		        date_trunc('hour', now()) - ((i - 1) * interval '1 hour'), 'eu', 360, 360
		 FROM generate_series(1, ?) AS i`,
		orgUID, checkUID, days*24)
	require.NoError(t, err)

	_, err = s.db.ExecContext(ctx,
		`INSERT INTO results
		   (organization_uid, check_uid, period_type, period_start, period_end, region, total_checks, successful_checks)
		 SELECT ?, ?, 'day', date_trunc('day', now()) - (i * interval '1 day'),
		        date_trunc('day', now()) - ((i - 1) * interval '1 day'), 'eu', 8640, 8640
		 FROM generate_series(1, ?) AS i`,
		orgUID, checkUID, days)
	require.NoError(t, err)
}

// TestUptimebarQueriesUseIndexes_Postgres is the plan regression for spec
// 2026-08-17-03: `results` has exactly two useful indexes and BOTH are partial
// (results_raw_idx WHERE period_type = 'raw', results_aggregated_idx WHERE
// period_type <> 'raw'). A predicate that straddles them —
// `period_type IN ('raw','hour','day')`, which is what the uptime-bar queries
// used to send — is implied by neither, so Postgres can only answer it with a
// sequential scan of the WHOLE table. On the live instance that meant reading
// ~318 MB and discarding ~891 k rows to return 17 k.
//
// The fix is the tier split in uptimebar. This test pins it at the level that
// actually matters — the query PLAN — because a timing assertion would be flaky.
// The final subtest is the positive control: it runs the OLD combined predicate
// against the SAME dataset and requires that it DOES seq-scan, proving the
// fixture is dense enough for the "no Seq Scan" assertions to have teeth.
//
//nolint:paralleltest // embedded-postgres tests run sequentially in this package
func TestUptimebarQueriesUseIndexes_Postgres(t *testing.T) {
	ctx := t.Context()
	r := require.New(t)

	s := newTier1ServicePG(t, portUptimebarPlan)

	org := models.NewOrganization("uptimebar-plan-org", "Uptimebar Plan Org")
	r.NoError(s.CreateOrganization(ctx, org))

	target := models.NewCheck(org.UID, "uptimebar-plan-target", "http")
	r.NoError(s.CreateCheck(ctx, target))

	// The check under test: a day of raw at a 10 s cadence plus 30 days of
	// rollups.
	seedPlanRaw(ctx, t, s, org.UID, target.UID, 8_640)
	seedPlanRollups(ctx, t, s, org.UID, target.UID, 30)

	// Noise: nine other checks with the same volume of raw. Without them a
	// single-check table would be trivially covered by any plan; with them the
	// target's rows are ~10 % of the table, which is what makes an index scan the
	// planner's genuine choice rather than an accident of a tiny fixture.
	for i := range 9 {
		noise := models.NewCheck(org.UID, fmt.Sprintf("uptimebar-plan-noise-%d", i), "http")
		r.NoError(s.CreateCheck(ctx, noise))
		seedPlanRaw(ctx, t, s, org.UID, noise.UID, 8_640)
		seedPlanRollups(ctx, t, s, org.UID, noise.UID, 30)
	}

	_, err := s.db.ExecContext(ctx, "ANALYZE results")
	r.NoError(err)

	// Capture the filters the real engine builds for both call sites, with the
	// hints the production path resolves (the live read-side retention, which is
	// what bounds the raw tier's clamp).
	planHints := uptimebar.Hints{RetentionRawHours: 24, RetentionHourDays: 7}

	rec := &planFilterRecorder{}
	now := time.Now().UTC()
	todayStart := now.Truncate(24 * time.Hour)

	_, err = uptimebar.BucketAvailability(
		ctx, rec, org.UID, []string{target.UID}, 24*time.Hour, todayStart.AddDate(0, 0, -29), 30, planHints)
	r.NoError(err)

	bucketFilters := len(rec.filters)
	r.Equal(2, bucketFilters, "BucketAvailability must issue exactly one aggregate per tier group")

	_, err = uptimebar.WindowAvailability(
		ctx, rec, org.UID, []string{target.UID}, now.AddDate(0, 0, -30), now, planHints)
	r.NoError(err)

	r.Len(rec.filters, 4, "WindowAvailability must issue exactly one aggregate per tier group")

	for i, filter := range rec.filters {
		name := fmt.Sprintf("aggregate %d (%s)", i, strings.Join(filter.PeriodTypes, "+"))

		plan := explainAggregateResultBuckets(ctx, t, s, filter)

		r.NotContains(plan, "Seq Scan on results",
			"%s must not sequentially scan results — that is the whole defect (plan:\n%s)", name, plan)
		r.NotContains(plan, "Parallel Seq Scan on results",
			"%s must not parallel-seq-scan results either (plan:\n%s)", name, plan)
		r.True(
			strings.Contains(plan, "Index Scan") || strings.Contains(plan, "Bitmap Index Scan"),
			"%s must be answered from an index (plan:\n%s)", name, plan)

		// Each tier side must ride ITS OWN partial index, not merely "an index":
		// that is what the restated period_type predicate buys.
		wantIndex := "results_aggregated_idx"
		if models.PeriodTypesTierSide(filter.PeriodTypes) == models.PeriodTierRaw {
			wantIndex = "results_raw_idx"
		}

		r.Contains(plan, wantIndex,
			"%s must ride %s (plan:\n%s)", name, wantIndex, plan)

		// No Sort node. The row path inherited applyResultsFilter's
		// `ORDER BY period_start DESC, uid DESC`, which this fold never needed
		// and which the planner answered with an external merge sort to disk
		// (12.8 MB measured on the 200-check page). A GROUP BY that the planner
		// chooses to answer by sorting would give that cost straight back.
		r.NotContains(plan, "Sort Method",
			"%s must not sort — the aggregate has no ORDER BY and must hash-aggregate "+
				"(plan:\n%s)", name, plan)
		r.NotContains(plan, "->  Sort",
			"%s must not contain a Sort node (plan:\n%s)", name, plan)
	}

	// Positive control 1: the row-shaped read this spec replaced, on the very
	// same data — the straddling-tier predicate that can only seq-scan.
	windowStart := todayStart.AddDate(0, 0, -29)
	combined := &models.ListResultsFilter{
		OrganizationUID:  org.UID,
		CheckUIDs:        []string{target.UID},
		PeriodTypes:      []string{models.PeriodTypeRaw, models.PeriodTypeHour, models.PeriodTypeDay},
		PeriodStartAfter: &windowStart,
		Limit:            884_300,
		SkipBlobs:        true,
	}

	controlPlan := explainListResults(ctx, t, s, combined)
	r.Contains(controlPlan, "Seq Scan on results",
		"the straddling predicate MUST still seq-scan — otherwise this fixture proves nothing "+
			"about the split queries above (plan:\n%s)", controlPlan)

	// Positive control 2: the tier-SPLIT row read — what shipped before this
	// spec. It rides the right index, so the split was never the remaining
	// problem; what it also does is ORDER BY period_start DESC, uid DESC and
	// ship every matching row. This control is what proves the "no Sort"
	// assertions above are about the aggregate and not about the fixture.
	rawStart := uptimebar.RawTierStart(windowStart, time.Now().UTC(), 24)
	rowPath := &models.ListResultsFilter{
		OrganizationUID:  org.UID,
		CheckUIDs:        []string{target.UID},
		PeriodTypes:      []string{models.PeriodTypeRaw},
		PeriodStartAfter: &rawStart,
		SkipBlobs:        true,
	}

	rowPathPlan := explainListResults(ctx, t, s, rowPath)
	r.Contains(rowPathPlan, "Sort",
		"the row path MUST sort (it inherits applyResultsFilter's ORDER BY) — otherwise the "+
			"no-Sort assertions above prove nothing (plan:\n%s)", rowPathPlan)
}
