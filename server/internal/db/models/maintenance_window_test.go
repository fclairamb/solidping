package models

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// mw is a tiny constructor for a maintenance window with the recurrence fields the
// tests care about; everything else is irrelevant to the recurrence math.
func mw(start, end time.Time, recurrence string, recurrenceEnd *time.Time) *MaintenanceWindow {
	return &MaintenanceWindow{
		StartAt:       start,
		EndAt:         end,
		Recurrence:    recurrence,
		RecurrenceEnd: recurrenceEnd,
	}
}

func ts(y int, m time.Month, d, h, min int) time.Time {
	return time.Date(y, m, d, h, min, 0, 0, time.UTC)
}

func TestDaysIn(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	r.Equal(31, daysIn(2026, time.January))
	r.Equal(28, daysIn(2026, time.February)) // non-leap
	r.Equal(29, daysIn(2024, time.February)) // leap
	r.Equal(30, daysIn(2026, time.April))
	r.Equal(31, daysIn(2026, time.December))
}

func TestAddMonthsClampsDay(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	jan31 := ts(2026, time.January, 31, 9, 0)

	// +1mo lands on Feb 28 (non-leap), NOT Mar 3.
	feb := addMonths(jan31, 1)
	r.Equal(2026, feb.Year())
	r.Equal(time.February, feb.Month())
	r.Equal(28, feb.Day())
	r.Equal(9, feb.Hour())

	// +2mo lands on Mar 31 — anchored on the original, so no accumulated drift.
	mar := addMonths(jan31, 2)
	r.Equal(time.March, mar.Month())
	r.Equal(31, mar.Day())

	// +3mo lands on Apr 30.
	apr := addMonths(jan31, 3)
	r.Equal(time.April, apr.Month())
	r.Equal(30, apr.Day())

	// Leap year: Jan 31 +1mo -> Feb 29.
	leap := addMonths(ts(2024, time.January, 31, 9, 0), 1)
	r.Equal(time.February, leap.Month())
	r.Equal(29, leap.Day())
}

func TestIsActiveAt_None(t *testing.T) {
	t.Parallel()

	start := ts(2026, time.June, 10, 12, 0)
	end := ts(2026, time.June, 10, 14, 0)
	window := mw(start, end, "none", nil)

	tests := []struct {
		name   string
		target time.Time
		want   bool
	}{
		{"before", ts(2026, time.June, 10, 11, 59), false},
		{"start boundary inclusive", start, true},
		{"inside", ts(2026, time.June, 10, 13, 0), true},
		{"end boundary exclusive", end, false},
		{"after", ts(2026, time.June, 10, 14, 1), false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tc.want, IsActiveAt(window, tc.target))
		})
	}
}

func TestIsActiveAt_Daily(t *testing.T) {
	t.Parallel()

	start := ts(2026, time.June, 1, 22, 0)
	end := ts(2026, time.June, 1, 23, 0)
	recEnd := ts(2026, time.June, 30, 0, 0)
	window := mw(start, end, "daily", &recEnd)

	tests := []struct {
		name   string
		target time.Time
		want   bool
	}{
		{"day 0 in slot", ts(2026, time.June, 1, 22, 30), true},
		{"day 0 out of slot", ts(2026, time.June, 1, 21, 30), false},
		{"day 10 in slot", ts(2026, time.June, 11, 22, 30), true},
		{"day 10 between slots", ts(2026, time.June, 11, 12, 0), false},
		{"before start", ts(2026, time.May, 31, 22, 30), false},
		{"after recurrence end", ts(2026, time.July, 1, 22, 30), false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tc.want, IsActiveAt(window, tc.target))
		})
	}
}

func TestIsActiveAt_Weekly(t *testing.T) {
	t.Parallel()

	// 2026-06-01 is a Monday.
	start := ts(2026, time.June, 1, 9, 0)
	end := ts(2026, time.June, 1, 10, 0)
	window := mw(start, end, "weekly", nil)

	tests := []struct {
		name   string
		target time.Time
		want   bool
	}{
		{"anchor monday in slot", ts(2026, time.June, 1, 9, 30), true},
		{"next monday in slot", ts(2026, time.June, 8, 9, 30), true},
		{"four weeks later", ts(2026, time.June, 29, 9, 30), true},
		{"tuesday not matched", ts(2026, time.June, 2, 9, 30), false},
		{"monday out of slot", ts(2026, time.June, 8, 11, 0), false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tc.want, IsActiveAt(window, tc.target))
		})
	}
}

