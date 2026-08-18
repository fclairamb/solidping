package availability

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/systemconfig"
	"github.com/fclairamb/solidping/server/internal/uptimebar"
)

// TestParseDurationToken covers the trailing-duration parser that time.ParseDuration
// can't handle (d/w/y suffixes) plus rejection of garbage tokens.
func TestParseDurationToken(t *testing.T) {
	t.Parallel()

	tests := []struct {
		token   string
		want    time.Duration
		wantErr bool
	}{
		{"24h", 24 * time.Hour, false},
		{"7d", 7 * 24 * time.Hour, false},
		{"30d", 30 * 24 * time.Hour, false},
		{"90d", 90 * 24 * time.Hour, false},
		{"365d", 365 * 24 * time.Hour, false},
		{"1y", 365 * 24 * time.Hour, false},
		{"2w", 14 * 24 * time.Hour, false},
		{"5x", 0, true},
		{"d", 0, true},
		{"0d", 0, true},
		{"-3d", 0, true},
		{"abc", 0, true},
		{"", 0, true},
		// Overflows int64 nanoseconds and would wrap into a garbage duration;
		// the parser must reject it rather than hand back a wrapped value.
		{"9999999999999d", 0, true},
	}

	for _, tc := range tests {
		t.Run(tc.token, func(t *testing.T) {
			t.Parallel()

			r := require.New(t)

			got, err := parseDurationToken(tc.token)
			if tc.wantErr {
				r.Error(err)
				r.ErrorIs(err, ErrInvalidPeriod)

				return
			}

			r.NoError(err)
			r.Equal(tc.want, got)
		})
	}
}

// TestParsePeriods covers the full token set, calendar tokens against a tz, and
// the validation rules (empty list, too many, unknown token, sanity lookback cap).
func TestParsePeriods(t *testing.T) {
	t.Parallel()

	// A fixed "now" so calendar-token assertions are deterministic.
	now := time.Date(2026, 6, 30, 15, 30, 0, 0, time.UTC)

	t.Run("trailing durations resolve to now minus the duration", func(t *testing.T) {
		t.Parallel()

		r := require.New(t)

		windows, err := parsePeriods([]string{"24h", "7d", "365d", "1y"}, now, time.UTC)
		r.NoError(err)
		r.Len(windows, 4)
		r.True(windows[0].start.Equal(now.Add(-24 * time.Hour)))
		r.True(windows[1].start.Equal(now.Add(-7 * 24 * time.Hour)))
		r.True(windows[2].start.Equal(now.Add(-365 * 24 * time.Hour)))
		r.True(windows[3].start.Equal(now.Add(-365 * 24 * time.Hour)))

		for _, w := range windows {
			r.True(w.end.Equal(now), "window end is always now")
		}
	})

	t.Run("today/mtd/ytd resolve against UTC", func(t *testing.T) {
		t.Parallel()

		r := require.New(t)

		windows, err := parsePeriods([]string{"today", "mtd", "ytd"}, now, time.UTC)
		r.NoError(err)
		r.True(windows[0].start.Equal(time.Date(2026, 6, 30, 0, 0, 0, 0, time.UTC)))
		r.True(windows[1].start.Equal(time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)))
		r.True(windows[2].start.Equal(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)))
	})

	t.Run("today resolves against the supplied timezone", func(t *testing.T) {
		t.Parallel()

		r := require.New(t)

		// At 15:30 UTC it is already 2026-06-30 in a +14 zone and still the same
		// calendar day; pick a zone where local midnight differs from UTC midnight.
		loc, err := time.LoadLocation("America/Los_Angeles") // UTC-7 in June
		r.NoError(err)

		windows, err := parsePeriods([]string{"today"}, now, loc)
		r.NoError(err)
		// 15:30 UTC = 08:30 local on 2026-06-30; local midnight is 07:00 UTC.
		r.True(windows[0].start.Equal(time.Date(2026, 6, 30, 7, 0, 0, 0, time.UTC)),
			"got %s", windows[0].start)
	})

	t.Run("rejects empty list", func(t *testing.T) {
		t.Parallel()

		r := require.New(t)

		_, err := parsePeriods(nil, now, time.UTC)
		r.ErrorIs(err, ErrInvalidPeriod)
	})

	t.Run("rejects too many periods", func(t *testing.T) {
		t.Parallel()

		r := require.New(t)

		many := make([]string, maxPeriods+1)
		for i := range many {
			many[i] = "24h"
		}

		_, err := parsePeriods(many, now, time.UTC)
		r.ErrorIs(err, ErrInvalidPeriod)
	})

	t.Run("rejects unknown token", func(t *testing.T) {
		t.Parallel()

		r := require.New(t)

		_, err := parsePeriods([]string{"7d", "5x"}, now, time.UTC)
		r.ErrorIs(err, ErrInvalidPeriod)
	})

	t.Run("accepts multi-year lookbacks (month tier is terminal)", func(t *testing.T) {
		t.Parallel()

		r := require.New(t)

		// The union query includes the never-deleted month tier, so windows far
		// past the day-tier retention horizon (~2 months by default) are
		// answerable. 800d used to be rejected by the old 12-month cap.
		windows, err := parsePeriods([]string{"800d", "5y"}, now, time.UTC)
		r.NoError(err)
		r.Len(windows, 2)
		r.True(windows[0].start.Equal(now.Add(-800 * 24 * time.Hour)))
		r.True(windows[1].start.Equal(now.Add(-5 * 365 * 24 * time.Hour)))
	})

	t.Run("rejects lookback beyond the sanity cap", func(t *testing.T) {
		t.Parallel()

		r := require.New(t)

		for _, token := range []string{"11y", "4000d"} {
			_, err := parsePeriods([]string{token}, now, time.UTC)
			r.ErrorIs(err, ErrInvalidPeriod, "token %q", token)
		}
	})
}

