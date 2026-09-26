package statuspages

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/uptimebar"
	"github.com/fclairamb/solidping/server/internal/utils/timeutils"
)

// A stale component is spoken as "stale" on the public wire — the front end
// renders it as the neutral "No data, last checked …" — and never as "up"
// (spec 2026-09-25-02). A stale GROUP reads the same, since the group rollup
// yields CheckStatusStale for an all-stale (or stale-and-up) group.
func TestPublicCheckStatus_Stale(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	r.Equal("stale", publicCheckStatus(models.CheckStatusStale))
	r.NotEqual(statusUp, publicCheckStatus(models.CheckStatusStale))

	group := models.RollupGroupStatus(map[models.CheckStatus]int{models.CheckStatusStale: 3})
	r.Equal("stale", publicCheckStatus(group))

	// Validating stays hidden as up; created stays created: unchanged.
	r.Equal(statusUp, publicCheckStatus(models.CheckStatusValidating))
	r.Equal(statusCreated, publicCheckStatus(models.CheckStatusCreated))
}

// The day bar's tooltip says how much of each day was actually measured
// (spec 2026-09-25-02): probes received ÷ probes expected.
func TestApplyCoverage(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	day := time.Date(2026, 9, 24, 0, 0, 0, 0, time.UTC)
	now := day.Add(48 * time.Hour)

	check := &models.Check{
		Period:    timeutils.Duration(time.Minute),
		Regions:   []string{"eu-west", "lauterbourg"},
		CreatedAt: day.Add(-30 * 24 * time.Hour),
	}

	byBucket := map[time.Time]uptimebar.BucketStats{
		// Two regions × 1440 minutes expected; lauterbourg was dark for 8h.
		day: {Up: 2400, Total: 2400},
	}

	data := buildAvailabilityData(byBucket, nil, day.Add(24*time.Hour), 2, true, false, 99.9, 99.0)
	applyCoverage(data, byBucket, 24*time.Hour, []*models.Check{check}, now)

	r.Len(data.DailyAvailability, 2)

	measured := data.DailyAvailability[0]
	r.NotNil(measured.CoveragePct)
	r.InDelta(2400.0/2880.0*100, *measured.CoveragePct, 0.01)

	silent := data.DailyAvailability[1]
	r.NotNil(silent.CoveragePct)
	r.Zero(*silent.CoveragePct, "a day with no probe at all was not measured")
}
