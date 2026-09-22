package degraded_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/degraded"
)

// now is the fixed evaluation instant every case measures against.
var now = time.Date(2026, 9, 22, 15, 30, 0, 0, time.UTC) //nolint:gochecknoglobals // test fixture

// defaultParams is the fleet calibration the spec ships: failures 5 of 60, slow
// 3 of 6, slow rule off until a threshold is set.
func defaultParams() degraded.Params {
	return degraded.Params{
		Failures:       5,
		FailuresWindow: 60,
		Slow:           3,
		SlowWindow:     6,
		SlowThreshold:  0,
		Period:         time.Minute,
	}
}

// probe builds one probe `minutesAgo` before `now`.
func probe(minutesAgo int, status models.ResultStatus, durationMs float64) degraded.Probe {
	return degraded.Probe{
		At:         now.Add(-time.Duration(minutesAgo) * time.Minute),
		Status:     status,
		DurationMs: durationMs,
	}
}

// series builds `count` consecutive up probes, oldest last, starting one minute
// ago — i.e. the newest-first order Evaluate requires.
func series(count int, status models.ResultStatus, durationMs float64) []degraded.Probe {
	out := make([]degraded.Probe, 0, count)
	for i := 1; i <= count; i++ {
		out = append(out, probe(i, status, durationMs))
	}

	return out
}

func TestEvaluateFailureRule(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		probes      []degraded.Probe
		params      degraded.Params
		wantFired   bool
		wantMatches int
		wantSlots   int
	}{
		{
			name:        "clean check does not fire",
			probes:      series(60, models.ResultStatusUp, 100),
			params:      defaultParams(),
			wantFired:   false,
			wantMatches: 0,
			wantSlots:   60,
		},
		{
			name: "four failures in the window stay below the threshold",
			probes: append(
				[]degraded.Probe{
					probe(1, models.ResultStatusDown, 0),
					probe(2, models.ResultStatusDown, 0),
					probe(3, models.ResultStatusTimeout, 0),
					probe(4, models.ResultStatusError, 0),
				},
				series(56, models.ResultStatusUp, 100)...,
			),
			params:      defaultParams(),
			wantFired:   false,
			wantMatches: 4,
			wantSlots:   60,
		},
		{
			name: "five failures fire",
			probes: append(
				[]degraded.Probe{
					probe(1, models.ResultStatusDown, 0),
					probe(2, models.ResultStatusDown, 0),
					probe(3, models.ResultStatusTimeout, 0),
					probe(4, models.ResultStatusError, 0),
					probe(5, models.ResultStatusDown, 0),
				},
				series(55, models.ResultStatusUp, 100)...,
			),
			params:      defaultParams(),
			wantFired:   true,
			wantMatches: 5,
			wantSlots:   60,
		},
		{
			name: "warning counts as up, never as a failure",
			probes: append(
				series(5, models.ResultStatusWarning, 100),
				series(55, models.ResultStatusUp, 100)...,
			),
			params:      defaultParams(),
			wantFired:   false,
			wantMatches: 0,
			wantSlots:   60,
		},
		{
			name: "M = 0 disables the rule outright",
			probes: append(
				series(10, models.ResultStatusDown, 0),
				series(50, models.ResultStatusUp, 100)...,
			),
			params: func() degraded.Params {
				p := defaultParams()
				p.Failures = 0

				return p
			}(),
			wantFired:   false,
			wantMatches: 0,
			wantSlots:   0,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)

			out := degraded.Evaluate(test.probes, test.params, now)

			r.Equal(test.wantFired, out.Failure.Fired)
			r.Equal(test.wantMatches, out.Failure.Matches)
			r.Equal(test.wantSlots, out.Failure.Slots)
			r.Equal(test.wantFired, out.Firing())
		})
	}
}

