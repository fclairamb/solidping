package postgres

import (
	"context"
	"fmt"
	"sort"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

// Ports distinct from every other _postgres_test.go file's embedded-Postgres
// port in this repo (see the port-numbering note in
// postgres_headroom_postgres_test.go).
const (
	portResponseTimeBinParity = 15534
	portResponseTimeBinP95    = 15535
	portResponseTimeBinPlan   = 15536
)

// responseTimeBinKey identifies one seam bin in the Go reference fold. The region
// is carried as a string plus a "was NULL" flag rather than a *string so the key
// stays comparable — and so a NULL region and a region literally named "" would
// show up as different bins if they ever collided (they must not merge: the chart
// keys its series on the region).
type responseTimeBinKey struct {
	CheckUID   string
	Region     string
	NullRegion bool
	BinStart   time.Time
}

// referenceResponseTimeBin is one bin folded in Go — the reference the aggregate
// must reproduce.
type referenceResponseTimeBin struct {
	Total     int
	Up        int
	Durations []float32
	Counts    map[int]int
}

// referenceResponseTimeBins folds raw rows the way the seam aggregate must, in Go:
// the aggregate's WHERE clause, then the bin grid (time.Truncate, whose origin the
// SQL bin expression reproduces), then the counters.
//
// Deliberately written out rather than delegating to uptimebar: this aggregate is
// NOT BucketStats — it carries a percentile and it groups by region — so there is
// no existing canonical fold to route through, and a hand fold is exactly what the
// SQL has to be checked against.
func referenceResponseTimeBins(
	rows []*models.Result, filter *models.ResponseTimeBinFilter,
) map[responseTimeBinKey]*referenceResponseTimeBin {
	out := make(map[responseTimeBinKey]*referenceResponseTimeBin)

	for _, row := range rows {
		if row.PeriodType != models.PeriodTypeRaw {
			continue
		}

		if row.PeriodStart.Before(filter.Since) {
			continue
		}

		// The aggregate drops these BEFORE binning, so a bin whose only probes are
		// excluded produces no group at all rather than an all-zero one.
		if row.Status == nil || row.ExcludedFromAvailability() {
			continue
		}

		key := responseTimeBinKey{
			CheckUID:   row.CheckUID,
			BinStart:   row.PeriodStart.UTC().Truncate(filter.BinDuration),
			NullRegion: row.Region == nil,
		}
		if row.Region != nil {
			key.Region = *row.Region
		}

		bin := out[key]
		if bin == nil {
			bin = &referenceResponseTimeBin{Counts: map[int]int{}}
			out[key] = bin
		}

		bin.Total++

		if models.ResultStatus(*row.Status).CountsAsUp() {
			bin.Up++
		}

		bin.Counts[*row.Status]++

		if row.Duration != nil {
			bin.Durations = append(bin.Durations, *row.Duration)
		}
	}

	return out
}

// requireSameResponseTimeBins compares the aggregate's output against the Go fold.
//
// The p95 is asserted at models.ResponseTimeBinP95Index — the aggregation job's own
// nearest-rank index, pinned against the job's helper itself by
// TestRawMetricsP95UsesTheSharedNearestRankIndex in the jobtypes package. Avg is
// compared with a tolerance (float addition is not associative and the two paths
// sum in different orders); every other number is exact.
func requireSameResponseTimeBins(
	t *testing.T, want map[responseTimeBinKey]*referenceResponseTimeBin,
	got []models.ResponseTimeBin, filter *models.ResponseTimeBinFilter, label string,
) {
	t.Helper()

	r := require.New(t)

	r.Len(got, len(want), "%s: different number of bins", label)

	for i := range got {
		bin := &got[i]

		key := responseTimeBinKey{
			CheckUID:   bin.CheckUID,
			BinStart:   bin.BinStart,
			NullRegion: bin.Region == nil,
		}
		if bin.Region != nil {
			key.Region = *bin.Region
		}

		reference, ok := want[key]
		r.True(ok, "%s: unexpected bin %+v", label, key)

		r.Equal(reference.Total, bin.Total, "%s: bin %+v total", label, key)
		r.Equal(reference.Up, bin.Up, "%s: bin %+v up", label, key)
		r.Equal(reference.Counts, bin.StatusCounts, "%s: bin %+v status counts", label, key)
		r.True(bin.BinStart.Equal(bin.BinStart.Truncate(filter.BinDuration)),
			"%s: bin %+v is not aligned to the bin grid", label, key)

		if len(reference.Durations) == 0 {
			// The spec's "a bin whose probes all lack a duration still comes back":
			// counts only, and never a confident zero for a response time nobody
			// measured.
			r.Nil(bin.DurationP95, "%s: bin %+v p95", label, key)
			r.Nil(bin.DurationAvg, "%s: bin %+v avg", label, key)
			r.Nil(bin.DurationMin, "%s: bin %+v min", label, key)
			r.Nil(bin.DurationMax, "%s: bin %+v max", label, key)

			continue
		}

		sorted := append([]float32(nil), reference.Durations...)
		sort.Slice(sorted, func(a, b int) bool { return sorted[a] < sorted[b] })

		var sum float32
		for _, duration := range sorted {
			sum += duration
		}

		r.NotNil(bin.DurationP95, "%s: bin %+v p95", label, key)
		r.InDelta(sorted[models.ResponseTimeBinP95Index(len(sorted))], *bin.DurationP95, 0.001,
			"%s: bin %+v p95", label, key)
		r.NotNil(bin.DurationAvg, "%s: bin %+v avg", label, key)
		r.InDelta(sum/float32(len(sorted)), *bin.DurationAvg, 0.01, "%s: bin %+v avg", label, key)
		r.NotNil(bin.DurationMin, "%s: bin %+v min", label, key)
		r.InDelta(sorted[0], *bin.DurationMin, 0.001, "%s: bin %+v min", label, key)
		r.NotNil(bin.DurationMax, "%s: bin %+v max", label, key)
		r.InDelta(sorted[len(sorted)-1], *bin.DurationMax, 0.001, "%s: bin %+v max", label, key)
	}
}

// seedResponseTimeBinFixture seeds the mix the parity test folds two ways, and
// returns the rows it wrote. It mirrors the sqlite package's copy row for row.
//
// What is in here and why:
//
//   - two named regions AND a NULL region, because region is a GROUP BY key on this
//     aggregate: a NULL-safe join that regressed to `=` would silently drop the
//     NULL-region series entirely, which is the legacy/marker series;
//   - probes with NULL durations mixed into bins that also have real ones (they must
//     not enter the percentile, drag the minimum to 0, or shift the p95 rank);
//   - the three statuses excluded from availability (created/running/abandoned),
//     sharing bins with countable probes, so a predicate that failed to drop one
//     inflates that bin's Total instead of producing a visibly separate bin;
//   - a bin at +90 min whose probes ALL lack a duration — the spec's explicit case:
//     it must still come back, with counts and four NULL durations;
//   - 40 probes in one minute-bin with distinct durations, so the nearest-rank p95
//     is a specific sample the test can point at rather than one of a cluster of
//     equal values (this is where an interpolating percentile would diverge);
//   - a Warning probe, which counts as UP.
func seedResponseTimeBinFixture(
	ctx context.Context, t *testing.T, create func(context.Context, *models.Result) error,
	orgUID, checkUID string, anchor time.Time,
) []*models.Result {
	t.Helper()

	r := require.New(t)

	rows := make([]*models.Result, 0, 80)

	dur := func(value float32) *float32 { return &value }

	raw := func(status models.ResultStatus, offset time.Duration, duration *float32, region *string) {
		code := int(status)
		row := models.NewResult(orgUID, checkUID, status, 0)
		row.PeriodStart = anchor.Add(offset)
		row.Region = region
		row.Status = &code
		row.Duration = duration
		row.Metrics, row.Output = nil, nil
		r.NoError(create(ctx, row))
		rows = append(rows, row)
	}

	eu := "eu-1"
	us := "us-1"

	// The dense bin: 40 distinct durations inside one minute, in eu-1. The
	// nearest-rank p95 of 40 samples is the 38th smallest (index 38 →
	// (40*19)/20 = 38), i.e. 390 ms for durations 10, 20, … 400.
	for i := range 40 {
		raw(models.ResultStatusUp, time.Duration(i)*time.Second, dur(float32(10*(i+1))), &eu)
	}

	// The same minute in us-1, so the two regions are separate bins rather than a
	// summed one.
	for i := range 7 {
		raw(models.ResultStatusUp, time.Duration(i)*time.Second, dur(float32(100+i)), &us)
	}

	// Excluded statuses sharing the dense bin. None may reach Total, Up or the mix.
	raw(models.ResultStatusCreated, 41*time.Second, dur(5), &eu)
	raw(models.ResultStatusRunning, 42*time.Second, nil, &eu)
	raw(models.ResultStatusAbandoned, 43*time.Second, dur(99999), &eu)

	// NULL region, its own bin: the legacy/marker series.
	raw(models.ResultStatusUp, 44*time.Second, dur(250), nil)
	raw(models.ResultStatusDown, 45*time.Second, nil, nil)

	// A later bin mixing NULL and real durations, plus a warning (counts as up)
	// and two failures, so Up is a strict subset of Total and the status mix has
	// three entries.
	raw(models.ResultStatusUp, 30*time.Minute, dur(140), &eu)
	raw(models.ResultStatusWarning, 30*time.Minute+time.Second, dur(2200), &eu)
	raw(models.ResultStatusDown, 30*time.Minute+2*time.Second, nil, &eu)
	raw(models.ResultStatusTimeout, 30*time.Minute+3*time.Second, dur(30000), &eu)
	raw(models.ResultStatusUp, 30*time.Minute+4*time.Second, nil, &eu)

	// The durationless bin.
	for i := range 4 {
		raw(models.ResultStatusDown, 90*time.Minute+time.Duration(i)*time.Second, nil, &us)
	}

	// A probe BEFORE the window, to prove Since is enforced.
	raw(models.ResultStatusUp, -7*time.Hour, dur(77), &eu)

	return rows
}

// TestAggregateResponseTimeBins_ParityWithGoFold_Postgres is the Postgres half of the seam
// aggregate's correctness contract: the database's per-(check, region, bin)
// numbers must equal what a Go fold over the same rows produces, at every bin
// width seamBinWidth can pick, with the p95 at the aggregation job's own
// nearest-rank index. The sqlite package asserts the same thing against the same
// fixture.
//
//nolint:paralleltest // embedded-postgres tests run sequentially in this package
func TestAggregateResponseTimeBins_ParityWithGoFold_Postgres(t *testing.T) {
	ctx := t.Context()
	r := require.New(t)

	s := newTier1ServicePG(t, portResponseTimeBinParity)

	org := models.NewOrganization("seam-parity-org", "Seam Parity Org")
	r.NoError(s.CreateOrganization(ctx, org))

	first := models.NewCheck(org.UID, "seam-parity", "http")
	r.NoError(s.CreateCheck(ctx, first))

	second := models.NewCheck(org.UID, "seam-parity-2", "http")
	r.NoError(s.CreateCheck(ctx, second))

	anchor := time.Now().UTC().Truncate(time.Hour).Add(-6 * time.Hour)

	rows := seedResponseTimeBinFixture(ctx, t, s.CreateResult, org.UID, first.UID, anchor)
	rows = append(rows,
		seedResponseTimeBinFixture(ctx, t, s.CreateResult, org.UID, second.UID,
			anchor.Add(-37*time.Minute))...)

	since := anchor.Add(-2 * time.Hour)

	// Every width seamBinWidth can return, so the grid is exercised at the finest
	// and the coarsest end.
	for _, width := range []time.Duration{
		time.Minute, 2 * time.Minute, 5 * time.Minute, 10 * time.Minute,
		15 * time.Minute, 30 * time.Minute, time.Hour,
	} {
		filter := &models.ResponseTimeBinFilter{
			OrganizationUID: org.UID,
			CheckUIDs:       []string{first.UID, second.UID},
			Since:           since,
			BinDuration:     width,
		}

		want := referenceResponseTimeBins(rows, filter)
		r.NotEmpty(want, "the fixture must produce bins at %s", width)

		got, err := s.AggregateResponseTimeBins(ctx, filter)
		r.NoError(err)

		requireSameResponseTimeBins(t, want, got, filter, width.String())
	}

	// The window bound really bites: the -7 h probe is outside it, so no bin
	// carries its 77 ms.
	filter := &models.ResponseTimeBinFilter{
		OrganizationUID: org.UID,
		CheckUIDs:       []string{first.UID},
		Since:           since,
		BinDuration:     time.Hour,
	}

	got, err := s.AggregateResponseTimeBins(ctx, filter)
	r.NoError(err)

	for i := range got {
		r.False(got[i].BinStart.Before(since.Truncate(time.Hour)),
			"a bin older than Since came back: %s", got[i].BinStart)
	}

	// The filter guards hold at the dialect boundary, not only in models.
	bad, err := s.AggregateResponseTimeBins(ctx, &models.ResponseTimeBinFilter{
		OrganizationUID: org.UID,
		CheckUIDs:       []string{first.UID},
		Since:           since,
	})
	r.ErrorIs(err, models.ErrResponseTimeBinsNoBinDuration)
	r.Nil(bad)
}

// TestAggregateResponseTimeBinsNearestRankP95_Postgres is the interpolation negative
// control. The dense bin holds 10, 20, … 400 ms; nearest-rank picks the 39th
// smallest sample (390 ms) while an interpolating percentile — Postgres'
// percentile_cont, the obvious thing to reach for — answers 395 ms for the same
// set. A test that only compared the aggregate against itself would pass either
// way, and the chart would visibly step every time the aggregation job replaced a
// seam bin with an hour rollup.
//
//nolint:paralleltest // embedded-postgres tests run sequentially in this package
func TestAggregateResponseTimeBinsNearestRankP95_Postgres(t *testing.T) {
	ctx := t.Context()
	r := require.New(t)

	s := newTier1ServicePG(t, portResponseTimeBinP95)

	org := models.NewOrganization("seam-p95-org", "Seam P95 Org")
	r.NoError(s.CreateOrganization(ctx, org))

	check := models.NewCheck(org.UID, "seam-p95", "http")
	r.NoError(s.CreateCheck(ctx, check))

	anchor := time.Now().UTC().Truncate(time.Hour).Add(-2 * time.Hour)
	seedResponseTimeBinFixture(ctx, t, s.CreateResult, org.UID, check.UID, anchor)

	bins, err := s.AggregateResponseTimeBins(ctx, &models.ResponseTimeBinFilter{
		OrganizationUID: org.UID,
		CheckUIDs:       []string{check.UID},
		Since:           anchor.Add(-time.Hour),
		BinDuration:     time.Minute,
	})
	r.NoError(err)

	var dense *models.ResponseTimeBin

	for i := range bins {
		if bins[i].Total == 40 {
			dense = &bins[i]
		}
	}

	r.NotNil(dense, "the 40-probe bin must be in the output")
	r.NotNil(dense.DurationP95)
	r.InDelta(390.0, *dense.DurationP95, 0.001,
		"nearest-rank p95 of 10..400 is 390; 395 means the SQL interpolated")
	r.InDelta(205.0, *dense.DurationAvg, 0.01)
	r.InDelta(10.0, *dense.DurationMin, 0.001)
	r.InDelta(400.0, *dense.DurationMax, 0.001)
	r.Equal(map[int]int{int(models.ResultStatusUp): 40}, dense.StatusCounts)
}

// explainResponseTimeBins returns the Postgres plan for the statement
// AggregateResponseTimeBins would run for this filter, built by the PRODUCTION
// builder with the production arguments, so the plan cannot drift from production
// by transcription.
func explainResponseTimeBins(
	ctx context.Context, t *testing.T, s *Service, filter *models.ResponseTimeBinFilter,
) string {
	t.Helper()

	require.NoError(t, filter.Validate())

	query, args := responseTimeBinsSQL(filter)

	return explainSQL(ctx, t, s, s.db.NewRaw(query, args...).String())
}

// TestResponseTimeBinsUseIndexes_Postgres is this spec's plan regression.
//
// The seam this aggregate replaces was the raw branch of the response-time chart's
// LATERAL fetch: it rode results_raw_idx correctly, and still shipped ~1 337 rows
// per check into Go (292 843 on the measured 200-check, 7-day page) so that Go
// could throw away eighty-nine out of ninety. So the assertions here are about two
// separate properties, and only the second one is new:
//
//   - the statement still rides results_raw_idx, and never sequentially scans
//     `results`. The restated `period_type = 'raw'` is what buys that; both useful
//     indexes on the table are partial on that predicate, and an IN list alone does
//     not imply it on every engine;
//   - what reaches Go is one row per (check, region, bin) — bounded by
//     checks x regions x bins — and the aggregate never spills a sort to disk.
//
// The positive control at the end runs the ROW-shaped read of the same seam over
// the same data and requires that it does ship a row per probe, so the bound above
// is a property of the aggregate and not of a thin fixture.
//
//nolint:paralleltest // embedded-postgres tests run sequentially in this package
func TestResponseTimeBinsUseIndexes_Postgres(t *testing.T) {
	ctx := t.Context()
	r := require.New(t)

	s := newTier1ServicePG(t, portResponseTimeBinPlan)

	org := models.NewOrganization("seam-plan-org", "Seam Plan Org")
	r.NoError(s.CreateOrganization(ctx, org))

	target := models.NewCheck(org.UID, "seam-plan-target", "http")
	r.NoError(s.CreateCheck(ctx, target))

	// A day of raw at a 10 s cadence (8 640 probes) plus 30 days of rollups, so the
	// aggregate has a realistic seam to fold and the rollup tier exists to be
	// excluded.
	seedPlanRaw(ctx, t, s, org.UID, target.UID, 8_640)
	seedPlanRollups(ctx, t, s, org.UID, target.UID, 30)

	// Noise: nine more checks at the same volume, so the target's rows are ~10 % of
	// the table and an index scan is the planner's genuine choice.
	for i := range 9 {
		noise := models.NewCheck(org.UID, fmt.Sprintf("seam-plan-noise-%d", i), "http")
		r.NoError(s.CreateCheck(ctx, noise))
		seedPlanRaw(ctx, t, s, org.UID, noise.UID, 8_640)
		seedPlanRollups(ctx, t, s, org.UID, noise.UID, 30)
	}

	_, err := s.db.ExecContext(ctx, "ANALYZE results")
	r.NoError(err)

	now := time.Now().UTC()
	since := now.Add(-26 * time.Hour) // uptimebar.RawTierStart's default clamp.

	for _, width := range []time.Duration{15 * time.Minute, time.Hour} {
		filter := &models.ResponseTimeBinFilter{
			OrganizationUID: org.UID,
			CheckUIDs:       []string{target.UID},
			Since:           since,
			BinDuration:     width,
		}

		plan := explainResponseTimeBins(ctx, t, s, filter)
		name := "seam bins at " + width.String()

		r.NotContains(plan, "Seq Scan on results",
			"%s must not sequentially scan results (plan:\n%s)", name, plan)
		r.NotContains(plan, "Parallel Seq Scan on results",
			"%s must not parallel-seq-scan results either (plan:\n%s)", name, plan)
		r.Contains(plan, "results_raw_idx",
			"%s must ride results_raw_idx — that is what the restated period_type predicate "+
				"buys (plan:\n%s)", name, plan)

		// Never a sort to disk. The row path this replaces inherited
		// applyResultsFilter's `ORDER BY period_start DESC, uid DESC` and the planner
		// answered it with an external merge; a bin fold that gave that back would
		// have bought nothing.
		r.NotContains(plan, "Sort Method: external",
			"%s must never sort to disk (plan:\n%s)", name, plan)

		// The headline: one row per (check, region, bin). One check, one region, a
		// 26 h window — 105 bins at 15 min, 27 at 1 h. The bound is generous on
		// purpose; what it excludes is the 8 640-row shape.
		r.LessOrEqual(planActualRows(plan), 110,
			"%s must return one row per (check, region, bin), not one per probe "+
				"(plan:\n%s)", name, plan)
	}

	// Positive control: the ROW-shaped seam read — the raw branch this spec deletes
	// from the response-time fetch — over the same data. It must ship a row per
	// probe, which is what makes the bound above meaningful.
	rowPath := &models.ListResultsFilter{
		OrganizationUID:  org.UID,
		CheckUIDs:        []string{target.UID},
		PeriodTypes:      []string{models.PeriodTypeRaw},
		PeriodStartAfter: &since,
		SkipBlobs:        true,
	}

	controlPlan := explainListResults(ctx, t, s, rowPath)
	r.Greater(planActualRows(controlPlan), 5_000,
		"the row path MUST ship thousands of rows for the same window — otherwise the "+
			"row-count bound above proves nothing about the aggregate (plan:\n%s)", controlPlan)
}
