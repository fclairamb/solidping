package availability

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/uptimebar"
)

// TestPlanBuckets covers the geometry rules the strip depends on: the 1-hour
// floor, hour-multiple validation, outward alignment and the cell cap.
func TestPlanBuckets(t *testing.T) {
	t.Parallel()

	// A deliberately UNALIGNED window start (14:23) — the whole point of the
	// alignment rule is that a request like this still produces cells whose
	// edges are whole hours.
	base := time.Date(2026, 8, 26, 14, 23, 17, 0, time.UTC)

	tests := []struct {
		name      string
		from      time.Time
		to        time.Time
		requested time.Duration
		wantWidth time.Duration
		wantStart time.Time
		wantCount int
		wantErr   bool
	}{
		{
			name:      "day view: 24h window at 1h yields 24 aligned cells",
			from:      base.Add(-24 * time.Hour),
			to:        base,
			requested: time.Hour,
			wantWidth: time.Hour,
			wantStart: time.Date(2026, 8, 25, 14, 0, 0, 0, time.UTC),
			// 25th 14:00 → 26th 14:23 spans 24h23m, which needs 25 hourly cells
			// to cover: alignment is OUTWARD, never truncating the requested edge.
			wantCount: 25,
		},
		{
			name:      "week view: 7d window at 6h yields 28 cells",
			from:      time.Date(2026, 8, 20, 0, 0, 0, 0, time.UTC),
			to:        time.Date(2026, 8, 27, 0, 0, 0, 0, time.UTC),
			requested: 6 * time.Hour,
			wantWidth: 6 * time.Hour,
			wantStart: time.Date(2026, 8, 20, 0, 0, 0, 0, time.UTC),
			wantCount: 28,
		},
		{
			name:      "month view: 30d window at 1d yields 30 cells",
			from:      time.Date(2026, 7, 28, 0, 0, 0, 0, time.UTC),
			to:        time.Date(2026, 8, 27, 0, 0, 0, 0, time.UTC),
			requested: 24 * time.Hour,
			wantWidth: 24 * time.Hour,
			wantStart: time.Date(2026, 7, 28, 0, 0, 0, 0, time.UTC),
			wantCount: 30,
		},
		{
			name:      "auto: a 1h zoom falls back to the 1h floor, never finer",
			from:      base.Add(-time.Hour),
			to:        base,
			wantWidth: time.Hour,
			wantStart: time.Date(2026, 8, 26, 13, 0, 0, 0, time.UTC),
			wantCount: 2,
		},
		{
			name: "auto: a 90d zoom widens until the cell count fits 60",
			from: time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC),
			to:   time.Date(2026, 8, 30, 0, 0, 0, 0, time.UTC),
			// 90d = 2160h; ceil(2160/60) = 36h, and the aligned count lands on
			// exactly 60 — the promised ceiling, not one past it.
			wantWidth: 36 * time.Hour,
			wantStart: time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC).Truncate(36 * time.Hour),
			wantCount: 60,
		},
		{
			name:      "a sub-hour bucket is rejected — the 1h floor is hard",
			from:      base.Add(-time.Hour),
			to:        base,
			requested: 5 * time.Minute,
			wantErr:   true,
		},
		{
			name:      "a bucket that is not a whole hour multiple is rejected",
			from:      base.Add(-24 * time.Hour),
			to:        base,
			requested: 90 * time.Minute,
			wantErr:   true,
		},
		{
			name:      "an inverted window is rejected",
			from:      base,
			to:        base.Add(-time.Hour),
			requested: time.Hour,
			wantErr:   true,
		},
		{
			name:      "an hourly read of a year blows the cell cap rather than truncating",
			from:      base.Add(-365 * 24 * time.Hour),
			to:        base,
			requested: time.Hour,
			wantErr:   true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			r := require.New(t)

			plan, err := planBuckets(tc.from, tc.to, tc.requested)
			if tc.wantErr {
				r.Error(err)
				r.ErrorIs(err, ErrInvalidWindow)

				return
			}

			r.NoError(err)
			r.Equal(tc.wantWidth, plan.width)
			r.Equal(tc.wantStart, plan.start)
			r.Equal(tc.wantCount, plan.count)
			// The invariant that matters is the ENGINE's: BucketAvailability keys
			// every row on periodStart.Truncate(width), so a cell start must be a
			// fixed point of exactly that operation (Go truncates relative to the
			// zero time, not the Unix epoch — asserting modulo epoch would be a
			// different, wrong invariant).
			r.True(plan.start.Equal(plan.start.Truncate(plan.width)),
				"the series must start on a bucket boundary the engine agrees with")
			r.False(plan.start.After(tc.from.UTC()), "alignment must never clip the requested window")
			r.False(plan.start.Add(time.Duration(plan.count)*plan.width).Before(tc.to.UTC()),
				"the cell series must cover the whole requested window")

			if tc.requested == 0 {
				r.LessOrEqual(plan.count, autoMaxCells,
					"the auto rule promises at most 60 cells AFTER alignment, not before")
			}
		})
	}
}