// TestIsActiveAt_MonthlyNoDrift is the P1 regression: a monthly window anchored on
// the 31st must activate on the last day of each month with no accumulated drift.
func TestIsActiveAt_MonthlyNoDrift(t *testing.T) {
	t.Parallel()

	start := ts(2026, time.January, 31, 22, 0)
	end := ts(2026, time.January, 31, 23, 0)
	window := mw(start, end, "monthly", nil)

	tests := []struct {
		name   string
		target time.Time
		want   bool
	}{
		{"jan 31 active", ts(2026, time.January, 31, 22, 30), true},
		{"feb 28 active (clamped)", ts(2026, time.February, 28, 22, 30), true},
		{"feb 27 inactive", ts(2026, time.February, 27, 22, 30), false},
		{"mar 31 active (no drift to mar 3)", ts(2026, time.March, 31, 22, 30), true},
		{"mar 3 inactive (the old bug)", ts(2026, time.March, 3, 22, 30), false},
		{"apr 30 active", ts(2026, time.April, 30, 22, 30), true},
		{"may 31 active", ts(2026, time.May, 31, 22, 30), true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tc.want, IsActiveAt(window, tc.target))
		})
	}
}

func TestIsActiveAt_MonthlyMidMonth(t *testing.T) {
	t.Parallel()

	start := ts(2026, time.January, 15, 8, 0)
	end := ts(2026, time.January, 15, 9, 0)
	window := mw(start, end, "monthly", nil)

	for _, m := range []time.Month{time.January, time.February, time.March, time.April, time.May} {
		require.True(t, IsActiveAt(window, ts(2026, m, 15, 8, 30)),
			"should be active on the 15th of %s", m)
		require.False(t, IsActiveAt(window, ts(2026, m, 16, 8, 30)),
			"should be inactive on the 16th of %s", m)
	}
}

// TestIsActiveAt_MonthlyJan30AcrossLeap checks an anchor on the 30th clamps to
// Feb 29 in a leap year and Feb 28 otherwise.
func TestIsActiveAt_MonthlyJan30AcrossLeap(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	// Leap year 2024: Jan 30 -> Feb 29.
	leap := mw(ts(2024, time.January, 30, 10, 0), ts(2024, time.January, 30, 11, 0), "monthly", nil)
	r.True(IsActiveAt(leap, ts(2024, time.February, 29, 10, 30)))
	r.False(IsActiveAt(leap, ts(2024, time.February, 28, 10, 30)))

	// Non-leap year 2026: Jan 30 -> Feb 28.
	nonLeap := mw(ts(2026, time.January, 30, 10, 0), ts(2026, time.January, 30, 11, 0), "monthly", nil)
	r.True(IsActiveAt(nonLeap, ts(2026, time.February, 28, 10, 30)))
}

func TestStatus(t *testing.T) {
	t.Parallel()

	now := ts(2026, time.June, 15, 12, 0)

	tests := []struct {
		name   string
		window *MaintenanceWindow
		want   string
	}{
		{
			name:   "none upcoming",
			window: mw(ts(2026, time.June, 20, 12, 0), ts(2026, time.June, 20, 13, 0), "none", nil),
			want:   "upcoming",
		},
		{
			name:   "none active",
			window: mw(ts(2026, time.June, 15, 11, 0), ts(2026, time.June, 15, 13, 0), "none", nil),
			want:   "active",
		},
		{
			name:   "none past",
			window: mw(ts(2026, time.June, 10, 11, 0), ts(2026, time.June, 10, 13, 0), "none", nil),
			want:   "past",
		},
		{
			name: "recurring active",
			window: mw(ts(2026, time.June, 1, 11, 0), ts(2026, time.June, 1, 13, 0), "daily",
				ptr(ts(2026, time.July, 1, 0, 0))),
			want: "active",
		},
		{
			name: "recurring upcoming before start",
			window: mw(ts(2026, time.June, 20, 11, 0), ts(2026, time.June, 20, 13, 0), "daily",
				nil),
			want: "upcoming",
		},
		{
			name: "recurring past after recurrence end",
			window: mw(ts(2026, time.June, 1, 11, 0), ts(2026, time.June, 1, 13, 0), "daily",
				ptr(ts(2026, time.June, 10, 0, 0))),
			want: "past",
		},
		{
			name: "recurring upcoming between slots, no recurrence end",
			window: mw(ts(2026, time.June, 1, 22, 0), ts(2026, time.June, 1, 23, 0), "daily",
				nil),
			want: "upcoming",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tc.want, Status(tc.window, now))
		})
	}
}

