package oncallschedules

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

// nextHandoff and computePeriodIndex are the rotation math. The spec calls
// these out as the place where bugs hide ("Don't skimp"), so the tests
// exercise the full DST + weekday-arithmetic surface in real IANA zones
// before any DB-backed flow runs.

func TestNextHandoffDailyWalksOneDay(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	loc, err := time.LoadLocation("Europe/Paris")
	r.NoError(err)

	// Tuesday 2026-05-05 08:00 local. Daily 09:00 handoff → next is the
	// same day 09:00.
	current := time.Date(2026, 5, 5, 8, 0, 0, 0, loc)
	next := nextHandoff(current, models.RotationTypeDaily, nil, 9, 0, loc)
	r.Equal(time.Date(2026, 5, 5, 9, 0, 0, 0, loc), next)

	// 09:01 same day → next handoff is tomorrow 09:00. Strictly-after
	// semantics matter: a `current == handoff` should advance, not stick.
	current = time.Date(2026, 5, 5, 9, 1, 0, 0, loc)
	next = nextHandoff(current, models.RotationTypeDaily, nil, 9, 0, loc)
	r.Equal(time.Date(2026, 5, 6, 9, 0, 0, 0, loc), next)
}

func TestNextHandoffWeeklyFindsTargetDay(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	loc, err := time.LoadLocation("Europe/Paris")
	r.NoError(err)

	// Sunday 2026-05-03 23:59 local, weekly Monday-09:00 handoff →
	// next is Monday 2026-05-04 09:00.
	mon := 0
	current := time.Date(2026, 5, 3, 23, 59, 0, 0, loc)
	next := nextHandoff(current, models.RotationTypeWeekly, &mon, 9, 0, loc)
	r.Equal(time.Date(2026, 5, 4, 9, 0, 0, 0, loc), next)
	r.Equal(time.Monday, next.Weekday())

	// Monday 09:01 → next is the Monday after, 09:00.
	current = time.Date(2026, 5, 4, 9, 1, 0, 0, loc)
	next = nextHandoff(current, models.RotationTypeWeekly, &mon, 9, 0, loc)
	r.Equal(time.Date(2026, 5, 11, 9, 0, 0, 0, loc), next)
}

func TestNextHandoffDSTSpringForwardEuropeParis(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	loc, err := time.LoadLocation("Europe/Paris")
	r.NoError(err)

	// Europe/Paris springs forward on the last Sunday of March; in 2026
	// that's Sunday 2026-03-29 02:00 → 03:00 local. A Sunday 09:00 weekly
	// handoff must stay at 09:00 local both before and after — never
	// drifting to 08:00 or 10:00.
	sun := 6                                       // Mon=0..Sun=6
	pre := time.Date(2026, 3, 22, 9, 1, 0, 0, loc) // Sunday before DST
	post := nextHandoff(pre, models.RotationTypeWeekly, &sun, 9, 0, loc)
	r.Equal(time.Date(2026, 3, 29, 9, 0, 0, 0, loc), post, "post-DST handoff stays at 09:00 local")
	r.Equal(time.Sunday, post.Weekday())
	// And the wall-clock hour must read 09 in the local zone.
	r.Equal(9, post.In(loc).Hour())
}

func TestNextHandoffDSTSouthernHemisphereAuckland(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	loc, err := time.LoadLocation("Pacific/Auckland")
	r.NoError(err)

	// Pacific/Auckland transitions opposite Europe/Paris. April 2026
	// fall-back is Sunday 5 April 03:00 → 02:00. A weekly Sunday 09:00
	// handoff must still land at 09:00 local.
	sun := 6
	pre := time.Date(2026, 3, 29, 9, 1, 0, 0, loc)
	post := nextHandoff(pre, models.RotationTypeWeekly, &sun, 9, 0, loc)
	r.Equal(time.Date(2026, 4, 5, 9, 0, 0, 0, loc), post)
	r.Equal(9, post.In(loc).Hour())
}