// bucketsFixture spins an in-memory org + check with the auto-seeded "created"
// lifecycle marker removed, so a test only sees the rows it wrote itself.
type bucketsFixture struct {
	db    db.Service
	org   *models.Organization
	check *models.Check
	svc   *Service
}

func newBucketsFixture(t *testing.T, slug string) *bucketsFixture {
	t.Helper()

	r := require.New(t)
	ctx := t.Context()

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	org := models.NewOrganization(slug, "")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	check := models.NewCheck(org.UID, slug+"-check", "domain")
	r.NoError(dbSvc.CreateCheck(ctx, check))

	seeded, err := dbSvc.ListResults(ctx, &models.ListResultsFilter{
		OrganizationUID: org.UID,
		CheckUIDs:       []string{check.UID},
		Limit:           10,
	})
	r.NoError(err)

	uids := make([]string, 0, len(seeded.Results))
	for _, row := range seeded.Results {
		uids = append(uids, row.UID)
	}

	if len(uids) > 0 {
		_, delErr := dbSvc.DeleteResults(ctx, org.UID, uids)
		r.NoError(delErr)
	}

	return &bucketsFixture{
		db: dbSvc, org: org, check: check,
		// 30 days of raw retention so a fixture can place raw rows anywhere in the
		// window without uptimebar's raw clamp deciding they cannot exist.
		svc: NewService(dbSvc, &config.Config{
			Aggregation: config.AggregationConfig{RetentionRaw: 30 * 24, RetentionHour: 60},
		}),
	}
}

// raw writes one raw probe at an exact instant, optionally region-tagged and/or
// maintenance-tagged.
func (f *bucketsFixture) raw(
	t *testing.T, at time.Time, status models.ResultStatus, region string, maintenance bool,
) {
	t.Helper()

	row := models.NewResult(f.org.UID, f.check.UID, status, 0.1)
	row.PeriodStart = at
	row.Maintenance = maintenance

	if region != "" {
		row.Region = &region
	}

	require.NoError(t, f.db.CreateResult(t.Context(), row))
}

// rollup writes one aggregated row (hour or day) with explicit counters.
func (f *bucketsFixture) rollup(
	t *testing.T, periodType string, at time.Time, total, success int, region string,
) {
	t.Helper()

	end := at.Add(time.Hour)
	if periodType == models.PeriodTypeDay {
		end = at.Add(24 * time.Hour)
	}

	row := &models.Result{
		UID:              models.NewResult(f.org.UID, f.check.UID, models.ResultStatusUp, 0).UID,
		OrganizationUID:  f.org.UID,
		CheckUID:         f.check.UID,
		PeriodType:       periodType,
		PeriodStart:      at,
		PeriodEnd:        &end,
		TotalChecks:      &total,
		SuccessfulChecks: &success,
		CreatedAt:        time.Now(),
	}

	if region != "" {
		row.Region = &region
	}

	require.NoError(t, f.db.CreateResult(t.Context(), row))
}

