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

// The day bar's tooltip says how much of each day was actually measured,
// region-independently (spec 2026-11-08-05): a day counts as measured when
// some region reported every period, however many regions the check runs in
// now; a period with no result from any region is unmeasured.
func TestApplyCoverage(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	day := time.Date(2026, 9, 24, 0, 0, 0, 0, time.UTC)
	now := day.Add(48 * time.Hour)

	check := &models.Check{
		UID:       "chk-1",
		Period:    timeutils.Duration(time.Minute),
		Regions:   []string{"eu-west", "lauterbourg"},
		CreatedAt: day.Add(-30 * 24 * time.Hour),
	}

	gapped := &models.Check{
		UID:       "chk-2",
		Period:    timeutils.Duration(time.Minute),
		CreatedAt: day.Add(-30 * 24 * time.Hour),
	}

	byCheck := map[string]map[time.Time]uptimebar.BucketStats{
		// Two regions now, lauterbourg dark for 8h: eu-west still reported
		// every minute, so the day was measured.
		"chk-1": {day: {Up: 2400, Total: 2400}},
		// One region, silent for 2 of the 24 hours: 22h of minutes.
		"chk-2": {day: {Up: 1320, Total: 1320}},
	}

	single := map[string]map[time.Time]uptimebar.BucketStats{"chk-1": byCheck["chk-1"]}

	data := buildAvailabilityData(byCheck["chk-1"], nil, day.Add(24*time.Hour), 2, true, false, 99.9, 99.0)
	applyCoverage(data, single, 24*time.Hour, []*models.Check{check}, now)

	r.Len(data.DailyAvailability, 2)

	measured := data.DailyAvailability[0]
	r.NotNil(measured.CoveragePct)
	r.InDelta(100.0, *measured.CoveragePct, 0.01, "a silent second region is not unmeasured time")

	silent := data.DailyAvailability[1]
	r.NotNil(silent.CoveragePct)
	r.Zero(*silent.CoveragePct, "a day with no probe at all was not measured")

	gapData := buildAvailabilityData(byCheck["chk-2"], nil, day.Add(24*time.Hour), 2, true, false, 99.9, 99.0)
	applyCoverage(gapData, byCheck, 24*time.Hour, []*models.Check{gapped}, now)
	r.NotNil(gapData.DailyAvailability[0].CoveragePct)
	r.InDelta(22.0/24.0*100, *gapData.DailyAvailability[0].CoveragePct, 0.01, "a real 2h gap reads 2h short")

	// A resource with both members: one fully measured, one 2h short. The
	// first's second region cannot fill the second's gap.
	merged := mergeBuckets(byCheck, []string{"chk-1", "chk-2"})
	both := buildAvailabilityData(merged, nil, day.Add(24*time.Hour), 2, true, false, 99.9, 99.0)
	applyCoverage(both, byCheck, 24*time.Hour, []*models.Check{check, gapped}, now)
	r.NotNil(both.DailyAvailability[0].CoveragePct)
	r.InDelta((24.0+22.0)/48.0*100, *both.DailyAvailability[0].CoveragePct, 0.01)
}