func TestComputePeriodIndexWeeklyParis(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	loc, err := time.LoadLocation("Europe/Paris")
	r.NoError(err)

	mon := 0
	startAt := time.Date(2026, 5, 4, 9, 0, 0, 0, loc) // Monday 09:00 local
	schedule := &models.OnCallSchedule{
		Timezone:       "Europe/Paris",
		RotationType:   models.RotationTypeWeekly,
		HandoffTime:    "09:00",
		HandoffWeekday: &mon,
		StartAt:        startAt,
	}

	// At StartAt itself, no handoff has happened yet (computePeriodIndex
	// counts handoffs <= instant). Period index = 0 → user position 0.
	idx, err := computePeriodIndex(schedule, startAt, loc)
	r.NoError(err)
	r.Equal(int64(0), idx)

	// Tuesday — still inside the first slot — period index stays 0.
	idx, err = computePeriodIndex(schedule, startAt.Add(24*time.Hour), loc)
	r.NoError(err)
	r.Equal(int64(0), idx)

	// Next Monday 09:00 — first handoff has happened → period index 1.
	idx, err = computePeriodIndex(schedule, startAt.Add(7*24*time.Hour), loc)
	r.NoError(err)
	r.Equal(int64(1), idx)

	// 26 weeks out (calendar-aware so DST doesn't shift the local hour) →
	// period index 26.
	idx, err = computePeriodIndex(schedule, startAt.AddDate(0, 0, 26*7), loc)
	r.NoError(err)
	r.Equal(int64(26), idx)
}

func TestComputePeriodIndexDaily(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	loc, err := time.LoadLocation("Europe/Paris")
	r.NoError(err)

	startAt := time.Date(2026, 5, 4, 9, 0, 0, 0, loc)
	schedule := &models.OnCallSchedule{
		Timezone:     "Europe/Paris",
		RotationType: models.RotationTypeDaily,
		HandoffTime:  "09:00",
		StartAt:      startAt,
	}

	idx, err := computePeriodIndex(schedule, startAt, loc)
	r.NoError(err)
	r.Equal(int64(0), idx)

	// One day later, exactly at handoff → 1.
	idx, err = computePeriodIndex(schedule, startAt.Add(24*time.Hour), loc)
	r.NoError(err)
	r.Equal(int64(1), idx)

	// 30 days later → 30.
	idx, err = computePeriodIndex(schedule, startAt.Add(30*24*time.Hour), loc)
	r.NoError(err)
	r.Equal(int64(30), idx)
}

func TestMondayBased(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	r.Equal(time.Monday, mondayBased(0))
	r.Equal(time.Tuesday, mondayBased(1))
	r.Equal(time.Saturday, mondayBased(5))
	r.Equal(time.Sunday, mondayBased(6))
	r.Equal(time.Monday, mondayBased(7)) // wraps
}

func TestParseHandoffTime(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	hour, minute, err := parseHandoffTime("09:00")
	r.NoError(err)
	r.Equal(9, hour)
	r.Equal(0, minute)

	hour, minute, err = parseHandoffTime("23:59")
	r.NoError(err)
	r.Equal(23, hour)
	r.Equal(59, minute)

	for _, bad := range []string{"", "9", "9:00:00", "24:00", "12:60", "ab:cd", "-1:00"} {
		_, _, parseErr := parseHandoffTime(bad)
		r.ErrorIs(parseErr, ErrInvalidHandoffTime, "input=%q", bad)
	}
}

func TestCovers(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	start := time.Date(2026, 5, 5, 9, 0, 0, 0, time.UTC)
	end := time.Date(2026, 5, 6, 9, 0, 0, 0, time.UTC)
	override := &models.OnCallScheduleOverride{StartAt: start, EndAt: end}

	r.True(covers(override, start), "start is inclusive")
	r.True(covers(override, start.Add(time.Hour)))
	r.False(covers(override, end), "end is exclusive")
	r.False(covers(override, start.Add(-time.Second)))
}