func (f *bucketsFixture) get(
	ctx context.Context, from, to time.Time, bucket time.Duration, region string,
) (*ListAvailabilityBucketsResponse, error) {
	return f.svc.GetAvailabilityBuckets(ctx, f.org.Slug, f.check.UID,
		&GetAvailabilityBucketsOptions{From: from, To: to, Bucket: bucket, Region: region})
}

// cellAt finds the cell whose periodStart is exactly `at`.
func cellAt(t *testing.T, resp *ListAvailabilityBucketsResponse, at time.Time) Bucket {
	t.Helper()

	for _, cell := range resp.Data {
		if cell.PeriodStart.Equal(at.UTC()) {
			return cell
		}
	}

	t.Fatalf("no bucket starting at %s in %d cells (first %s)", at, len(resp.Data), resp.Data[0].PeriodStart)

	return Bucket{}
}

// TestGetAvailabilityBuckets_BoundaryAlignmentAndNoData pins the two rules the
// strip is built on: a probe lands in the hour that contains it (never the
// neighboring cell), and an hour with no probes at all is a distinct no-data
// state — NOT 100%.
func TestGetAvailabilityBuckets_BoundaryAlignmentAndNoData(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := t.Context()

	f := newBucketsFixture(t, "bkt-align")

	now := time.Now().UTC().Truncate(time.Hour)
	// Three consecutive hours. The middle one is left completely empty.
	h0 := now.Add(-3 * time.Hour)
	h1 := now.Add(-2 * time.Hour)
	h2 := now.Add(-1 * time.Hour)

	// h0: one up at the very first second, one down at the very last second —
	// both must land in h0, not spill into h1.
	f.raw(t, h0, models.ResultStatusUp, "", false)
	f.raw(t, h0.Add(59*time.Minute+59*time.Second), models.ResultStatusDown, "", false)
	// h2: a single up probe one second after the boundary.
	f.raw(t, h2.Add(time.Second), models.ResultStatusUp, "", false)

	resp, err := f.get(ctx, h0, now, time.Hour, "")
	r.NoError(err)
	r.Equal(3600, resp.BucketSeconds)
	r.Len(resp.Data, 3)

	first := cellAt(t, resp, h0)
	r.True(first.HasData)
	r.Equal(2, first.TotalChecks, "both edge probes belong to the hour that contains them")
	r.Equal(1, first.SuccessfulChecks)
	r.InDelta(50.0, *first.AvailabilityPct, 0.001)
	// 50% is far below the degraded threshold, but the cell carries exactly ONE
	// failed sample, and the shared small-bucket guard never paints a single
	// failure red — the same rule the status page applies.
	r.Equal(uptimebar.StatusDegraded, first.Status)

	empty := cellAt(t, resp, h1)
	r.False(empty.HasData, "an hour with no probes has no data")
	r.Nil(empty.AvailabilityPct, "no data must be null, never 0 and never 100")
	r.Equal(uptimebar.StatusNoData, empty.Status)
	r.Zero(empty.TotalChecks)

	last := cellAt(t, resp, h2)
	r.True(last.HasData)
	r.Equal(1, last.TotalChecks)
	r.InDelta(100.0, *last.AvailabilityPct, 0.001)
	r.Equal(uptimebar.StatusUp, last.Status)

	// The window fold covers all three hours at once: 2 up out of 3.
	r.True(resp.Window.HasData)
	r.Equal(3, resp.Window.TotalChecks)
	r.Equal(2, resp.Window.SuccessfulChecks)
}

