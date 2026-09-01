package slo_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/slo"
)

func TestLoadLocation(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	loc, err := slo.LoadLocation("")
	r.NoError(err)
	r.Equal(time.UTC, loc)

	loc, err = slo.LoadLocation("Europe/Paris")
	r.NoError(err)
	r.Equal("Europe/Paris", loc.String())

	_, err = slo.LoadLocation("Mars/Olympus_Mons")
	r.ErrorIs(err, slo.ErrInvalidTimezone)
}

func TestMonthWindowAcrossTimezones(t *testing.T) {
	t.Parallel()

	paris, err := time.LoadLocation("Europe/Paris")
	require.NoError(t, err)

	auckland, err := time.LoadLocation("Pacific/Auckland")
	require.NoError(t, err)

	tests := []struct {
		name      string
		loc       *time.Location
		at        time.Time
		wantStart string
		wantEnd   string
	}{
		{
			name:      "utc mid month",
			loc:       time.UTC,
			at:        time.Date(2026, 3, 17, 11, 30, 0, 0, time.UTC),
			wantStart: "2026-03-01T00:00:00Z",
			wantEnd:   "2026-04-01T00:00:00Z",
		},
		{
			// 00:30 UTC on the 1st is still February 28th 23:30 in New York,
			// so a New-York SLO must still be in February.
			name:      "instant that is a different month locally",
			loc:       mustLoad(t, "America/New_York"),
			at:        time.Date(2026, 3, 1, 0, 30, 0, 0, time.UTC),
			wantStart: "2026-02-01T05:00:00Z",
			wantEnd:   "2026-03-01T05:00:00Z",
		},
		{
			name:      "paris offset",
			loc:       paris,
			at:        time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC),
			wantStart: "2026-06-30T22:00:00Z",
			wantEnd:   "2026-07-31T22:00:00Z",
		},
		{
			name:      "southern hemisphere dst",
			loc:       auckland,
			at:        time.Date(2026, 4, 10, 0, 0, 0, 0, time.UTC),
			wantStart: "2026-03-31T11:00:00Z",
			wantEnd:   "2026-04-30T12:00:00Z",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			r := require.New(t)
			w := slo.MonthWindow(tt.loc, tt.at)
			r.Equal(tt.wantStart, w.Start.UTC().Format(time.RFC3339))
			r.Equal(tt.wantEnd, w.End.UTC().Format(time.RFC3339))
			r.True(w.Contains(tt.at))
		})
	}
}

// A month containing a DST transition is genuinely an hour shorter or longer
// than the naive 24h-per-day arithmetic. An error budget denominated in seconds
// has to reflect that, or "0.1% of March" is wrong by 3.6 s every spring.
func TestMonthWindowLengthTracksDST(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	paris := mustLoad(t, "Europe/Paris")

	// March 2026: clocks jump forward on the last Sunday -> 31 days minus 1h.
	spring := slo.MonthWindow(paris, time.Date(2026, 3, 10, 0, 0, 0, 0, paris))
	r.InDelta(31*24*3600-3600, spring.Seconds(), 0.5)

	// October 2026: clocks fall back -> 31 days plus 1h.
	autumn := slo.MonthWindow(paris, time.Date(2026, 10, 10, 0, 0, 0, 0, paris))
	r.InDelta(31*24*3600+3600, autumn.Seconds(), 0.5)

	// The same months in UTC have no transition at all.
	utcSpring := slo.MonthWindow(time.UTC, time.Date(2026, 3, 10, 0, 0, 0, 0, time.UTC))
	r.InDelta(31*24*3600, utcSpring.Seconds(), 0.5)
}

func TestPreviousMonthWindows(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	windows := slo.PreviousMonthWindows(time.UTC, time.Date(2026, 1, 15, 0, 0, 0, 0, time.UTC), 3)
	r.Len(windows, 3)
	r.Equal("2026-01-01T00:00:00Z", windows[0].Start.UTC().Format(time.RFC3339))
	r.Equal("2025-12-01T00:00:00Z", windows[1].Start.UTC().Format(time.RFC3339))
	r.Equal("2025-11-01T00:00:00Z", windows[2].Start.UTC().Format(time.RFC3339))

	r.Nil(slo.PreviousMonthWindows(time.UTC, time.Now(), 0))
}

func TestWeekWindowStartsMonday(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	// 2026-08-20 is a Thursday.
	w := slo.WeekWindow(time.UTC, time.Date(2026, 8, 20, 13, 0, 0, 0, time.UTC))
	r.Equal("2026-08-17T00:00:00Z", w.Start.UTC().Format(time.RFC3339))
	r.Equal("2026-08-24T00:00:00Z", w.End.UTC().Format(time.RFC3339))

	// Sunday must belong to the week that started the previous Monday, not to
	// the next one (Go's Weekday puts Sunday at 0).
	sunday := slo.WeekWindow(time.UTC, time.Date(2026, 8, 23, 23, 59, 0, 0, time.UTC))
	r.Equal(w.Start, sunday.Start)
}

