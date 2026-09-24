package checks_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	entcore "github.com/fclairamb/solidping/server/internal/entitlements"
	"github.com/fclairamb/solidping/server/internal/handlers/checks"
	"github.com/fclairamb/solidping/server/internal/notifier"
	"github.com/fclairamb/solidping/server/internal/regions"
)

// Automatic region placement on the write paths (spec 2026-09-25-06 A1/A3).

type placementWorld struct {
	svc *checks.Service
	db  *sqlite.Service
	org *models.Organization
}

// newPlacementWorld declares four cloud regions and the system default
// ["gravelines"], with a live worker in each region named in liveRegions.
// tokyo's worker reports no headless Chrome. maxPerMinute (0 = unlimited)
// caps the org's checks-per-minute.
func newPlacementWorld(t *testing.T, maxPerMinute int, liveRegions ...string) *placementWorld {
	t.Helper()

	r := require.New(t)
	ctx := t.Context()

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	r.NoError(dbSvc.SetSystemParameter(ctx, regions.ParamRegions, []regions.RegionDefinition{
		{Slug: "tokyo"}, {Slug: "gravelines"}, {Slug: "lauterbourg"}, {Slug: "paris"},
	}, false))
	r.NoError(dbSvc.SetSystemParameter(ctx, regions.ParamDefaultRegions, []string{"gravelines"}, false))

	for _, region := range liveRegions {
		worker := models.NewWorker("w-"+region, "w-"+region)
		slug := region
		worker.Region = &slug
		registered, regErr := dbSvc.RegisterOrUpdateWorker(ctx, worker)
		r.NoError(regErr)

		capabilities := []string{models.CapabilityIPv4, models.CapabilityIPv6, models.CapabilityBrowser}
		if region == "tokyo" {
			capabilities = []string{models.CapabilityIPv4, models.CapabilityIPv6}
		}

		r.NoError(dbSvc.UpdateWorkerHeartbeat(ctx, registered.UID, capabilities, ""))
	}

	org := models.NewOrganization("placement-org", "Placement Org")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	entSvc := entcore.NewService(dbSvc, entcore.DefaultsFor(config.DeploymentModeSelfHosted), 0)
	if maxPerMinute > 0 {
		r.NoError(entSvc.Set(ctx, org.UID, entcore.Entitlements{
			Limits: entcore.Limits{MaxChecksPerMinute: entcore.Int(maxPerMinute)},
			Source: models.EntitlementSourceAdmin,
		}, "user:test", ""))
	}

	return &placementWorld{
		svc: checks.NewService(dbSvc, notifier.NewLocalEventNotifier(), disabledCreds(t), entSvc),
		db:  dbSvc,
		org: org,
	}
}

func (w *placementWorld) create(t *testing.T, req checks.CreateCheckRequest) checks.CheckResponse {
	t.Helper()

	if req.Type == "" {
		req.Type = "http"
	}

	if req.Config == nil {
		req.Config = map[string]any{"url": "https://acme.com"}
	}

	resp, err := w.svc.CreateCheck(t.Context(), w.org.Slug, req)
	require.NoError(t, err)

	return resp
}

func (w *placementWorld) jobRegions(t *testing.T, uid string) []string {
	t.Helper()

	jobs, err := w.db.ListCheckJobsByCheckUID(t.Context(), uid)
	require.NoError(t, err)

	out := make([]string, 0, len(jobs))
	for _, job := range jobs {
		if job.Region != nil {
			out = append(out, *job.Region)
		}
	}

	return out
}

func TestNewCheckIsPlacedAutomaticallyOnTwoHealthyRegions(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	w := newPlacementWorld(t, 0, "tokyo", "gravelines", "paris")

	resp := w.create(t, checks.CreateCheckRequest{})
	r.Equal(models.PlacementAuto, resp.Placement)
	r.NotNil(resp.RegionCount)
	r.Equal(2, *resp.RegionCount)
	r.Nil(resp.RegionPool)
	// System default first, then the declared order — lauterbourg has no live
	// worker, so the next healthy one (paris) is taken instead.
	r.Equal([]string{"gravelines", "tokyo"}, resp.Regions)
	r.ElementsMatch([]string{"gravelines", "tokyo"}, w.jobRegions(t, resp.UID))

	// A browser check skips tokyo, which has no headless Chrome.
	browser := w.create(t, checks.CreateCheckRequest{
		Type: "browser", Config: map[string]any{"url": "https://acme.com"},
	})
	r.Equal([]string{"gravelines", "paris"}, browser.Regions)

	// Deterministic: the same inputs give the same placement.
	again := w.create(t, checks.CreateCheckRequest{})
	r.Equal(resp.Regions, again.Regions)
}