// TestGetAvailabilityBuckets_TierSeam mixes raw, hour and day rows inside ONE
// window and asserts every tier reaches its own cell. A strip that read only one
// tier would silently show gray for two-thirds of the window.
func TestGetAvailabilityBuckets_TierSeam(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := t.Context()

	f := newBucketsFixture(t, "bkt-seam")

	dayStart := time.Now().UTC().Truncate(24 * time.Hour)
	// Three daily cells: the oldest fed by a `day` rollup, the middle by `hour`
	// rollups, the newest by raw probes (the seam the aggregator has not reached).
	oldDay := dayStart.Add(-48 * time.Hour)
	midDay := dayStart.Add(-24 * time.Hour)

	f.rollup(t, models.PeriodTypeDay, oldDay, 100, 90, "")
	f.rollup(t, models.PeriodTypeHour, midDay.Add(time.Hour), 60, 60, "")
	f.rollup(t, models.PeriodTypeHour, midDay.Add(2*time.Hour), 60, 30, "")
	f.raw(t, dayStart.Add(time.Minute), models.ResultStatusUp, "", false)
	f.raw(t, dayStart.Add(2*time.Minute), models.ResultStatusDown, "", false)

	resp, err := f.get(ctx, oldDay, dayStart.Add(24*time.Hour), 24*time.Hour, "")
	r.NoError(err)
	r.Len(resp.Data, 3)

	fromDay := cellAt(t, resp, oldDay)
	r.True(fromDay.HasData, "the day tier must reach its cell")
	r.Equal(100, fromDay.TotalChecks)
	r.InDelta(90.0, *fromDay.AvailabilityPct, 0.001)

	fromHour := cellAt(t, resp, midDay)
	r.True(fromHour.HasData, "hour rollups must fold into the daily cell containing them")
	r.Equal(120, fromHour.TotalChecks)
	r.Equal(90, fromHour.SuccessfulChecks)
	r.InDelta(75.0, *fromHour.AvailabilityPct, 0.001)

	fromRaw := cellAt(t, resp, dayStart)
	r.True(fromRaw.HasData, "raw at the seam must not read as no-data")
	r.Equal(2, fromRaw.TotalChecks)
	r.InDelta(50.0, *fromRaw.AvailabilityPct, 0.001)

	// And the whole-window fold sees all three tiers: 90+90+1 up of 100+120+2.
	r.Equal(222, resp.Window.TotalChecks)
	r.Equal(181, resp.Window.SuccessfulChecks)
}

// TestGetAvailabilityBuckets_Regions covers both halves of the region rule: with
// no filter the engine SUMS up/total across regions (never averages their
// percentages), and with a filter it buckets that region alone.
//
// The fixture is chosen so the two rules give different answers: eu has 1 probe
// (down), us has 3 (all up). Averaging the two regions' percentages would give
// 50%; summing gives 75%. Only one of those can pass.
func TestGetAvailabilityBuckets_Regions(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := t.Context()

	f := newBucketsFixture(t, "bkt-regions")

	hour := time.Now().UTC().Truncate(time.Hour).Add(-time.Hour)

	f.raw(t, hour.Add(time.Minute), models.ResultStatusDown, "eu-1", false)
	f.raw(t, hour.Add(2*time.Minute), models.ResultStatusUp, "us-1", false)
	f.raw(t, hour.Add(3*time.Minute), models.ResultStatusUp, "us-1", false)
	f.raw(t, hour.Add(4*time.Minute), models.ResultStatusUp, "us-1", false)

	all, err := f.get(ctx, hour, hour.Add(time.Hour), time.Hour, "")
	r.NoError(err)
	r.Len(all.Data, 1)
	r.Equal(4, all.Data[0].TotalChecks)
	r.Equal(3, all.Data[0].SuccessfulChecks)
	r.InDelta(75.0, *all.Data[0].AvailabilityPct, 0.001,
		"all-regions must sum up/total (75%), not average the per-region percentages (50%)")

	eu, err := f.get(ctx, hour, hour.Add(time.Hour), time.Hour, "eu-1")
	r.NoError(err)
	r.Len(eu.Data, 1)
	r.Equal("eu-1", eu.Region)
	r.Equal(1, eu.Data[0].TotalChecks, "the region filter must scope the cell to that region alone")
	r.InDelta(0.0, *eu.Data[0].AvailabilityPct, 0.001)
	r.InDelta(0.0, *eu.Window.AvailabilityPct, 0.001, "the header figure must follow the same filter")

	us, err := f.get(ctx, hour, hour.Add(time.Hour), time.Hour, "us-1")
	r.NoError(err)
	r.Equal(3, us.Data[0].TotalChecks)
	r.InDelta(100.0, *us.Data[0].AvailabilityPct, 0.001)

	// Positive control that the filter is a real query filter and not a no-op:
	// the three answers must differ from each other.
	r.NotEqual(*all.Data[0].AvailabilityPct, *eu.Data[0].AvailabilityPct)
	r.NotEqual(*all.Data[0].AvailabilityPct, *us.Data[0].AvailabilityPct)
}

