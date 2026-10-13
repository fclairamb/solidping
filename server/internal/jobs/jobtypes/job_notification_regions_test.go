package jobtypes_test

import (
	"log/slog"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/jobs/jobdef"
	"github.com/fclairamb/solidping/server/internal/jobs/jobtypes"
	"github.com/fclairamb/solidping/server/internal/regions"
)

func regionsIncident(orgUID string, failing ...any) *models.Incident {
	return &models.Incident{
		OrganizationUID: orgUID,
		Details: models.JSONMap{"regions": map[string]any{
			"failingRegions": failing, "regionCount": float64(3), "failQuorum": float64(2),
		}},
	}
}

func TestResolveRegionInfoUsesOrgCustomRegionLabels(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	jctx, dbSvc, orgUID := newAgentGCTestContext(t)

	r.NoError(regions.NewService(dbSvc).SetOrgCustomRegions(t.Context(), orgUID, []regions.RegionDefinition{
		{Slug: "paris-lab", Emoji: "🏢", Name: "Paris Lab"},
	}))

	info := jobtypes.ResolveRegionInfoForTest(t.Context(), jctx, regionsIncident(orgUID, "@paris-lab", "mars-1"))

	r.NotNil(info)
	r.Equal("🏢 Paris Lab, mars-1 (2 of 3 regions)", info.Text(), "private label resolved, unknown slug kept")
}

func TestResolveRegionInfoNilWithoutRegion(t *testing.T) {
	t.Parallel()

	jctx, _, orgUID := newAgentGCTestContext(t)

	require.Nil(t, jobtypes.ResolveRegionInfoForTest(t.Context(), jctx, &models.Incident{OrganizationUID: orgUID}))
}

func TestResolveRegionInfoFallsBackToSlugsWhenLabelsFail(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	jctx, dbSvc, orgUID := newAgentGCTestContext(t)

	// A broken database: the label lookups fail, the info is still returned.
	r.NoError(dbSvc.Close())

	info := jobtypes.ResolveRegionInfoForTest(t.Context(), &jobdef.JobContext{
		DBService: dbSvc, Logger: slog.Default(), Services: jctx.Services,
	}, regionsIncident(orgUID, "eu-west", "us-east"))

	r.NotNil(info, "a notification is never failed over labels")
	r.Equal("eu-west, us-east (2 of 3 regions)", info.Text())
}
