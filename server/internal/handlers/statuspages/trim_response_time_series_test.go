package statuspages

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

// trimTestResult builds a minimal Result for exercising trimResponseTimeSeries
// in isolation, without touching the database.
func trimTestResult(uid, periodType string, periodStart time.Time) *models.Result {
	return &models.Result{
		UID:         uid,
		PeriodType:  periodType,
		PeriodStart: periodStart,
	}
}

// TestTrimResponseTimeSeries_RollupTieBreakDeterministic pins the bug from
// spec 2026-08-23-01: two ROLLUP points (hour and day) sharing a period_start
// have no order under the old two-rung comparator (period_start DESC, then
// raw-beats-rollup), so sort.SliceStable falls back to input order.
//
// The only way to prove a tie-break is actually doing the work is to feed the
// same points in both possible input orders and assert the SAME output order
// both times. A single input order would pass by accident even without the
// fix, because sort.SliceStable happens to preserve it.
func TestTrimResponseTimeSeries_RollupTieBreakDeterministic(t *testing.T) {
	t.Parallel()

	base := time.Date(2026, 8, 23, 0, 0, 0, 0, time.UTC)

	// Ordinary points on either side of the tie, so the primary
	// period_start-DESC rung is exercised too.
	newer := trimTestResult("uid-newer", models.PeriodTypeRaw, base.Add(time.Hour))
	older := trimTestResult("uid-older", models.PeriodTypeDay, base.Add(-24*time.Hour))

	// Two ROLLUP points sharing one period_start (midnight): the hour bucket
	// for that hour and the day bucket for that day. uid-hour > uid-day
	// lexicographically, so uid DESC must place the hour point first.
	tieHour := trimTestResult("uid-hour", models.PeriodTypeHour, base)
	tieDay := trimTestResult("uid-day", models.PeriodTypeDay, base)

	wantOrder := []string{"uid-newer", "uid-hour", "uid-day", "uid-older"}

	cases := []struct {
		name  string
		input []*models.Result
	}{
		{name: "input order A", input: []*models.Result{newer, tieHour, tieDay, older}},
		{name: "input order B", input: []*models.Result{older, tieDay, tieHour, newer}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			recentByCheck := map[string]map[string][]*models.Result{
				"check-1": {"": append([]*models.Result{}, tc.input...)},
			}

			trimResponseTimeSeries(recentByCheck)

			got := make([]string, 0, len(wantOrder))
			for _, r := range recentByCheck["check-1"][""] {
				got = append(got, r.UID)
			}

			require.Equal(t, wantOrder, got)
		})
	}
}

// TestTrimResponseTimeSeries_RawBeatsRollupOnTie pins the existing rung: when
// a RAW point and a ROLLUP point share a period_start, raw must still win.
// This guards against the new uid-DESC rung silently taking precedence over
// it.
func TestTrimResponseTimeSeries_RawBeatsRollupOnTie(t *testing.T) {
	t.Parallel()

	base := time.Date(2026, 8, 23, 0, 0, 0, 0, time.UTC)

	// The rollup has the lexicographically larger UID, so if uid DESC ever
	// ran before the raw-wins rung, the rollup would incorrectly win.
	raw := trimTestResult("uid-a-raw", models.PeriodTypeRaw, base)
	rollup := trimTestResult("uid-z-rollup", models.PeriodTypeHour, base)

	recentByCheck := map[string]map[string][]*models.Result{
		"check-1": {"": {rollup, raw}},
	}

	trimResponseTimeSeries(recentByCheck)

	got := recentByCheck["check-1"][""]
	require.Len(t, got, 2)
	require.Equal(t, "uid-a-raw", got[0].UID)
	require.Equal(t, "uid-z-rollup", got[1].UID)
}