// TestEvaluateWindowEdge pins the window boundary: the 61st-oldest probe is
// outside a 60-probe window, however bad it was.
func TestEvaluateWindowEdge(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	params := defaultParams()
	params.Failures = 2
	params.FailuresWindow = 5

	// Newest five are clean; the sixth is a failure and must not count.
	probes := append(series(5, models.ResultStatusUp, 100), probe(6, models.ResultStatusDown, 0))

	out := degraded.Evaluate(probes, params, now)

	r.Equal(5, out.Failure.Slots)
	r.Equal(0, out.Failure.Matches)
	r.False(out.Failure.Fired)

	// Slide the window by one: the failure is now the fifth-newest (inside the
	// window) and a second one joins it, so 2-of-5 fires.
	probes = []degraded.Probe{
		probe(1, models.ResultStatusDown, 0),
		probe(2, models.ResultStatusUp, 100),
		probe(3, models.ResultStatusUp, 100),
		probe(4, models.ResultStatusUp, 100),
		probe(5, models.ResultStatusDown, 0),
		probe(6, models.ResultStatusUp, 100),
	}

	out = degraded.Evaluate(probes, params, now)

	r.Equal(2, out.Failure.Matches)
	r.True(out.Failure.Fired)
}

// TestEvaluateMaxAge pins "the last 6 probes of a check that was paused for two
// days must not include Tuesday": a probe older than 2 x N x period is dropped,
// and dropping it does NOT free the slot for an even older one.
func TestEvaluateMaxAge(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	params := degraded.Params{
		Failures:       2,
		FailuresWindow: 6,
		Period:         time.Minute,
	}

	// 2 x 6 x 1m = 12 minutes of reach. Two failures 30 minutes back are the
	// entire history: stale, so nothing fires and no slot is consumed.
	probes := []degraded.Probe{
		probe(30, models.ResultStatusDown, 0),
		probe(31, models.ResultStatusDown, 0),
	}

	out := degraded.Evaluate(probes, params, now)

	r.Equal(0, out.Failure.Slots)
	r.Equal(0, out.Failure.Matches)
	r.False(out.Failure.Fired)

	// The same two failures inside the reach do fire.
	probes = []degraded.Probe{
		probe(3, models.ResultStatusDown, 0),
		probe(4, models.ResultStatusDown, 0),
	}

	out = degraded.Evaluate(probes, params, now)

	r.Equal(2, out.Failure.Slots)
	r.True(out.Failure.Fired)
}

// TestEvaluateMaintenanceIsNotASlot pins the rule that a maintenance probe is
// SKIPPED rather than counted: it must neither match nor push a real failure out
// of the window.
func TestEvaluateMaintenanceIsNotASlot(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	params := degraded.Params{
		Failures:       2,
		FailuresWindow: 3,
		Period:         time.Minute,
	}

	maintenanceDown := probe(1, models.ResultStatusDown, 0)
	maintenanceDown.Maintenance = true

	probes := []degraded.Probe{
		maintenanceDown,                    // skipped entirely
		probe(2, models.ResultStatusUp, 10), // slot 1
		probe(3, models.ResultStatusDown, 0), // slot 2
		probe(4, models.ResultStatusDown, 0), // slot 3
	}

	out := degraded.Evaluate(probes, params, now)

	r.Equal(3, out.Failure.Slots, "the maintenance probe must not consume a slot")
	r.Equal(2, out.Failure.Matches, "the maintenance failure must not be counted")
	r.True(out.Failure.Fired, "the two real failures still reach the window")

	// A lifecycle marker and an abandoned row are non-slots for the same reason.
	probes = []degraded.Probe{
		probe(1, models.ResultStatusRunning, 0),
		probe(2, models.ResultStatusAbandoned, 0),
		probe(3, models.ResultStatusUp, 10),
	}

	out = degraded.Evaluate(probes, params, now)
	r.Equal(1, out.Failure.Slots)
	r.Equal(0, out.Failure.Matches)
}

