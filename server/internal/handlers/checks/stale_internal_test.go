package checks

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/utils/timeutils"
)

// The stale status is a first-class filter token and stats key (spec
// 2026-09-25-02): `?status=stale` used to answer 400, and a stale check must
// not be counted as "unknown" or, worse, down.
func TestStaleStatusFilterAndStats(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	statuses, err := parseStatusFilter("up,stale")
	r.NoError(err)
	r.Equal([]models.CheckStatus{models.CheckStatusUp, models.CheckStatusStale}, statuses)

	got := foldCheckStatusCounts([]models.CheckStatusCount{
		{Status: models.CheckStatusStale, Enabled: true, Count: 12},
		{Status: models.CheckStatusUp, Enabled: true, Count: 3},
	})

	r.Equal(12, got.ByStatus[models.WireStatusStale])
	r.Zero(got.ByStatus[models.WireStatusUnknown])
	r.Zero(got.Down, "stale is not down")
}

func TestStatusChangeLabel(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	r.Equal("no data", StatusChangeLabel("STALE"))
	r.Equal("UP", StatusChangeLabel("UP"))
	r.Equal("DOWN", StatusChangeLabel("DOWN"))
}

// Per-region freshness lists every region that reported, plus every configured
// region that did not, and flags the silent ones.
func TestBuildRegionFreshness(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	now := time.Date(2026, 9, 24, 21, 41, 0, 0, time.UTC)

	check := &models.Check{
		Period:  timeutils.Duration(time.Minute),
		Regions: []string{"eu-west", "lauterbourg", "us-east"},
	}

	rows := []models.RegionLastResult{
		{Region: "eu-west", LastResultAt: now.Add(-time.Minute)},
		{Region: "lauterbourg", LastResultAt: now.Add(-8 * time.Hour)},
	}

	got := BuildRegionFreshness(check, rows, now)
	r.Len(got, 3)

	r.Equal("eu-west", got[0].Region)
	r.False(got[0].Stale)

	r.Equal("lauterbourg", got[1].Region)
	r.True(got[1].Stale)
	r.NotNil(got[1].LastResultAt)

	r.Equal("us-east", got[2].Region)
	r.True(got[2].Stale, "a configured region with no result at all is silent")
	r.Nil(got[2].LastResultAt)
}