// TestResolveLocation covers the tz parameter: empty → UTC, valid IANA, invalid.
func TestResolveLocation(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	loc, err := resolveLocation("")
	r.NoError(err)
	r.Equal(time.UTC, loc)

	loc, err = resolveLocation("Europe/Paris")
	r.NoError(err)
	r.Equal("Europe/Paris", loc.String())

	_, err = resolveLocation("Not/AZone")
	r.ErrorIs(err, ErrInvalidPeriod)
}

// TestMonitoredDuration covers the monitoredSeconds cap and the partial flag for
// a check younger than the window.
func TestMonitoredDuration(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 6, 30, 12, 0, 0, 0, time.UTC)

	t.Run("older check: full window is monitored", func(t *testing.T) {
		t.Parallel()

		r := require.New(t)

		w := periodWindow{token: "7d", start: now.Add(-7 * 24 * time.Hour), end: now}
		createdAt := now.Add(-100 * 24 * time.Hour)

		got := monitoredDuration(w, createdAt, now)
		r.Equal(7*24*time.Hour, got)
	})

	t.Run("young check: capped at now − createdAt", func(t *testing.T) {
		t.Parallel()

		r := require.New(t)

		// Check created 3 days ago, asked over 365d → monitored is only 3 days.
		w := periodWindow{token: "365d", start: now.Add(-365 * 24 * time.Hour), end: now}
		createdAt := now.Add(-3 * 24 * time.Hour)

		got := monitoredDuration(w, createdAt, now)
		r.Equal(3*24*time.Hour, got)
	})
}

// TestBuildPeriodRow covers the probe-ratio row assembly: null availability /
// hasData=false for an empty window, the partial flag, and the
// downtimeSeconds == (1 − avail) × monitoredSeconds formula.
func TestBuildPeriodRow(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 6, 30, 12, 0, 0, 0, time.UTC)
	w := periodWindow{token: "7d", start: now.Add(-7 * 24 * time.Hour), end: now}

	t.Run("no data: availabilityPct nil, hasData false", func(t *testing.T) {
		t.Parallel()

		r := require.New(t)

		row := buildPeriodRow(w, uptimebar.BucketStats{}, 7*24*time.Hour, now.Add(-100*24*time.Hour))
		r.False(row.HasData)
		r.Nil(row.AvailabilityPct)
		r.Zero(row.DowntimeSeconds)
		r.False(row.Partial)
	})

	t.Run("downtimeSeconds equals (1 − avail) × monitoredSeconds", func(t *testing.T) {
		t.Parallel()

		r := require.New(t)

		// 90/100 up over a 7-day window: 10% failing → 10% of 604800s = 60480s.
		monitored := 7 * 24 * time.Hour
		row := buildPeriodRow(w, uptimebar.BucketStats{Up: 90, Total: 100}, monitored, now.Add(-100*24*time.Hour))
		r.True(row.HasData)
		r.NotNil(row.AvailabilityPct)
		r.InDelta(90.0, *row.AvailabilityPct, 0.0001)
		r.Equal(int64(60480), row.DowntimeSeconds)
		r.Equal(int64(monitored.Seconds()), row.MonitoredSeconds)
	})

	t.Run("partial flag set when check younger than window", func(t *testing.T) {
		t.Parallel()

		r := require.New(t)

		createdAt := now.Add(-3 * 24 * time.Hour) // younger than the 7d window
		row := buildPeriodRow(w, uptimebar.BucketStats{Up: 100, Total: 100}, 3*24*time.Hour, createdAt)
		r.True(row.Partial)
	})
}

