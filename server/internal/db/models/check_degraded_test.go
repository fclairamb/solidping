package models_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

// The degraded-detection configuration is nullable-with-a-code-default: the five
// numeric columns are NULL when nobody configured them and the documented
// default is resolved at read time. These tests pin the three states the
// accessors have to keep apart — unset, explicitly off, explicitly set — because
// collapsing any two of them is how a rule ends up silently disabled.

func TestEffectiveDegradedResolvesNilToDefaults(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	// A zero-value Check is the bypassing insert path: nothing was configured,
	// so every accessor must answer with the documented default and NOT with the
	// Go zero value.
	check := &models.Check{}

	r.Equal(5, check.EffectiveDegradedFailures())
	r.Equal(60, check.EffectiveDegradedFailuresWindow())
	r.Equal(3, check.EffectiveDegradedSlow())
	r.Equal(6, check.EffectiveDegradedSlowWindow())
	r.Equal(0, check.EffectiveSlowThresholdMs())

	// The constants are the contract the migration's column comments quote.
	r.Equal(5, models.DefaultDegradedFailures)
	r.Equal(60, models.DefaultDegradedFailuresWindow)
	r.Equal(3, models.DefaultDegradedSlow)
	r.Equal(6, models.DefaultDegradedSlowWindow)
	r.Equal(0, models.DefaultSlowThresholdMs)
}

func TestEffectiveDegradedKeepsExplicitZero(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	// 0 is the documented way to turn a rule off. An accessor that treated it as
	// "unset" and substituted 5 would make the rules impossible to disable —
	// the same trap as a `default:5` bun tag, one layer up.
	zero := 0
	check := &models.Check{
		DegradedFailures:   &zero,
		DegradedSlow:       &zero,
		DegradedSlowWindow: &zero,
	}

	r.Equal(0, check.EffectiveDegradedFailures())
	r.Equal(0, check.EffectiveDegradedSlow())
	r.Equal(0, check.EffectiveDegradedSlowWindow())
	// Untouched fields still default.
	r.Equal(60, check.EffectiveDegradedFailuresWindow())
}

func TestEffectiveDegradedReturnsConfiguredValues(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	failures, window, slow, slowWindow, threshold := 7, 40, 4, 12, 2500
	check := &models.Check{
		DegradedFailures:       &failures,
		DegradedFailuresWindow: &window,
		DegradedSlow:           &slow,
		DegradedSlowWindow:     &slowWindow,
		SlowThresholdMs:        &threshold,
	}

	r.Equal(7, check.EffectiveDegradedFailures())
	r.Equal(40, check.EffectiveDegradedFailuresWindow())
	r.Equal(4, check.EffectiveDegradedSlow())
	r.Equal(12, check.EffectiveDegradedSlowWindow())
	r.Equal(2500, check.EffectiveSlowThresholdMs())
}

// TestNewCheckLeavesDegradedNumericsUnset pins that the constructor no longer
// hardcodes 5/60/3/6/0. It must not: writing them in one constructor is what
// made every other insert path a silent 0, and the observable defaults now come
// from the accessors instead.
func TestNewCheckLeavesDegradedNumericsUnset(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	check := models.NewCheck("org-uid", "acme", "http")

	r.Nil(check.DegradedFailures)
	r.Nil(check.DegradedFailuresWindow)
	r.Nil(check.DegradedSlow)
	r.Nil(check.DegradedSlowWindow)
	r.Nil(check.SlowThresholdMs)

	r.Equal(5, check.EffectiveDegradedFailures())
	r.Equal(60, check.EffectiveDegradedFailuresWindow())
	r.Equal(3, check.EffectiveDegradedSlow())
	r.Equal(6, check.EffectiveDegradedSlowWindow())
	r.Equal(0, check.EffectiveSlowThresholdMs())

	// The rollout rule is the one thing NewCheck still writes: ON for a new
	// check, while every pre-existing row keeps the migration's false.
	r.True(check.DegradedEnabled)
}
