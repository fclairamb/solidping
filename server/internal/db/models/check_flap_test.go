package models_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

// flapCheck builds an in-memory Check carrying the given flap window/count +
// raw state, mirroring the incidents package's own test helper of the same
// name (server/internal/handlers/incidents/service_test.go) so the two
// suites stay easy to compare. RecoveryPeriodSeconds/FlapBackoffFactor/
// MaxRecoveryMultiplier are pinned (2min / F=2 / cap=8) — the recovery-period
// math itself (worked example, cap, off-switches) is covered by the
// incidents package's own tests; these tests are about flap-count/window
// behavior only.
func flapCheck(window, flapCount int, lastOutageAt *time.Time) *models.Check {
	return &models.Check{
		RecoveryPeriodSeconds: 120,
		FlappingWindowSeconds: window,
		FlapBackoffFactor:     2,
		MaxRecoveryMultiplier: 8,
		FlapCount:             flapCount,
		LastOutageAt:          lastOutageAt,
	}
}

// TestEffectiveFlapCount is the acceptance test for the lazy-reset trap (spec
// 2026-08-24-05): flap_count only resets to 0 at the NEXT outage onset, so a
// check whose last outage is older than the flapping window must still
// report an EFFECTIVE count of 0, even though the raw column is stale and
// nonzero.
func TestEffectiveFlapCount(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	const window = 21600 // 6h
	now := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)

	tests := []struct {
		name         string
		window       int
		flapCount    int
		lastOutageAt *time.Time
		want         int
	}{
		{
			name:         "no outage ever recorded",
			window:       window,
			flapCount:    0,
			lastOutageAt: nil,
			want:         0,
		},
		{
			name:         "stale nonzero flap_count but no last_outage_at is still 0",
			window:       window,
			flapCount:    4,
			lastOutageAt: nil,
			want:         0,
		},
		{
			name:         "fresh outage inside the window reports the raw count",
			window:       window,
			flapCount:    3,
			lastOutageAt: timePtr(now.Add(-1 * time.Hour)),
			want:         3,
		},
		{
			name:         "outage exactly at the window boundary still counts",
			window:       window,
			flapCount:    2,
			lastOutageAt: timePtr(now.Add(-window * time.Second)),
			want:         2,
		},
		{
			name: "THE LAZY-RESET TRAP: window lapsed reports 0 despite a " +
				"stale nonzero raw flap_count",
			window:       window,
			flapCount:    4,
			lastOutageAt: timePtr(now.Add(-12 * time.Hour)),
			want:         0,
		},
		{
			name:         "flapping window disabled (feature off) is always 0",
			window:       0,
			flapCount:    5,
			lastOutageAt: timePtr(now.Add(-1 * time.Minute)),
			want:         0,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			check := flapCheck(tc.window, tc.flapCount, tc.lastOutageAt)
			r.Equal(tc.want, check.EffectiveFlapCount(now))
		})
	}
}

// TestEffectiveRecoveryPeriodAt proves the display-facing variant is driven
// by the EFFECTIVE (lazy-reset-aware) flap count, not the raw column — the
// same trap as TestEffectiveFlapCount, but through the recovery-period math a
// check-detail page would actually render.
func TestEffectiveRecoveryPeriodAt(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	const window = 21600 // 6h
	now := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)

	t.Run("fresh flap escalates the recovery period", func(t *testing.T) {
		t.Parallel()
		check := flapCheck(window, 1, timePtr(now.Add(-time.Minute)))
		r.Equal(4*time.Minute, check.EffectiveRecoveryPeriodAt(now),
			"flapCount=1 -> R*F^1 = 2m*2 = 4m")
	})

	t.Run("lapsed window falls back to the constant base period", func(t *testing.T) {
		t.Parallel()
		// Stale flapCount=4 would escalate if read raw; the window lapsed
		// 12h ago so the effective period must be the plain base R.
		check := flapCheck(window, 4, timePtr(now.Add(-12*time.Hour)))
		r.Equal(2*time.Minute, check.EffectiveRecoveryPeriodAt(now),
			"lapsed window -> effective flapCount 0 -> constant base R")
	})

	t.Run("matches EffectiveRecoveryPeriod when the raw count is still fresh", func(t *testing.T) {
		t.Parallel()
		check := flapCheck(window, 2, timePtr(now.Add(-time.Minute)))
		r.Equal(check.EffectiveRecoveryPeriod(), check.EffectiveRecoveryPeriodAt(now))
	})
}

// TestFlappingWindowElapsed covers the shared lazy-reset predicate directly,
// including the boundary case (exactly `window` old counts as still inside).
func TestFlappingWindowElapsed(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	now := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)

	t.Run("nil last outage has elapsed", func(t *testing.T) {
		t.Parallel()
		check := &models.Check{FlappingWindowSeconds: 3600}
		r.True(check.FlappingWindowElapsed(now))
	})

	t.Run("window disabled always reports elapsed", func(t *testing.T) {
		t.Parallel()
		check := &models.Check{FlappingWindowSeconds: 0, LastOutageAt: timePtr(now)}
		r.True(check.FlappingWindowElapsed(now))
	})

	t.Run("exactly at the boundary has not elapsed", func(t *testing.T) {
		t.Parallel()
		check := &models.Check{FlappingWindowSeconds: 3600, LastOutageAt: timePtr(now.Add(-time.Hour))}
		r.False(check.FlappingWindowElapsed(now))
	})

	t.Run("one second past the boundary has elapsed", func(t *testing.T) {
		t.Parallel()
		check := &models.Check{
			FlappingWindowSeconds: 3600,
			LastOutageAt:          timePtr(now.Add(-time.Hour - time.Second)),
		}
		r.True(check.FlappingWindowElapsed(now))
	})
}

func timePtr(t time.Time) *time.Time {
	return &t
}
