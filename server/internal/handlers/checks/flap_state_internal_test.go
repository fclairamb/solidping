package checks

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

// TestBuildFlapStateResponse covers the presence/absence contract for
// CheckResponse.flapState (spec 2026-08-24-05): omitted when the flapping
// feature is off or nothing has accumulated (including the lazy-reset trap —
// a stale raw flap_count from an outage the window has since forgotten),
// present with the effective values otherwise.
func TestBuildFlapStateResponse(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	now := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)

	t.Run("absent: no outage ever recorded", func(t *testing.T) {
		t.Parallel()
		check := models.NewCheck("org", "check", "http")
		r.Nil(buildFlapStateResponse(check, now))
	})

	t.Run("absent: flapping feature off (window=0) despite a raw flap_count", func(t *testing.T) {
		t.Parallel()
		check := models.NewCheck("org", "check", "http")
		check.FlappingWindowSeconds = 0
		check.FlapCount = 3
		last := now.Add(-time.Minute)
		check.LastOutageAt = &last
		r.Nil(buildFlapStateResponse(check, now))
	})

	t.Run("absent: THE LAZY-RESET TRAP - window lapsed hides a stale nonzero flap_count", func(t *testing.T) {
		t.Parallel()
		check := models.NewCheck("org", "check", "http")
		check.FlapCount = 4
		last := now.Add(-12 * time.Hour) // window is the default 6h
		check.LastOutageAt = &last
		r.Nil(buildFlapStateResponse(check, now),
			"a 12h-old outage must not still report flapCount=4")
	})

	t.Run("present: fresh flap reports the effective values", func(t *testing.T) {
		t.Parallel()
		check := models.NewCheck("org", "check", "http")
		check.FlapCount = 1 // 2nd outage within the window
		last := now.Add(-10 * time.Minute)
		check.LastOutageAt = &last

		got := buildFlapStateResponse(check, now)
		r.NotNil(got)
		r.Equal(1, got.FlapCount)
		r.Equal(&last, got.LastOutageAt)
		r.Equal(240, got.EffectiveRecoveryPeriodSeconds, "R=120s * F^1=2 -> 240s")
	})
}

// TestConvertCheckToResponseFlapState confirms buildFlapStateResponse is
// actually wired into the public CheckResponse via convertCheckToResponse,
// not just unit-tested in isolation.
func TestConvertCheckToResponseFlapState(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	svc := &Service{}

	t.Run("omitted on a check with no flap state", func(t *testing.T) {
		t.Parallel()
		check := models.NewCheck("org", "quiet-check", "http")
		resp := svc.convertCheckToResponse(check)
		r.Nil(resp.FlapState)
	})

	t.Run("present on a currently-flapping check", func(t *testing.T) {
		t.Parallel()
		check := models.NewCheck("org", "flapping-check", "http")
		check.FlapCount = 2
		last := time.Now().Add(-time.Minute)
		check.LastOutageAt = &last

		resp := svc.convertCheckToResponse(check)
		r.NotNil(resp.FlapState)
		r.Equal(2, resp.FlapState.FlapCount)
		r.NotZero(resp.FlapState.EffectiveRecoveryPeriodSeconds)
	})
}
