package checkjobsvc

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/utils/timeutils"
)

// TestClampAhead covers the D3 bounded claim-ahead formula (spec
// 2026-07-05-08): window = min(FetchMaxAhead, job.Period/2, 30s).
func TestClampAhead(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		maxAhead time.Duration
		period   timeutils.Duration
		want     time.Duration
	}{
		{
			name:     "ShortPeriodClampsBelowFloor",
			maxAhead: 5 * time.Minute,
			period:   timeutils.Duration(time.Minute), // period/2 = 30s = floor
			want:     30 * time.Second,
		},
		{
			name:     "VeryShortPeriodClampsBelowFloor",
			maxAhead: 5 * time.Minute,
			period:   timeutils.Duration(20 * time.Second), // period/2 = 10s < floor
			want:     10 * time.Second,
		},
		{
			name:     "LongPeriodClampsToFloor",
			maxAhead: 5 * time.Minute,
			period:   timeutils.Duration(time.Hour), // period/2 = 30m, way above floor
			want:     maxClaimAheadFloor,
		},
		{
			name:     "SmallMaxAheadOverrideWins",
			maxAhead: 5 * time.Second, // operator override tighter than the floor
			period:   timeutils.Duration(time.Hour),
			want:     5 * time.Second,
		},
		{
			name:     "MaxAheadTighterThanPeriodHalf",
			maxAhead: 10 * time.Second,
			period:   timeutils.Duration(time.Minute), // period/2 = 30s > maxAhead
			want:     10 * time.Second,
		},
		{
			name:     "ZeroPeriodDegradesToMaxAheadFloor",
			maxAhead: 5 * time.Minute,
			period:   0,
			want:     maxClaimAheadFloor,
		},
		{
			name:     "NegativePeriodDegradesToMaxAheadFloor",
			maxAhead: 5 * time.Minute,
			period:   timeutils.Duration(-time.Minute),
			want:     maxClaimAheadFloor,
		},
		{
			name:     "ExactlyAtFloor",
			maxAhead: 5 * time.Minute,
			period:   timeutils.Duration(time.Minute), // period/2 == floor exactly
			want:     30 * time.Second,
		},
		{
			name:     "MultiRegionSplitPeriodThreeMinutes",
			maxAhead: 5 * time.Minute,
			period:   timeutils.Duration(3 * time.Minute), // 3 regions x 1m base -> period/2=90s > floor
			want:     maxClaimAheadFloor,
		},
		{
			// A 2-week (336h) period — domain's new long-interval option
			// (spec 2026-08-15-07) — must clamp to the SAME 30s floor as any
			// other long period, not blow up or degrade to something else
			// once period/2 measures in days instead of minutes.
			name:     "TwoWeekPeriodStillClampsToFloor",
			maxAhead: 5 * time.Minute,
			period:   timeutils.Duration(336 * time.Hour), // period/2 = 168h, way above floor
			want:     maxClaimAheadFloor,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got := clampAhead(tc.maxAhead, tc.period)
			require.Equal(t, tc.want, got)
		})
	}
}
