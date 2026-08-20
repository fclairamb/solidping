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
		UID: uuid.New().String(), Slug: slug, Name: slug, Region: &region,
		Capabilities: capsForV6(v6),
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
		r.NoError(dbSvc.UpdateWorkerHeartbeat(ctx, capable.UID, capsForV6(yes(true)), ""))
		r.NoError(dbSvc.UpdateWorkerHeartbeat(ctx, incapable.UID, capsForV6(yes(false)), ""))

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

	r.NoError(dbSvc.UpdateWorkerHeartbeat(ctx, worker.UID, nil, ""))

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
	r.Equal(models.CapabilityStatePresent, again.Capability(models.CapabilityIPv6),
		"a capability-less re-registration must not erase a known set")
}

// capsForV6 translates the tests' legacy three-state IPv6 pointer into the
// capability set a worker now reports: nil = never reported (a nil, i.e.
// unknown, set), true = a v4+v6 host, false = a v4-only host. `false` maps to a
// NON-EMPTY set on purpose — a v4-only host still reports ipv4, and the set
// being non-nil is what makes "no ipv6" a real answer rather than an absence.
func capsForV6(v6 *bool) []string {
	if v6 == nil {
		return nil
	}

	if *v6 {
		return []string{models.CapabilityIPv4, models.CapabilityIPv6}
	}

	return []string{models.CapabilityIPv4}
}

// capsLiveWorker registers a live cloud worker with an explicit capability set,
// so a test can distinguish "reported nothing" (nil) from "reported an empty
// set" — the distinction the whole spec turns on.
func capsLiveWorker(t *testing.T, dbSvc *sqlite.Service, slug, region string, caps []string) {
	t.Helper()

	r := require.New(t)
	ctx := t.Context()

	worker, err := dbSvc.RegisterOrUpdateWorker(ctx, &models.Worker{
		UID: uuid.New().String(), Slug: slug, Name: slug, Region: &region,
		Capabilities: caps,
	})
	r.NoError(err)

	_, err = dbSvc.DB().NewUpdate().Model((*models.Worker)(nil)).
		Set("last_active_at = ?", time.Now()).
		Where("uid = ?", worker.UID).Exec(ctx)
	r.NoError(err)
}

// TestRegionListResponseCapabilityShape is the API-shape guard: the regions
// response carries exactly the published capability registry and nothing else.
//
// `ipv6` must still be bit-for-bit what the original boolean implementation
// produced for the same fixture — a v6 worker in eu, a v4-only worker in us —
// and no other key, name or value may move. The set has grown twice by
// deliberate addition (`ipv4`, spec 2026-08-16-02; `browser`, spec
// 2026-08-19-03); nothing else was licensed to change.
func TestRegionListResponseCapabilityShape(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	svc, dbSvc, org := newRegionAPI(t)

	capsLiveWorker(t, dbSvc, "wrk-eu", "eu-west-1", []string{"ipv4", "ipv6"})
	capsLiveWorker(t, dbSvc, "wrk-us", "us-east-1", []string{"ipv4"})

	resp, err := svc.ListOrgRegions(t.Context(), org.Slug)
	r.NoError(err)

	encoded, err := json.Marshal(resp)
	r.NoError(err)

	var decoded struct {
		Data []struct {
			Slug         string            `json:"slug"`
			Emoji        string            `json:"emoji"`
			Name         string            `json:"name"`
			Capabilities map[string]string `json:"capabilities"`
		} `json:"data"`
	}
	r.NoError(json.Unmarshal(encoded, &decoded))

	byslug := map[string]map[string]string{}
	for _, region := range decoded.Data {
		byslug[region.Slug] = region.Capabilities
	}

	// The pre-2026-08-16 payload for this fixture, plus the two deliberately
	// added keys. Neither worker reports `browser`, and a reported-without is a
	// real "no" — the same closed-set rule `ipv6` obeys.
	r.Equal(map[string]string{"ipv6": "yes", "ipv4": "yes", "browser": "no"}, byslug["eu"])
	r.Equal(map[string]string{"ipv6": "no", "ipv4": "yes", "browser": "no"}, byslug["us"])
}

// TestRegionListIPv4CarriesAllThreeStates: the new key is a first-class
// three-state verdict, and — the regression that matters — a worker that never
// reported yields `unknown`, never `no`.
func TestRegionListIPv4CarriesAllThreeStates(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		caps []string
		want string
	}{
		{"never-reported-is-unknown", nil, regions.CapabilityUnknown},
		{"empty-set-is-no", []string{}, regions.CapabilityNo},
		{"reported-is-yes", []string{"ipv4"}, regions.CapabilityYes},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			r := require.New(t)
			svc, dbSvc, org := newRegionAPI(t)

			capsLiveWorker(t, dbSvc, "wrk-eu", "eu-west-1", tc.caps)

			resp, err := svc.ListOrgRegions(t.Context(), org.Slug)
			r.NoError(err)

			for _, region := range resp.Data {
				if region.Slug != "eu" {
					continue
				}

				r.Equal(tc.want, region.Capabilities[regions.CapabilityIPv4])

				if tc.caps == nil {
					r.NotEqual(regions.CapabilityNo, region.Capabilities[regions.CapabilityIPv4],
						"an unreported ipv4 must never be advertised as a NO")
				}
			}
		})
	}
}
