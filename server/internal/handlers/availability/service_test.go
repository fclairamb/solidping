package availability

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
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
