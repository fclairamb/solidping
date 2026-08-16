package regions_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	regionshandler "github.com/fclairamb/solidping/server/internal/handlers/regions"
	"github.com/fclairamb/solidping/server/internal/regions"
)

func newRegionAPI(t *testing.T) (*regionshandler.Service, *sqlite.Service, *models.Organization) {
	t.Helper()

	ctx := t.Context()
	r := require.New(t)

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	org := models.NewOrganization("capsorg", "Caps Org")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	r.NoError(dbSvc.SetSystemParameter(ctx, regions.ParamRegions, []regions.RegionDefinition{
		{Slug: "eu", Emoji: "🇪🇺", Name: "Europe"},
		{Slug: "us", Emoji: "🇺🇸", Name: "United States"},
	}, false))

	return regionshandler.NewService(dbSvc), dbSvc, org
}

func liveWorker(t *testing.T, dbSvc *sqlite.Service, slug, region string, v6 *bool) *models.Worker {
	t.Helper()

	r := require.New(t)
	ctx := t.Context()

	worker, err := dbSvc.RegisterOrUpdateWorker(ctx, &models.Worker{
		UID: uuid.New().String(), Slug: slug, Name: slug, Region: &region, EgressIPv6: v6,
	})
	r.NoError(err)

	_, err = dbSvc.DB().NewUpdate().Model((*models.Worker)(nil)).
		Set("last_active_at = ?", time.Now()).
		Where("uid = ?", worker.UID).Exec(ctx)
	r.NoError(err)

	return worker
}

func yes(v bool) *bool { return &v }

// TestRegionListReportsCapability is AC5's first half: the list endpoint serves
// the three-state capability for every region.
func TestRegionListReportsCapability(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	svc, dbSvc, org := newRegionAPI(t)

	liveWorker(t, dbSvc, "wrk-eu", "eu-west-1", yes(true))
	liveWorker(t, dbSvc, "wrk-us", "us-east-1", yes(false))

	resp, err := svc.ListOrgRegions(t.Context(), org.Slug)
	r.NoError(err)

	byslug := map[string]map[string]string{}
	for _, region := range resp.Data {
		byslug[region.Slug] = region.Capabilities
	}

	r.Equal(regions.CapabilityYes, byslug["eu"][regions.CapabilityIPv6])
	r.Equal(regions.CapabilityNo, byslug["us"][regions.CapabilityIPv6])

	global, err := svc.ListGlobalRegions(t.Context())
	r.NoError(err)
	r.Len(global.Data, 2)

	for _, region := range global.Data {
		r.NotEmpty(region.Capabilities[regions.CapabilityIPv6],
			"the public region list must answer too")
	}
}

// TestRegionListStaysReadableByAnOlderClient is AC5's second half: a client
// that knows nothing about `capabilities` decodes the payload exactly as it did
// before the field existed.
func TestRegionListStaysReadableByAnOlderClient(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	svc, dbSvc, org := newRegionAPI(t)

	liveWorker(t, dbSvc, "wrk-eu", "eu-west-1", yes(true))

	resp, err := svc.ListOrgRegions(t.Context(), org.Slug)
	r.NoError(err)

	encoded, err := json.Marshal(resp)
	r.NoError(err)

	// The pre-2026-08-15 client shape, verbatim.
	var old struct {
		Data []struct {
			Slug  string `json:"slug"`
			Emoji string `json:"emoji"`
			Name  string `json:"name"`
		} `json:"data"`
		DefaultRegions []string `json:"defaultRegions"`
	}

	r.NoError(json.Unmarshal(encoded, &old))
	r.Len(old.Data, 2)
	r.Equal("eu", old.Data[0].Slug)
	r.Equal("Europe", old.Data[0].Name)
	r.Equal("🇪🇺", old.Data[0].Emoji)
}

// TestCapabilitySurvivesHeartbeatCycle is AC1: a reported family persists
// across registration and repeated heartbeats without flapping, for both the
// capable and the incapable worker.
func TestCapabilitySurvivesHeartbeatCycle(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	svc, dbSvc, org := newRegionAPI(t)
	ctx := t.Context()

	capable := liveWorker(t, dbSvc, "wrk-eu", "eu-west-1", yes(true))
	incapable := liveWorker(t, dbSvc, "wrk-us", "us-east-1", yes(false))

	for range 3 {
		r.NoError(dbSvc.UpdateWorkerHeartbeat(ctx, capable.UID, models.WorkerEgress{IPv6: yes(true)}))
		r.NoError(dbSvc.UpdateWorkerHeartbeat(ctx, incapable.UID, models.WorkerEgress{IPv6: yes(false)}))

		resp, err := svc.ListOrgRegions(ctx, org.Slug)
		r.NoError(err)

		for _, region := range resp.Data {
			switch region.Slug {
			case "eu":
				r.Equal(regions.CapabilityYes, region.Capabilities[regions.CapabilityIPv6])
			case "us":
				r.Equal(regions.CapabilityNo, region.Capabilities[regions.CapabilityIPv6])
			}
		}
	}
}

// TestHeartbeatWithoutCapabilityKeepsStoredValue: a heartbeat from a caller
// that cannot answer must not wipe a known capability back to unknown.
func TestHeartbeatWithoutCapabilityKeepsStoredValue(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	svc, dbSvc, org := newRegionAPI(t)
	ctx := t.Context()

	worker := liveWorker(t, dbSvc, "wrk-eu", "eu-west-1", yes(true))

	r.NoError(dbSvc.UpdateWorkerHeartbeat(ctx, worker.UID, models.WorkerEgress{}))

	resp, err := svc.ListOrgRegions(ctx, org.Slug)
	r.NoError(err)

	for _, region := range resp.Data {
		if region.Slug == "eu" {
			r.Equal(regions.CapabilityYes, region.Capabilities[regions.CapabilityIPv6])
		}
	}
}

// TestReRegistrationWithoutCapabilityKeepsStoredValue is the same guarantee on
// the registration path, which an agent hits on every reconnect before its
// first capability-bearing frame.
func TestReRegistrationWithoutCapabilityKeepsStoredValue(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	_, dbSvc, _ := newRegionAPI(t)
	ctx := t.Context()

	liveWorker(t, dbSvc, "wrk-eu", "eu-west-1", yes(true))

	again, err := dbSvc.RegisterOrUpdateWorker(ctx, &models.Worker{
		Slug: "wrk-eu", Name: "wrk-eu",
	})
	r.NoError(err)
	r.NotNil(again.EgressIPv6)
	r.True(*again.EgressIPv6, "a capability-less re-registration must not erase a known value")
}