// TestIncidentBlock covers the wall-clock incident stats: clamping at the window
// end and an unresolved incident running to now.
func TestIncidentBlock(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 6, 30, 12, 0, 0, 0, time.UTC)
	w := periodWindow{token: "7d", start: now.Add(-7 * 24 * time.Hour), end: now}

	incident := func(started time.Time, resolved *time.Time) *models.Incident {
		return &models.Incident{StartedAt: started, ResolvedAt: resolved}
	}

	t.Run("empty when no incidents", func(t *testing.T) {
		t.Parallel()

		r := require.New(t)

		block := incidentBlock(nil, w, now)
		r.Equal(0, block.Count)
		r.Zero(block.TotalDowntimeSeconds)
	})

	t.Run("resolved incident inside window", func(t *testing.T) {
		t.Parallel()

		r := require.New(t)

		start := now.Add(-2 * time.Hour)
		end := now.Add(-1 * time.Hour)
		block := incidentBlock([]*models.Incident{incident(start, &end)}, w, now)
		r.Equal(1, block.Count)
		r.Equal(int64(3600), block.TotalDowntimeSeconds)
		r.Equal(int64(3600), block.LongestSeconds)
		r.Equal(int64(3600), block.AverageSeconds)
	})

	t.Run("unresolved incident runs to now", func(t *testing.T) {
		t.Parallel()

		r := require.New(t)

		start := now.Add(-90 * time.Minute)
		block := incidentBlock([]*models.Incident{incident(start, nil)}, w, now)
		r.Equal(1, block.Count)
		r.Equal(int64(90*60), block.TotalDowntimeSeconds)
	})

	t.Run("incident starting before the window is ignored", func(t *testing.T) {
		t.Parallel()

		r := require.New(t)

		start := w.start.Add(-time.Hour) // started before the window opened
		end := now.Add(-1 * time.Hour)
		block := incidentBlock([]*models.Incident{incident(start, &end)}, w, now)
		r.Equal(0, block.Count, "only incidents that started in the window count")
	})

	t.Run("average and longest over multiple incidents", func(t *testing.T) {
		t.Parallel()

		r := require.New(t)

		s1, e1 := now.Add(-5*time.Hour), now.Add(-4*time.Hour)    // 1h
		s2, e2 := now.Add(-3*time.Hour), now.Add(-30*time.Minute) // 2.5h
		block := incidentBlock(
			[]*models.Incident{incident(s1, &e1), incident(s2, &e2)}, w, now,
		)
		r.Equal(2, block.Count)
		r.Equal(int64(3600+9000), block.TotalDowntimeSeconds)
		r.Equal(int64(9000), block.LongestSeconds)
		r.Equal(int64((3600+9000)/2), block.AverageSeconds)
	})
}