func TestPreviousWindow(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	monthly := slo.PreviousWindow(time.UTC, time.Date(2026, 3, 2, 4, 0, 0, 0, time.UTC), false)
	r.Equal("2026-02-01T00:00:00Z", monthly.Start.UTC().Format(time.RFC3339))
	r.Equal("2026-03-01T00:00:00Z", monthly.End.UTC().Format(time.RFC3339))

	weekly := slo.PreviousWindow(time.UTC, time.Date(2026, 8, 20, 4, 0, 0, 0, time.UTC), true)
	r.Equal("2026-08-10T00:00:00Z", weekly.Start.UTC().Format(time.RFC3339))
	r.Equal("2026-08-17T00:00:00Z", weekly.End.UTC().Format(time.RFC3339))
}

func mustLoad(t *testing.T, name string) *time.Location {
	t.Helper()

	loc, err := time.LoadLocation(name)
	require.NoError(t, err)

	return loc
}

// TestPrecedingWindowIsCalendarAware is the whole point of the helper: the
// period before a report window must be the previous CALENDAR period, not the
// same number of seconds subtracted. A duration-based "previous month" for
// March would reach back into January, and the two windows must touch exactly —
// no gap, no overlap.
func TestPrecedingWindowIsCalendarAware(t *testing.T) {
	t.Parallel()

	paris := mustLoad(t, "Europe/Paris")

	for _, tc := range []struct {
		name       string
		loc        *time.Location
		window     slo.Window
		weekly     bool
		wantStart  string
		wantEnd    string
		wantLength time.Duration
	}{
		{
			// The case a naive `Start.Add(-span)` gets wrong: March is 31 days,
			// February 28, so subtracting March's span lands on 1 February
			// minus 3 days.
			name:       "month before March is a 28-day February",
			loc:        time.UTC,
			window:     slo.MonthWindow(time.UTC, time.Date(2026, 3, 10, 0, 0, 0, 0, time.UTC)),
			wantStart:  "2026-02-01T00:00:00Z",
			wantEnd:    "2026-03-01T00:00:00Z",
			wantLength: 28 * 24 * time.Hour,
		},
		{
			name:       "month before January rolls the year back",
			loc:        time.UTC,
			window:     slo.MonthWindow(time.UTC, time.Date(2026, 1, 10, 0, 0, 0, 0, time.UTC)),
			wantStart:  "2025-12-01T00:00:00Z",
			wantEnd:    "2026-01-01T00:00:00Z",
			wantLength: 31 * 24 * time.Hour,
		},
		{
			// March 2026 contains the European spring-forward (Sunday 29
			// March), so the month the wall clock lived through is an hour
			// shorter than 31x24h.
			name:       "DST month is honestly an hour short",
			loc:        paris,
			window:     slo.MonthWindow(paris, time.Date(2026, 4, 10, 0, 0, 0, 0, paris)),
			wantStart:  "2026-02-28T23:00:00Z",
			wantEnd:    "2026-03-31T22:00:00Z",
			wantLength: 31*24*time.Hour - time.Hour,
		},
		{
			name:       "week before is exactly seven days",
			loc:        time.UTC,
			window:     slo.WeekWindow(time.UTC, time.Date(2026, 8, 20, 0, 0, 0, 0, time.UTC)),
			weekly:     true,
			wantStart:  "2026-08-10T00:00:00Z",
			wantEnd:    "2026-08-17T00:00:00Z",
			wantLength: 7 * 24 * time.Hour,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			r := require.New(t)

			prev := slo.PrecedingWindow(tc.loc, tc.window, tc.weekly)

			r.Equal(tc.wantStart, prev.Start.UTC().Format(time.RFC3339))
			r.Equal(tc.wantEnd, prev.End.UTC().Format(time.RFC3339))
			r.Equal(tc.wantLength, prev.End.Sub(prev.Start))

			// Adjacent and half-open: the previous window ends exactly where
			// the reported one begins.
			r.True(prev.End.Equal(tc.window.Start))
		})
	}
}

// TestPrecedingWindowDefaultsToUTC pins the nil-location fallback — a schedule
// with an unloadable timezone must still produce a usable comparison window
// rather than panicking inside the report builder.
func TestPrecedingWindowDefaultsToUTC(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	window := slo.MonthWindow(time.UTC, time.Date(2026, 7, 15, 0, 0, 0, 0, time.UTC))
	prev := slo.PrecedingWindow(nil, window, false)

	r.Equal("2026-06-01T00:00:00Z", prev.Start.UTC().Format(time.RFC3339))
	r.Equal("2026-07-01T00:00:00Z", prev.End.UTC().Format(time.RFC3339))
}
