package regions_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	regionshandler "github.com/fclairamb/solidping/server/internal/handlers/regions"
	"github.com/fclairamb/solidping/server/internal/regionoutage"
)

// TestOrgRegionsCarryOutageStatus: the org regions endpoint reports a cloud
// region the region sweep holds as dark as `offline` with the time it went
// quiet (spec 2026-09-25-03); a stalled region stays `online`, and a private
// region carries no status at all.
func TestOrgRegionsCarryOutageStatus(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := t.Context()
	svc, dbSvc, org := newRegionAPI(t)

	since := time.Date(2026, 9, 24, 13, 41, 0, 0, time.UTC)
	r.NoError(regionoutage.Save(ctx, dbSvc, &regionoutage.Marker{
		Region: "eu", Phase: regionoutage.PhaseDark, Since: since, DetectedAt: since, LastSeenAt: since,
	}))
	r.NoError(regionoutage.Save(ctx, dbSvc, &regionoutage.Marker{
		Region: "us", Phase: regionoutage.PhaseStalled, Since: since, DetectedAt: since, LastSeenAt: since,
	}))

	response, err := svc.ListOrgRegions(ctx, org.Slug)
	r.NoError(err)

	bySlug := make(map[string]regionshandler.RegionResponse, len(response.Data))
	for _, region := range response.Data {
		bySlug[region.Slug] = region
	}

	r.Equal(regionshandler.RegionStatusOffline, bySlug["eu"].Status)
	r.NotNil(bySlug["eu"].OfflineSince)
	r.True(since.Equal(*bySlug["eu"].OfflineSince))

	r.Equal(regionshandler.RegionStatusOnline, bySlug["us"].Status, "stalled is not offline")
	r.Nil(bySlug["us"].OfflineSince)

	global, err := svc.ListGlobalRegions(ctx)
	r.NoError(err)

	for _, region := range global.Data {
		r.Empty(region.Status, "the public endpoint carries no outage status")
	}
}