// TestGetAvailability_SparseLongPeriodSeries proves — rather than assumes —
// that the probe-ratio availability pipeline (GetAvailability →
// uptimebar.WindowAvailability → buildPeriodRow) is correct for a check
// whose results are spaced far apart (e.g. domain's new 336h/2-week period
// option, spec 2026-08-15-07), not just a dense series with hundreds of
// probes per window:
//   - a 30-day window holding only 2 raw rows computes the exact
//     successful/total ratio from those 2 rows, not something derived from
//     an assumed rows-per-window count;
//   - a window with ZERO probes (a check younger than any data, or a gap
//     bigger than the window) yields HasData=false / AvailabilityPct=nil —
//     never a divide-by-zero, a misleading 0%, or NaN.
func TestGetAvailability_SparseLongPeriodSeries(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := t.Context()

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	org := models.NewOrganization("avail-sparse-org", "")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	check := models.NewCheck(org.UID, "avail-sparse-check", "domain")
	r.NoError(dbSvc.CreateCheck(ctx, check))

	// Remove the auto-seeded "created" marker (status=created carries no
	// up/down signal but would otherwise sit inside our windows).
	seeded, err := dbSvc.ListResults(ctx, &models.ListResultsFilter{
		OrganizationUID: org.UID,
		CheckUIDs:       []string{check.UID},
		Limit:           10,
	})
	r.NoError(err)

	seededUIDs := make([]string, 0, len(seeded.Results))
	for _, row := range seeded.Results {
		seededUIDs = append(seededUIDs, row.UID)
	}

	if len(seededUIDs) > 0 {
		_, delErr := dbSvc.DeleteResults(ctx, org.UID, seededUIDs)
		r.NoError(delErr)
	}

	// The check's CreatedAt is "now" (set at insert time above), so both
	// windows below are Partial — irrelevant here since the assertions only
	// cover TotalChecks/SuccessfulChecks/AvailabilityPct, not MonitoredSeconds.
	now := time.Now().UTC()

	// Two raw executions ~2 weeks apart, both inside a 30-day lookback: one
	// Up, one Down.
	first := models.NewResult(org.UID, check.UID, models.ResultStatusUp, 0.1)
	first.PeriodStart = now.Add(-20 * 24 * time.Hour)
	r.NoError(dbSvc.CreateResult(ctx, first))

	second := models.NewResult(org.UID, check.UID, models.ResultStatusDown, 0.1)
	second.PeriodStart = now.Add(-6 * 24 * time.Hour)
	r.NoError(dbSvc.CreateResult(ctx, second))

	// This deployment keeps 30 days of raw (a supported configuration), so both
	// probes above are still raw and none has been rolled up. uptimebar clamps
	// its raw-tier query to the CONFIGURED raw retention, so the hint has to say
	// what the fixture assumes — with the 24h default, a 20-day-old raw row could
	// not exist at all.
	svc := NewService(dbSvc, &config.Config{
		Aggregation: config.AggregationConfig{RetentionRaw: 30 * 24, RetentionHour: 7},
	})

	t.Run("sparse window computes the exact ratio from its handful of rows", func(t *testing.T) {
		t.Parallel()

		rr := require.New(t)

		resp, availErr := svc.GetAvailability(ctx, org.Slug, check.UID, &GetAvailabilityOptions{
			Periods: []string{"30d"},
		})
		rr.NoError(availErr)
		rr.Len(resp.Data, 1)

		row := resp.Data[0]
		rr.True(row.HasData, "a window with 2 real probes must report data")
		rr.Equal(2, row.TotalChecks)
		rr.Equal(1, row.SuccessfulChecks)
		rr.NotNil(row.AvailabilityPct)
		rr.InDelta(50.0, *row.AvailabilityPct, 0.0001, "1 up out of 2 = 50%, not skewed by window length")
	})

	t.Run("empty window yields no data, never divide-by-zero or 0%", func(t *testing.T) {
		t.Parallel()

		rr := require.New(t)

		// A 1-day window predates both probes (the older is 20 days back) —
		// zero rows fall inside it.
		resp, availErr := svc.GetAvailability(ctx, org.Slug, check.UID, &GetAvailabilityOptions{
			Periods: []string{"1d"},
		})
		rr.NoError(availErr)
		rr.Len(resp.Data, 1)

		row := resp.Data[0]
		rr.False(row.HasData, "zero probes in the window must not report data")
		rr.Nil(row.AvailabilityPct, "must be nil, never 0% (0% is a real measured value) or NaN")
		rr.Equal(0, row.TotalChecks)
		rr.Zero(row.DowntimeSeconds)
	})
}