func TestNextOccurrences_None(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	start := ts(2026, time.June, 20, 12, 0)
	end := ts(2026, time.June, 20, 13, 0)
	window := mw(start, end, "none", nil)

	// Upcoming one-off: single occurrence returned.
	occ := NextOccurrences(window, ts(2026, time.June, 15, 0, 0), 3)
	r.Len(occ, 1)
	r.Equal(start, occ[0].StartAt)
	r.Equal(end, occ[0].EndAt)

	// Active one-off (from inside the window): still returned.
	occ = NextOccurrences(window, ts(2026, time.June, 20, 12, 30), 3)
	r.Len(occ, 1)

	// Past one-off: empty.
	occ = NextOccurrences(window, ts(2026, time.June, 21, 0, 0), 3)
	r.Empty(occ)

	// n <= 0 returns nil.
	r.Nil(NextOccurrences(window, ts(2026, time.June, 15, 0, 0), 0))
}

func TestNextOccurrences_DailyChronologicalAndActiveFirst(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	start := ts(2026, time.June, 1, 22, 0)
	end := ts(2026, time.June, 1, 23, 0)
	window := mw(start, end, "daily", nil)

	// from is inside the June 10 slot: that occurrence must be first.
	from := ts(2026, time.June, 10, 22, 30)
	occ := NextOccurrences(window, from, 3)
	r.Len(occ, 3)

	r.Equal(ts(2026, time.June, 10, 22, 0), occ[0].StartAt)
	r.Equal(ts(2026, time.June, 11, 22, 0), occ[1].StartAt)
	r.Equal(ts(2026, time.June, 12, 22, 0), occ[2].StartAt)

	// Chronological and each lasts one hour.
	for i := range occ {
		r.True(occ[i].EndAt.After(occ[i].StartAt))
		r.Equal(time.Hour, occ[i].EndAt.Sub(occ[i].StartAt))
		if i > 0 {
			r.True(occ[i].StartAt.After(occ[i-1].StartAt))
		}
	}
}

func TestNextOccurrences_HonorsRecurrenceEnd(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	start := ts(2026, time.June, 1, 22, 0)
	end := ts(2026, time.June, 1, 23, 0)
	recEnd := ts(2026, time.June, 3, 0, 0)
	window := mw(start, end, "daily", &recEnd)

	occ := NextOccurrences(window, ts(2026, time.June, 1, 0, 0), 10)
	// Only Jun 1 and Jun 2 occurrences fall before the recurrence end.
	r.Len(occ, 2)
	r.Equal(ts(2026, time.June, 1, 22, 0), occ[0].StartAt)
	r.Equal(ts(2026, time.June, 2, 22, 0), occ[1].StartAt)
}

// TestNextOccurrences_MonthlyClampHolds verifies clamping across the returned set.
func TestNextOccurrences_MonthlyClampHolds(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	start := ts(2026, time.January, 31, 22, 0)
	end := ts(2026, time.January, 31, 23, 0)
	window := mw(start, end, "monthly", nil)

	occ := NextOccurrences(window, ts(2026, time.January, 1, 0, 0), 4)
	r.Len(occ, 4)

	wantDays := []struct {
		month time.Month
		day   int
	}{
		{time.January, 31},
		{time.February, 28},
		{time.March, 31},
		{time.April, 30},
	}
	for i, w := range wantDays {
		r.Equal(w.month, occ[i].StartAt.Month(), "occurrence %d month", i)
		r.Equal(w.day, occ[i].StartAt.Day(), "occurrence %d day", i)
	}
}

func TestNextOccurrences_CapsN(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	start := ts(2026, time.January, 1, 0, 0)
	end := ts(2026, time.January, 1, 1, 0)
	window := mw(start, end, "daily", nil)

	occ := NextOccurrences(window, start, 1000)
	r.Len(occ, maxNextOccurrences)
}

func ptr[T any](v T) *T { return &v }