// TestEvaluateSlowRule covers the population nothing else in the product reports.
func TestEvaluateSlowRule(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	params := defaultParams()
	params.SlowThreshold = 1000

	// The motivating episode's opening triple: 2565 ms, 1362 ms, 5534 ms, all
	// SUCCESSFUL, and no failure anywhere yet.
	probes := append(
		[]degraded.Probe{
			probe(1, models.ResultStatusUp, 5534),
			probe(2, models.ResultStatusUp, 1362),
			probe(3, models.ResultStatusUp, 2565),
		},
		series(57, models.ResultStatusUp, 453)...,
	)

	out := degraded.Evaluate(probes, params, now)

	r.True(out.Slow.Fired, "3 of the last 6 successful probes were over 1000 ms")
	r.Equal(3, out.Slow.Matches)
	r.False(out.Failure.Fired, "the failure rule is still silent — this is the point of two rules")
	r.True(out.Firing())
	r.Equal(6, out.ResolveWindow, "only the slow rule fired, so its N governs")

	// A zero threshold is the documented off state, whatever M and N say.
	params.SlowThreshold = 0
	out = degraded.Evaluate(probes, params, now)
	r.False(out.Slow.Active)
	r.False(out.Firing())

	// A failed probe is never also slow, even with a huge duration: it belongs to
	// the failure population and double-counting a timeout would trip both rules.
	params.SlowThreshold = 1000
	out = degraded.Evaluate(append(
		series(6, models.ResultStatusTimeout, 30000),
		series(54, models.ResultStatusUp, 100)...,
	), params, now)
	r.Equal(0, out.Slow.Matches)
}

// TestEvaluateResolution pins the resolution rule: the condition must have been
// false for N consecutive countable probes, and when both rules fired the larger
// N governs.
func TestEvaluateResolution(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	params := defaultParams()
	params.SlowThreshold = 1000

	// episode builds `clean` recovered probes in front of the motivating
	// episode's slow triple, with the rest of the hour healthy.
	episode := func(clean int) []degraded.Probe {
		out := series(clean, models.ResultStatusUp, 200)
		out = append(out,
			probe(clean+1, models.ResultStatusUp, 5534),
			probe(clean+2, models.ResultStatusUp, 1362),
			probe(clean+3, models.ResultStatusUp, 2565),
		)

		for i := clean + 4; i <= 60; i++ {
			out = append(out, probe(i, models.ResultStatusUp, 200))
		}

		return out
	}

	// Five clean probes: the streak is 5, one short of the slow rule's 6.
	out := degraded.Evaluate(episode(5), params, now)
	r.Equal(5, out.CleanStreak)
	r.False(out.Firing(), "the slow probes have aged out of the 6-probe window")

	// One more clean probe and the streak reaches the window.
	out = degraded.Evaluate(episode(6), params, now)
	r.Equal(6, out.CleanStreak)

	// Both rules firing: the larger window governs resolution.
	both := append(
		[]degraded.Probe{
			probe(1, models.ResultStatusUp, 5534),
			probe(2, models.ResultStatusUp, 1362),
			probe(3, models.ResultStatusUp, 2565),
			probe(4, models.ResultStatusDown, 0),
			probe(5, models.ResultStatusDown, 0),
			probe(6, models.ResultStatusDown, 0),
			probe(7, models.ResultStatusDown, 0),
			probe(8, models.ResultStatusDown, 0),
		},
		series(52, models.ResultStatusUp, 200)...,
	)

	out = degraded.Evaluate(both, params, now)
	r.True(out.Failure.Fired)
	r.True(out.Slow.Fired)
	r.Equal(60, out.ResolveWindow, "the failure rule's 60 governs, not the slow rule's 6")
	r.Equal(0, out.CleanStreak)
}

// TestEvaluateStartedAt pins that the incident's start is the oldest matching
// probe in the window, not the instant the evaluator noticed.
func TestEvaluateStartedAt(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	params := defaultParams()
	params.Failures = 2
	params.FailuresWindow = 10

	probes := append(
		[]degraded.Probe{
			probe(1, models.ResultStatusDown, 0),
			probe(2, models.ResultStatusUp, 100),
			probe(3, models.ResultStatusUp, 100),
			probe(4, models.ResultStatusDown, 0),
		},
		series(6, models.ResultStatusUp, 100)...,
	)

	out := degraded.Evaluate(probes, params, now)

	r.True(out.Failure.Fired)
	r.Equal(now.Add(-4*time.Minute), out.StartedAt())
	r.False(out.WindowStart.IsZero())
	r.Equal(now, out.WindowEnd)
}