// TestGetAvailabilityBuckets_RegionFilterReadsRollupsToo is the region rule's
// second positive control: rollup rows carry their region, so a region-scoped
// read must reach them. If the filter were applied to raw only (or after the
// fetch), this cell would read as the SUM of both regions.
func TestGetAvailabilityBuckets_RegionFilterReadsRollupsToo(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := t.Context()

	f := newBucketsFixture(t, "bkt-rgn-rollup")

	hour := time.Now().UTC().Truncate(time.Hour).Add(-2 * time.Hour)

	f.rollup(t, models.PeriodTypeHour, hour, 60, 30, "eu-1")
	f.rollup(t, models.PeriodTypeHour, hour, 60, 60, "us-1")

	all, err := f.get(ctx, hour, hour.Add(time.Hour), time.Hour, "")
	r.NoError(err)
	r.Equal(120, all.Data[0].TotalChecks)
	r.InDelta(75.0, *all.Data[0].AvailabilityPct, 0.001)

	eu, err := f.get(ctx, hour, hour.Add(time.Hour), time.Hour, "eu-1")
	r.NoError(err)
	r.Equal(60, eu.Data[0].TotalChecks, "a region-scoped read must see the region's ROLLUP rows")
	r.InDelta(50.0, *eu.Data[0].AvailabilityPct, 0.001)
}

// TestGetAvailabilityBuckets_MaintenanceCounted pins the decided semantics:
// maintenance probes count exactly like any other probe, matching the
// availability table sitting right below the strip. The positive control is the
// pair — the maintenance-tagged failure moves the number just as much as an
// untagged one would.
func TestGetAvailabilityBuckets_MaintenanceCounted(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := t.Context()

	tagged := newBucketsFixture(t, "bkt-maint-on")
	untagged := newBucketsFixture(t, "bkt-maint-off")

	hour := time.Now().UTC().Truncate(time.Hour).Add(-time.Hour)

	for _, f := range []*bucketsFixture{tagged, untagged} {
		maintenance := f == tagged

		f.raw(t, hour.Add(time.Minute), models.ResultStatusUp, "", false)
		f.raw(t, hour.Add(2*time.Minute), models.ResultStatusUp, "", false)
		f.raw(t, hour.Add(3*time.Minute), models.ResultStatusUp, "", false)
		// The one failure: maintenance-tagged on one fixture, plain on the other.
		f.raw(t, hour.Add(4*time.Minute), models.ResultStatusDown, "", maintenance)
	}

	withMaint, err := tagged.get(ctx, hour, hour.Add(time.Hour), time.Hour, "")
	r.NoError(err)

	without, err := untagged.get(ctx, hour, hour.Add(time.Hour), time.Hour, "")
	r.NoError(err)

	r.Equal(4, withMaint.Data[0].TotalChecks,
		"a maintenance probe stays in the denominator — exclusion is SLO-only")
	r.InDelta(75.0, *withMaint.Data[0].AvailabilityPct, 0.001)
	r.InDelta(*without.Data[0].AvailabilityPct, *withMaint.Data[0].AvailabilityPct, 0.001,
		"tagging a probe as maintenance must change NOTHING on this surface")
}