func TestExplicitRegionsStayPinned(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	w := newPlacementWorld(t, 0, "gravelines", "paris")

	resp := w.create(t, checks.CreateCheckRequest{Regions: []string{"lauterbourg"}})
	r.Equal(models.PlacementPinned, resp.Placement)
	r.Equal([]string{"lauterbourg"}, resp.Regions, "an explicit list is pinned, healthy or not")
	r.Nil(resp.RegionCount)

	pinnedDefault := w.create(t, checks.CreateCheckRequest{Placement: strPtr(models.PlacementPinned)})
	r.Equal(models.PlacementPinned, pinnedDefault.Placement)
	r.Equal([]string{"gravelines"}, pinnedDefault.Regions, "pinned without regions resolves the defaults as before")
}

func TestPoolAndCountAreHonored(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	w := newPlacementWorld(t, 0, "tokyo", "gravelines", "lauterbourg", "paris")

	three := 3
	resp := w.create(t, checks.CreateCheckRequest{
		RegionCount: &three, RegionPool: []string{"paris", "lauterbourg"},
	})
	r.Equal(models.PlacementAuto, resp.Placement, "regionCount / regionPool imply auto")
	r.Equal([]string{"lauterbourg", "paris"}, resp.Regions)
	r.Equal(2, *resp.RegionCount, "N is capped by the pool")
	r.Equal([]string{"paris", "lauterbourg"}, resp.RegionPool)
	r.NotEmpty(resp.Warnings)
	r.Equal(checks.CodePlacementRegionCountReduced, resp.Warnings[len(resp.Warnings)-1].Code)
}

func TestPlacementRequestContradictionsAreRejected(t *testing.T) {
	t.Parallel()

	w := newPlacementWorld(t, 0, "gravelines")
	two := 2
	zero := 0

	cases := []struct {
		name string
		req  checks.CreateCheckRequest
	}{
		{"unknown placement", checks.CreateCheckRequest{Placement: strPtr("sometimes")}},
		{"auto with regions", checks.CreateCheckRequest{
			Placement: strPtr(models.PlacementAuto), Regions: []string{"paris"},
		}},
		{"regions with a count", checks.CreateCheckRequest{Regions: []string{"paris"}, RegionCount: &two}},
		{"pinned with a count", checks.CreateCheckRequest{Placement: strPtr(models.PlacementPinned), RegionCount: &two}},
		{"count below 1", checks.CreateCheckRequest{RegionCount: &zero}},
		{"private pool", checks.CreateCheckRequest{RegionPool: []string{"@office"}}},
		{"unknown pool", checks.CreateCheckRequest{RegionPool: []string{"mars"}}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			req := tc.req
			req.Type = "http"
			req.Config = map[string]any{"url": "https://acme.com"}

			_, err := w.svc.CreateCheck(t.Context(), w.org.Slug, req)
			require.Error(t, err)
		})
	}
}

// TestRegionCountIsCappedByTheChecksPerMinuteLimit: N is reduced to what the
// org's maxChecksPerMinute allows, and the response says so.
func TestRegionCountIsCappedByTheChecksPerMinuteLimit(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	// A 1-minute check costs 1 run/minute per region: a limit of 1 fits one.
	w := newPlacementWorld(t, 1, "gravelines", "paris")

	resp := w.create(t, checks.CreateCheckRequest{})
	r.Equal(models.PlacementAuto, resp.Placement)
	r.Equal([]string{"gravelines"}, resp.Regions)
	r.Equal(1, *resp.RegionCount)

	var reduced *string

	for i := range resp.Warnings {
		if resp.Warnings[i].Code == checks.CodePlacementRegionCountReduced {
			reduced = &resp.Warnings[i].Message
		}
	}

	r.NotNil(reduced, "the response says the count was reduced")
	r.Contains(*reduced, "limit of 1 checks/minute")

	// Positive control: the same org with room for two gets two.
	roomy := newPlacementWorld(t, 10, "gravelines", "paris")
	r.Len(roomy.create(t, checks.CreateCheckRequest{}).Regions, 2)
}

