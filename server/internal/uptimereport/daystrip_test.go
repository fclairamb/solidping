package uptimereport

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/slo"
)

// TestPlanDayStripCoversEveryDayOfTheWindow pins the cell geometry, including
// the UTC alignment the bucketing engine requires: the series must START on a
// UTC-day boundary, or uptimebar's Truncate-keyed rows land in the neighboring
// cell.
func TestPlanDayStripCoversEveryDayOfTheWindow(t *testing.T) {
	t.Parallel()

	paris, err := time.LoadLocation("Europe/Paris")
	require.NoError(t, err)

	for _, tc := range []struct {
		name      string
		window    slo.Window
		wantStart string
		wantDays  int
	}{
		{
			name:      "31-day month in UTC",
			window:    slo.MonthWindow(time.UTC, time.Date(2026, 7, 15, 0, 0, 0, 0, time.UTC)),
			wantStart: "2026-07-01T00:00:00Z",
			wantDays:  31,
		},
		{
			name:      "28-day month in UTC",
			window:    slo.MonthWindow(time.UTC, time.Date(2026, 2, 15, 0, 0, 0, 0, time.UTC)),
			wantStart: "2026-02-01T00:00:00Z",
			wantDays:  28,
		},
		{
			// Local midnight in Paris is 22:00 UTC the previous day, so the
			// series starts on the UTC day the window opens in and needs one
			// extra cell to reach the window's end.
			name:      "month in a positive-offset zone",
			window:    slo.MonthWindow(paris, time.Date(2026, 7, 15, 0, 0, 0, 0, paris)),
			wantStart: "2026-06-30T00:00:00Z",
			wantDays:  32,
		},
		{
			name:      "week in UTC",
			window:    slo.WeekWindow(time.UTC, time.Date(2026, 8, 20, 0, 0, 0, 0, time.UTC)),
			wantStart: "2026-08-17T00:00:00Z",
			wantDays:  7,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			r := require.New(t)

			plan := planDayStrip(tc.window)

			r.Equal(tc.wantStart, plan.start.Format(time.RFC3339))
			r.Equal(tc.wantDays, plan.days)

			// UTC-day aligned, which is what uptimebar's Truncate(24h) keying
			// requires of the series start.
			r.True(plan.start.Equal(plan.start.Truncate(24 * time.Hour)))

			// Every instant of the window falls inside a cell.
			r.False(plan.start.After(tc.window.Start.UTC()))
			r.False(plan.start.AddDate(0, 0, plan.days).Before(tc.window.End.UTC()))

			r.Contains(plan.label(), "(UTC)")
		})
	}
}

// TestPlanDayStripIsBounded guards against a runaway row from a malformed
// window, and against an empty/inverted one producing cells at all.
func TestPlanDayStripIsBounded(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	r.Equal(maxStripDays, planDayStrip(slo.Window{Start: start, End: start.AddDate(1, 0, 0)}).days)

	empty := planDayStrip(slo.Window{Start: start, End: start})
	r.Zero(empty.days)
	r.Empty(empty.label())
}

// TestEncodeDayCellsCollapsesRuns is what keeps a 50-check monthly report far
// below Gmail's clipping limit: a healthy month is ONE cell, not 31.
func TestEncodeDayCellsCollapsesRuns(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	r.Nil(encodeDayCells(nil))

	healthy := make([]string, 31)
	for i := range healthy {
		healthy[i] = dayGoodColor
	}

	cells := encodeDayCells(healthy)
	r.Len(cells, 1)
	r.Equal(31, cells[0].Span)
	r.True(cells[0].Wide)

	mixed := encodeDayCells([]string{
		dayGoodColor, dayGoodColor, dayBadColor, dayGoodColor, dayNoDataColor, dayNoDataColor,
	})
	r.Equal([]DayCell{
		{Color: dayGoodColor, Span: 2, Wide: true},
		{Color: dayBadColor, Span: 1},
		{Color: dayGoodColor, Span: 1},
		{Color: dayNoDataColor, Span: 2, Wide: true},
	}, mixed)

	// Spans always add back up to the number of days handed in — a strip that
	// silently lost a day would be a lie about the period.
	total := 0
	for _, cell := range mixed {
		total += cell.Span
	}

	r.Equal(6, total)
}