// TestGetAvailabilityBuckets_LifecycleAndAbandonedExcluded is the exclusion
// rule's positive control: `created`/`running` markers and reaped `abandoned`
// attempts leave BOTH numerator and denominator untouched, while a genuine
// `down` in the same cell moves the number. A test that only asserted the
// exclusion would pass whether or not the rule was applied — so the same cell
// carries a real failure to prove the counting still works.
func TestGetAvailabilityBuckets_LifecycleAndAbandonedExcluded(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := t.Context()

	f := newBucketsFixture(t, "bkt-lifecycle")

	hour := time.Now().UTC().Truncate(time.Hour).Add(-time.Hour)

	// Countable: 3 up.
	f.raw(t, hour.Add(time.Minute), models.ResultStatusUp, "", false)
	f.raw(t, hour.Add(2*time.Minute), models.ResultStatusUp, "", false)
	f.raw(t, hour.Add(3*time.Minute), models.ResultStatusUp, "", false)
	// Not countable: lifecycle markers + a reaped attempt. Six rows that must
	// leave the ratio at exactly 3/3.
	f.raw(t, hour.Add(4*time.Minute), models.ResultStatusCreated, "", false)
	f.raw(t, hour.Add(5*time.Minute), models.ResultStatusRunning, "", false)
	f.raw(t, hour.Add(6*time.Minute), models.ResultStatusAbandoned, "", false)
	f.raw(t, hour.Add(7*time.Minute), models.ResultStatusAbandoned, "", false)
	f.raw(t, hour.Add(8*time.Minute), models.ResultStatusAbandoned, "", false)
	f.raw(t, hour.Add(9*time.Minute), models.ResultStatusAbandoned, "", false)

	resp, err := f.get(ctx, hour, hour.Add(time.Hour), time.Hour, "")
	r.NoError(err)
	r.Equal(3, resp.Data[0].TotalChecks,
		"lifecycle markers and abandoned attempts leave the denominator alone")
	r.InDelta(100.0, *resp.Data[0].AvailabilityPct, 0.001)
	r.Equal(uptimebar.StatusUp, resp.Data[0].Status)

	// Positive control: swap ONE of those uncountable rows for a genuine failure
	// pair and the very same cell moves. Two failures, not one, because the
	// small-bucket guard keeps a single failed sample out of red.
	f.raw(t, hour.Add(10*time.Minute), models.ResultStatusDown, "", false)
	f.raw(t, hour.Add(11*time.Minute), models.ResultStatusDown, "", false)

	after, err := f.get(ctx, hour, hour.Add(time.Hour), time.Hour, "")
	r.NoError(err)
	r.Equal(5, after.Data[0].TotalChecks, "a genuine failure DOES enter the denominator")
	r.InDelta(60.0, *after.Data[0].AvailabilityPct, 0.001)
	r.Equal(uptimebar.StatusDown, after.Data[0].Status)
}

// TestGetAvailabilityBuckets_WarningCountsAsUp pins the last counting rule the
// strip inherits, with its positive control (a `down` in the same shape is not
// counted as up).
func TestGetAvailabilityBuckets_WarningCountsAsUp(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := t.Context()

	warned := newBucketsFixture(t, "bkt-warn")
	downed := newBucketsFixture(t, "bkt-warn-ctl")

	hour := time.Now().UTC().Truncate(time.Hour).Add(-time.Hour)

	warned.raw(t, hour.Add(time.Minute), models.ResultStatusWarning, "", false)
	warned.raw(t, hour.Add(2*time.Minute), models.ResultStatusWarning, "", false)
	downed.raw(t, hour.Add(time.Minute), models.ResultStatusDown, "", false)
	downed.raw(t, hour.Add(2*time.Minute), models.ResultStatusDown, "", false)

	warnResp, err := warned.get(ctx, hour, hour.Add(time.Hour), time.Hour, "")
	r.NoError(err)
	r.InDelta(100.0, *warnResp.Data[0].AvailabilityPct, 0.001, "warning counts as up")

	downResp, err := downed.get(ctx, hour, hour.Add(time.Hour), time.Hour, "")
	r.NoError(err)
	r.InDelta(0.0, *downResp.Data[0].AvailabilityPct, 0.001)
}

