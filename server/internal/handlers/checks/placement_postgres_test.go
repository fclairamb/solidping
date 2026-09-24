package checks_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/handlers/checks"
	"github.com/fclairamb/solidping/server/internal/regions"
)

// portPlacementPG is distinct from every other _postgres_test.go port.
const portPlacementPG = 15542

// TestAutoPlacementRoundTrip_Postgres is the Postgres twin of the placement
// write path (spec 2026-09-25-06): the placement / region_count / text[]
// region_pool columns written by create and PATCH, and ReplaceAutoChecks'
// `placement = 'auto'` and uuid `uid IN (...)` filters, on the real dialect.
func TestAutoPlacementRoundTrip_Postgres(t *testing.T) {
	t.Parallel()

	svc, dbSvc, org := newReconcilePostgresService(t, portPlacementPG)
	r := require.New(t)
	ctx := t.Context()

	r.NoError(dbSvc.SetSystemParameter(ctx, regions.ParamRegions, []regions.RegionDefinition{
		{Slug: "gravelines"}, {Slug: "lauterbourg"}, {Slug: "paris"},
	}, false))

	two := 2
	created, err := svc.CreateCheck(ctx, org.Slug, checks.CreateCheckRequest{
		Type: "http", Config: map[string]any{"url": "https://acme.com"},
		RegionCount: &two, RegionPool: []string{"lauterbourg", "paris"},
	})
	r.NoError(err)
	r.Equal(models.PlacementAuto, created.Placement)
	r.Equal([]string{"lauterbourg", "paris"}, created.Regions)

	stored, err := dbSvc.GetCheck(ctx, org.UID, created.UID)
	r.NoError(err)
	r.Equal(models.PlacementAuto, stored.Placement)
	r.Equal(2, *stored.RegionCount)
	r.Equal([]string{"lauterbourg", "paris"}, stored.RegionPool)

	// Widen the pool, then move the check off lauterbourg.
	pool := []string{"gravelines", "lauterbourg", "paris"}
	_, err = svc.UpdateCheck(ctx, org.Slug, created.UID, &checks.UpdateCheckRequest{RegionPool: &pool})
	r.NoError(err)

	changes, err := svc.ReplaceAutoChecks(ctx, checks.ReplacementRequest{
		Region: "lauterbourg", CheckUIDs: []string{created.UID},
		Healthy: map[string]bool{"gravelines": true, "paris": true},
		Reason:  models.PlacementReasonRegionOffline,
	})
	r.NoError(err)
	r.Len(changes, 1)
	r.Equal("gravelines", changes[0].To)

	moved, err := dbSvc.GetCheck(ctx, org.UID, created.UID)
	r.NoError(err)
	r.Equal([]string{"gravelines", "paris"}, moved.Regions)

	jobs, err := dbSvc.ListCheckJobsByCheckUID(ctx, created.UID)
	r.NoError(err)
	r.Len(jobs, 2)

	// Back to pinned: the count and the pool are cleared.
	pinned := models.PlacementPinned
	_, err = svc.UpdateCheck(ctx, org.Slug, created.UID, &checks.UpdateCheckRequest{Placement: &pinned})
	r.NoError(err)

	frozen, err := dbSvc.GetCheck(ctx, org.UID, created.UID)
	r.NoError(err)
	r.Equal(models.PlacementPinned, frozen.Placement)
	r.Nil(frozen.RegionCount)
	r.Empty(frozen.RegionPool)
	r.Equal([]string{"gravelines", "paris"}, frozen.Regions)
}