// TestGetAvailability_AbandonedExcludedGenuineErrorCounts is the availability
// service's slice of the required spec 2026-08-18-03 positive-control test: a
// row the abandoned-result reaper finalized (status=error, Abandoned=true)
// must not move the availability percentage at all, while a genuine error
// result (Abandoned=false) in the same window still drags it down. This is
// the third of the three surfaces the spec calls out by name (hour rollup,
// uptimebar union, availability service) — all three must agree, and this
// pins the service's own end of that agreement through a real DB round trip,
// not just the shared in-memory predicate.
func TestGetAvailability_AbandonedExcludedGenuineErrorCounts(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := t.Context()

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	org := models.NewOrganization("avail-abandoned-org", "")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	check := models.NewCheck(org.UID, "avail-abandoned-check", "domain")
	r.NoError(dbSvc.CreateCheck(ctx, check))

	// Remove the auto-seeded "created" marker so it can't leak into the
	// window under test.
	seeded, err := dbSvc.ListResults(ctx, &models.ListResultsFilter{
		OrganizationUID: org.UID,
		CheckUIDs:       []string{check.UID},
		Limit:           10,
	})
	r.NoError(err)

	seededUIDs := make([]string, 0, len(seeded.Results))
	for _, row := range seeded.Results {
		seededUIDs = append(seededUIDs, row.UID)
	}

	if len(seededUIDs) > 0 {
		_, delErr := dbSvc.DeleteResults(ctx, org.UID, seededUIDs)
		r.NoError(delErr)
	}

	now := time.Now().UTC()

	up := models.NewResult(org.UID, check.UID, models.ResultStatusUp, 0.1)
	up.PeriodStart = now.Add(-2 * time.Hour)
	r.NoError(dbSvc.CreateResult(ctx, up))

	abandoned := models.NewResult(org.UID, check.UID, models.ResultStatusError, 0)
	abandoned.PeriodStart = now.Add(-1 * time.Hour)
	abandoned.Abandoned = true
	r.NoError(dbSvc.CreateResult(ctx, abandoned))

	svc := NewService(dbSvc, &config.Config{
		Aggregation: config.AggregationConfig{RetentionRaw: 30 * 24, RetentionHour: 7},
	})

	resp, availErr := svc.GetAvailability(ctx, org.Slug, check.UID, &GetAvailabilityOptions{
		Periods: []string{"7d"},
	})
	r.NoError(availErr)
	r.Len(resp.Data, 1)

	row := resp.Data[0]
	r.True(row.HasData)
	r.Equal(1, row.TotalChecks, "the abandoned row must not enter total_checks")
	r.Equal(1, row.SuccessfulChecks)
	r.NotNil(row.AvailabilityPct)
	r.InDelta(100.0, *row.AvailabilityPct, 0.0001, "one up + one abandoned reads as 100%, not 50%")

	// Positive control: a genuine error (Abandoned=false) in the same window
	// must drag availability down — proving the exclusion above is specific
	// to Abandoned=true, not a bug swallowing errors in general.
	genuineErr := models.NewResult(org.UID, check.UID, models.ResultStatusError, 0)
	genuineErr.PeriodStart = now.Add(-30 * time.Minute)
	r.NoError(dbSvc.CreateResult(ctx, genuineErr))

	resp, availErr = svc.GetAvailability(ctx, org.Slug, check.UID, &GetAvailabilityOptions{
		Periods: []string{"7d"},
	})
	r.NoError(availErr)
	r.Len(resp.Data, 1)

	row = resp.Data[0]
	r.Equal(2, row.TotalChecks, "the genuine error must enter total_checks")
	r.Equal(1, row.SuccessfulChecks)
	r.NotNil(row.AvailabilityPct)
	r.InDelta(50.0, *row.AvailabilityPct, 0.0001, "the genuine error must drag availability down")
}