func TestOrgDefaultComesFirstInTheCandidateOrder(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	w := newPlacementWorld(t, 0, "gravelines", "paris")
	r.NoError(w.db.SetOrgParameter(t.Context(), w.org.UID, regions.ParamDefaultRegions, []string{"paris"}, false))

	resp := w.create(t, checks.CreateCheckRequest{})
	r.Equal(models.PlacementAuto, resp.Placement)
	r.Equal([]string{"paris", "gravelines"}, resp.Regions, "org default, then system default")
}

// TestPrivateOrgDefaultKeepsThePinnedResolution: an org that made its own
// private location the default keeps getting pinned checks there — auto never
// places into a private region.
func TestPrivateOrgDefaultKeepsThePinnedResolution(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	w := newPlacementWorld(t, 0, "gravelines", "paris")
	r.NoError(w.db.SetOrgParameter(t.Context(), w.org.UID, regions.ParamDefaultRegions, []string{"@office"}, false))

	resp := w.create(t, checks.CreateCheckRequest{})
	r.Equal(models.PlacementPinned, resp.Placement)
	r.Equal([]string{"@office"}, resp.Regions)
}

func TestUpdateSwitchesPlacement(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := t.Context()
	w := newPlacementWorld(t, 0, "gravelines", "lauterbourg", "paris")

	pinned := w.create(t, checks.CreateCheckRequest{Regions: []string{"paris"}})

	// placement: auto keeps the current region and the current count.
	auto, err := w.svc.UpdateCheck(ctx, w.org.Slug, pinned.UID, &checks.UpdateCheckRequest{
		Placement: strPtr(models.PlacementAuto),
	})
	r.NoError(err)
	r.Equal(models.PlacementAuto, auto.Placement)
	r.Equal([]string{"paris"}, auto.Regions, "switching keeps a still-good placement")
	r.Equal(1, *auto.RegionCount)

	// regionCount grows it.
	two := 2
	grown, err := w.svc.UpdateCheck(ctx, w.org.Slug, pinned.UID, &checks.UpdateCheckRequest{RegionCount: &two})
	r.NoError(err)
	r.Equal([]string{"paris", "gravelines"}, grown.Regions)
	r.ElementsMatch([]string{"paris", "gravelines"}, w.jobRegions(t, pinned.UID))

	// An explicit region list pins it again.
	repinned, err := w.svc.UpdateCheck(ctx, w.org.Slug, pinned.UID, &checks.UpdateCheckRequest{
		Regions: &[]string{"lauterbourg"},
	})
	r.NoError(err)
	r.Equal(models.PlacementPinned, repinned.Placement)
	r.Equal([]string{"lauterbourg"}, repinned.Regions)
	r.Nil(repinned.RegionCount)
	r.Equal([]string{"lauterbourg"}, w.jobRegions(t, pinned.UID))

	// placement: pinned alone freezes the current placement.
	backToAuto, err := w.svc.UpdateCheck(ctx, w.org.Slug, pinned.UID, &checks.UpdateCheckRequest{
		Regions: &[]string{},
	})
	r.NoError(err)
	r.Equal(models.PlacementAuto, backToAuto.Placement, "regions: [] is back to the default placement")
	r.Equal(2, *backToAuto.RegionCount)

	frozen, err := w.svc.UpdateCheck(ctx, w.org.Slug, pinned.UID, &checks.UpdateCheckRequest{
		Placement: strPtr(models.PlacementPinned),
	})
	r.NoError(err)
	r.Equal(models.PlacementPinned, frozen.Placement)
	r.Equal(backToAuto.Regions, frozen.Regions)

	// A plain rename touches nothing about the placement.
	renamed, err := w.svc.UpdateCheck(ctx, w.org.Slug, pinned.UID, &checks.UpdateCheckRequest{Name: strPtr("Renamed")})
	r.NoError(err)
	r.Equal(frozen.Regions, renamed.Regions)
	r.Equal(models.PlacementPinned, renamed.Placement)
}

// TestPassiveChecksAreNeverAutoPlaced: a heartbeat has no region to place.
func TestPassiveChecksAreNeverAutoPlaced(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	w := newPlacementWorld(t, 0, "gravelines", "paris")

	resp := w.create(t, checks.CreateCheckRequest{Type: "heartbeat", Config: map[string]any{}})
	r.Equal(models.PlacementPinned, resp.Placement)
	r.Empty(resp.Regions)
	r.Nil(resp.RegionCount)
}