// TestGetAvailabilityBuckets_WindowFoldIsExactNotTheCellSum is why the response
// carries `window` from its own engine call. On an unaligned short window the
// cells span MORE time than was asked for, so summing them would answer a
// different question than the header stat is meant to answer.
func TestGetAvailabilityBuckets_WindowFoldIsExactNotTheCellSum(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := t.Context()

	f := newBucketsFixture(t, "bkt-fold")

	hour := time.Now().UTC().Truncate(time.Hour).Add(-3 * time.Hour)
	// A window from 20 minutes into the hour to 20 minutes into the next one.
	from := hour.Add(20 * time.Minute)
	to := hour.Add(80 * time.Minute)

	// Inside the requested window: 1 up.
	f.raw(t, hour.Add(30*time.Minute), models.ResultStatusUp, "", false)
	// Outside it, but inside the aligned cells: 2 down, before `from`.
	f.raw(t, hour.Add(time.Minute), models.ResultStatusDown, "", false)
	f.raw(t, hour.Add(2*time.Minute), models.ResultStatusDown, "", false)

	resp, err := f.get(ctx, from, to, time.Hour, "")
	r.NoError(err)
	r.Len(resp.Data, 2, "the cells cover [hour, hour+2h) — outward alignment")

	cellSum := 0
	for _, cell := range resp.Data {
		cellSum += cell.TotalChecks
	}

	r.Equal(3, cellSum, "the cells legitimately include probes outside the requested window")
	r.Equal(1, resp.Window.TotalChecks,
		"the header figure must answer exactly [from, to), not the aligned cell span")
	r.InDelta(100.0, *resp.Window.AvailabilityPct, 0.001)
	r.True(resp.WindowStart.Equal(from))
	r.True(resp.WindowEnd.Equal(to))
}

// TestGetAvailabilityBuckets_NoDataPastRetention covers the long-window tail: a
// month-tier row exists but is deliberately NOT attributable to a single daily
// cell, so those cells stay gray while the whole-window fold still sees it.
func TestGetAvailabilityBuckets_NoDataPastRetention(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := t.Context()

	f := newBucketsFixture(t, "bkt-tail")

	monthStart := time.Now().UTC().Truncate(24 * time.Hour).Add(-40 * 24 * time.Hour)

	total, success := 40000, 39000
	end := monthStart.Add(30 * 24 * time.Hour)
	row := &models.Result{
		UID:              models.NewResult(f.org.UID, f.check.UID, models.ResultStatusUp, 0).UID,
		OrganizationUID:  f.org.UID,
		CheckUID:         f.check.UID,
		PeriodType:       models.PeriodTypeMonth,
		PeriodStart:      monthStart,
		PeriodEnd:        &end,
		TotalChecks:      &total,
		SuccessfulChecks: &success,
		CreatedAt:        time.Now(),
	}
	r.NoError(f.db.CreateResult(ctx, row))

	resp, err := f.get(ctx, monthStart, monthStart.Add(5*24*time.Hour), 24*time.Hour, "")
	r.NoError(err)
	r.Len(resp.Data, 5)

	for _, cell := range resp.Data {
		r.False(cell.HasData,
			"a month rollup spans many cells and must not be dumped into the one at its period_start")
		r.Equal(uptimebar.StatusNoData, cell.Status)
		r.Nil(cell.AvailabilityPct)
	}

	r.True(resp.Window.HasData, "the whole-window fold DOES include the month tier")
	r.Equal(40000, resp.Window.TotalChecks)
}

// TestGetAvailabilityBuckets_Validation covers the handler-facing error surface.
func TestGetAvailabilityBuckets_Validation(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := t.Context()

	f := newBucketsFixture(t, "bkt-valid")

	now := time.Now().UTC().Truncate(time.Hour)

	_, err := f.get(ctx, now, now, time.Hour, "")
	r.ErrorIs(err, ErrInvalidWindow, "a zero-length window is rejected")

	_, err = f.get(ctx, now.Add(-time.Hour), now, time.Minute, "")
	r.ErrorIs(err, ErrInvalidWindow, "a sub-hour bucket is rejected")

	_, err = f.svc.GetAvailabilityBuckets(ctx, "no-such-org", f.check.UID,
		&GetAvailabilityBucketsOptions{From: now.Add(-time.Hour), To: now, Bucket: time.Hour})
	r.ErrorIs(err, ErrOrganizationNotFound)

	_, err = f.svc.GetAvailabilityBuckets(ctx, f.org.Slug, "no-such-check",
		&GetAvailabilityBucketsOptions{From: now.Add(-time.Hour), To: now, Bucket: time.Hour})
	r.ErrorIs(err, ErrCheckNotFound)
}