// TestGetAvailability_RawClampFollowsTheLiveRetentionParameter is the read-side
// regression for the retention source. uptimebar clamps its raw-tier query to
// the configured raw retention, and the server "Aggregation" settings tab writes
// that setting to the `performance.aggregation_retention_raw_hours` system
// parameter — NOT to the koanf config struct. A reader that only consulted koanf
// would keep clamping at the 24h default while the aggregation job kept three
// days of raw, silently dropping two days of rows that no rollup covers yet.
//
// The two subtests are each other's control: the same 3-day-old raw row is
// invisible with the parameter unset (the default 24h clamp, which is correct
// for that configuration) and counted once the parameter says the job keeps it.
func TestGetAvailability_RawClampFollowsTheLiveRetentionParameter(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := t.Context()

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	org := models.NewOrganization("avail-retention-org", "")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	check := models.NewCheck(org.UID, "avail-retention-check", "http")
	r.NoError(dbSvc.CreateCheck(ctx, check))

	// Drop the auto-seeded "created" lifecycle marker so only the seeded probe
	// below can produce a count.
	seeded, err := dbSvc.ListResults(ctx, &models.ListResultsFilter{
		OrganizationUID: org.UID, CheckUIDs: []string{check.UID}, Limit: 10,
	})
	r.NoError(err)

	seededUIDs := make([]string, 0, len(seeded.Results))
	for _, row := range seeded.Results {
		seededUIDs = append(seededUIDs, row.UID)
	}

	if len(seededUIDs) > 0 {
		_, delErr := dbSvc.DeleteResults(ctx, org.UID, seededUIDs)
		r.NoError(delErr)
	}

	probe := models.NewResult(org.UID, check.UID, models.ResultStatusUp, 0.1)
	probe.PeriodStart = time.Now().UTC().Add(-3 * 24 * time.Hour)
	r.NoError(dbSvc.CreateResult(ctx, probe))

	// cfg is nil: the koanf field says nothing, so only the system parameter can
	// move the clamp.
	svc := NewService(dbSvc, nil)

	totalFor := func(t *testing.T) int {
		t.Helper()

		resp, availErr := svc.GetAvailability(ctx, org.Slug, check.UID, &GetAvailabilityOptions{
			Periods: []string{"30d"},
		})
		require.NoError(t, availErr)
		require.Len(t, resp.Data, 1)

		return resp.Data[0].TotalChecks
	}

	r.Equal(0, totalFor(t),
		"with the default 24h raw retention the clamp correctly excludes a 3-day-old raw row")

	// What the Aggregation settings tab writes.
	r.NoError(dbSvc.SetSystemParameter(ctx,
		string(systemconfig.KeyPerfAggRetentionRawHours), 7*24, false))

	r.Equal(1, totalFor(t),
		"a UI-set performance.aggregation_retention_raw_hours must widen the raw clamp")
}

// TestGetAvailability_PeriodsFanOutPreservesRequestOrder is the key correctness
// test for the by-index concurrent fan-out: computePeriod for each window now
// runs in its own goroutine, so a naive append (instead of writing periods[i])
// would both race and scramble the response order. The tokens are deliberately
// out of chronological order so a silent sort — or a race that just happens to
// preserve order for monotonic windows — would not mask a regression.
func TestGetAvailability_PeriodsFanOutPreservesRequestOrder(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := t.Context()

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	org := models.NewOrganization("avail-order-org", "")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	check := models.NewCheck(org.UID, "avail-order-check", "http")
	r.NoError(dbSvc.CreateCheck(ctx, check))

	svc := NewService(dbSvc, nil)

	requested := []string{"365d", "1h", "30d", "90d", "24h"}

	resp, availErr := svc.GetAvailability(ctx, org.Slug, check.UID, &GetAvailabilityOptions{
		Periods: requested,
	})
	r.NoError(availErr)
	r.Len(resp.Data, len(requested))

	got := make([]string, len(resp.Data))
	for i, row := range resp.Data {
		got[i] = row.Period
	}

	r.Equal(requested, got, "response rows must stay in the requested token order, not chronological order")
}

// TestGetAvailability_PeriodsFanOutIsRace is the race-detector target for the
// concurrent fan-out (`go test -race`): several periods, run several times, so
// the detector has actual concurrent DB access to inspect.
func TestGetAvailability_PeriodsFanOutIsRace(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := t.Context()

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	org := models.NewOrganization("avail-race-org", "")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	check := models.NewCheck(org.UID, "avail-race-check", "http")
	r.NoError(dbSvc.CreateCheck(ctx, check))

	svc := NewService(dbSvc, nil)

	for i := range 5 {
		t.Run("run", func(t *testing.T) {
			t.Parallel()

			rr := require.New(t)

			resp, availErr := svc.GetAvailability(ctx, org.Slug, check.UID, &GetAvailabilityOptions{
				Periods: []string{"1h", "24h", "30d", "90d", "365d"},
			})
			rr.NoError(availErr, "run %d", i)
			rr.Len(resp.Data, 5, "run %d", i)
		})
	}
}

