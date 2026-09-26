package models_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/utils/timeutils"
)

func TestExpectedProbesAndCoverage(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	r.InDelta(1440, models.ExpectedProbes(24*time.Hour, time.Minute, 0), 0.001)
	r.InDelta(4320, models.ExpectedProbes(24*time.Hour, time.Minute, 3), 0.001)
	r.Zero(models.ExpectedProbes(0, time.Minute, 1))
	r.Zero(models.ExpectedProbes(time.Hour, 0, 1))

	// An 8h gap in 24h of one-minute probes: 960 of 1440 → two thirds.
	coverage, ok := models.Coverage(960, models.ExpectedProbes(24*time.Hour, time.Minute, 1))
	r.True(ok)
	r.InDelta(2.0/3.0, coverage, 0.001)

	coverage, ok = models.Coverage(2000, 1440)
	r.True(ok)
	r.InDelta(1.0, coverage, 0.0001, "more results than expected is full coverage, never more")

	_, ok = models.Coverage(10, 0)
	r.False(ok)

	created := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)
	check := &models.Check{Period: timeutils.Duration(time.Minute), CreatedAt: created}
	dayStart := time.Date(2026, 9, 24, 0, 0, 0, 0, time.UTC)
	now := time.Date(2026, 9, 24, 18, 0, 0, 0, time.UTC)

	r.InDelta(360, check.ExpectedProbesBetween(dayStart, dayStart.Add(24*time.Hour), now), 0.001,
		"clamped to [createdAt, now)")
}

func TestStaleThreshold(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	r.Equal(5*time.Minute, models.StaleThreshold(10*time.Second))
	r.Equal(5*time.Minute, models.StaleThreshold(time.Minute))
	r.Equal(30*time.Minute, models.StaleThreshold(10*time.Minute))
}