// errBarrierTimeout means the concurrent fan-out never got enough callers into
// ListIncidents at once — i.e. it regressed to serial execution.
var errBarrierTimeout = errors.New("periods are being computed serially")

// barrierIncidentsDB wraps a real db.Service and turns ListIncidents — the call
// every computePeriod goroutine makes exactly once, on its way out — into a
// rendezvous point. It blocks each caller until barrierN callers have arrived
// simultaneously, then releases them all (and lets every later caller through
// immediately, once released). This makes concurrency an observable, counted
// fact instead of a wall-clock guess: a serial implementation can never get two
// callers in flight at once, so it deadlocks at the barrier and the timeout
// fires with a diagnostic message instead of the test hanging or passing by
// accident.
type barrierIncidentsDB struct {
	db.Service

	barrierN int

	mu          sync.Mutex
	inFlight    int
	maxInFlight int
	released    bool
	releaseCh   chan struct{}
}

func newBarrierIncidentsDB(underlying db.Service, barrierN int) *barrierIncidentsDB {
	return &barrierIncidentsDB{
		Service:   underlying,
		barrierN:  barrierN,
		releaseCh: make(chan struct{}),
	}
}

func (b *barrierIncidentsDB) ListIncidents(
	ctx context.Context, filter *models.ListIncidentsFilter,
) ([]*models.Incident, int64, error) {
	b.mu.Lock()
	b.inFlight++

	if b.inFlight > b.maxInFlight {
		b.maxInFlight = b.inFlight
	}

	if b.inFlight >= b.barrierN && !b.released {
		b.released = true

		close(b.releaseCh)
	}

	snapshot := b.inFlight
	b.mu.Unlock()

	select {
	case <-b.releaseCh:
	case <-time.After(3 * time.Second):
		return nil, 0, fmt.Errorf(
			"%w: only %d of %d calls were ever in flight", errBarrierTimeout, snapshot, b.barrierN)
	}

	incidents, total, err := b.Service.ListIncidents(ctx, filter)

	b.mu.Lock()
	b.inFlight--
	b.mu.Unlock()

	return incidents, total, err
}

// TestGetAvailability_PeriodsRunConcurrently is the deterministic proof of the
// spec's headline claim: the per-window computations genuinely overlap, and the
// overlap never exceeds maxConcurrentPeriods. Both properties are pinned by
// count, not by timing — a revert to a serial loop makes this test fail with a
// clear message (via the barrier timeout) instead of silently passing.
func TestGetAvailability_PeriodsRunConcurrently(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := t.Context()

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	org := models.NewOrganization("avail-concur-org", "")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	check := models.NewCheck(org.UID, "avail-concur-check", "http")
	r.NoError(dbSvc.CreateCheck(ctx, check))

	// More periods than maxConcurrentPeriods so the barrier also exercises (and
	// pins) SetLimit: the fan-out must reach the limit but never exceed it.
	periods := []string{"1h", "2h", "3h", "6h", "12h", "24h", "2d", "7d"}
	r.Greater(len(periods), maxConcurrentPeriods,
		"test needs more periods than the concurrency limit to exercise SetLimit")

	barrier := newBarrierIncidentsDB(dbSvc, maxConcurrentPeriods)
	svc := NewService(barrier, nil)

	resp, availErr := svc.GetAvailability(ctx, org.Slug, check.UID, &GetAvailabilityOptions{
		Periods: periods,
	})
	r.NoError(availErr)
	r.Len(resp.Data, len(periods))

	barrier.mu.Lock()
	maxInFlight := barrier.maxInFlight
	barrier.mu.Unlock()

	r.Greater(maxInFlight, 1, "periods must be computed concurrently, not one at a time")
	r.LessOrEqual(maxInFlight, maxConcurrentPeriods, "SetLimit must bound the fan-out")
	r.Equal(maxConcurrentPeriods, maxInFlight,
		"the barrier only releases once maxConcurrentPeriods callers are simultaneously in flight, "+
			"so a correctly-bounded fan-out must reach exactly that many")
}
