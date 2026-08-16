package regions_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	agentcrypto "github.com/fclairamb/solidping/server/internal/agents"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/regions"
)

// capEnv is the fixture for region capability aggregation: a real sqlite DB
// with a real regions service on top, so the worker/agent joins are exercised
// rather than mocked.
type capEnv struct {
	t     *testing.T
	dbSvc *sqlite.Service
	svc   *regions.Service
}

func newCapEnv(t *testing.T) *capEnv {
	t.Helper()

	ctx := t.Context()
	r := require.New(t)

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	return &capEnv{t: t, dbSvc: dbSvc, svc: regions.NewService(dbSvc)}
}

// org creates an organization.
func (e *capEnv) org(slug string) *models.Organization {
	e.t.Helper()

	org := models.NewOrganization(slug, slug)
	require.NoError(e.t, e.dbSvc.CreateOrganization(e.t.Context(), org))

	return org
}

// cloudWorker registers a cloud worker in region with the given reported
// egress, then forces its last_active_at to lastActive. A nil v6 models a
// worker that never reported (an older binary).
func (e *capEnv) cloudWorker(slug, region string, v6 *bool, lastActive time.Time) *models.Worker {
	e.t.Helper()

	r := require.New(e.t)

	worker, err := e.dbSvc.RegisterOrUpdateWorker(e.t.Context(), &models.Worker{
		UID: uuid.New().String(), Slug: slug, Name: slug, Region: &region, EgressIPv6: v6,
	})
	r.NoError(err)

	e.setLastActive(worker.UID, lastActive)

	return worker
}

// agentWorker seeds an org agent bound to a private region plus the workers row
// the WS handler would have registered for it (region NULL, matching
// production: an org agent's routing never goes through the workers row).
func (e *capEnv) agentWorker(orgUID, region, name string, v6 *bool, lastActive time.Time) *models.Agent {
	e.t.Helper()

	r := require.New(e.t)
	ctx := e.t.Context()

	agent := models.NewAgent(orgUID, region, name, "ed-"+name, "x-"+name, "fp-"+name)
	_, err := e.dbSvc.DB().NewInsert().Model(agent).Exec(ctx)
	r.NoError(err)

	worker, err := e.dbSvc.RegisterOrUpdateWorker(ctx, &models.Worker{
		UID: agent.UID, Slug: agentcrypto.WorkerSlug(agent.UID),
		Name: "agent:" + name, EgressIPv6: v6,
	})
	r.NoError(err)

	e.setLastActive(worker.UID, lastActive)

	return agent
}

func (e *capEnv) setLastActive(workerUID string, at time.Time) {
	e.t.Helper()

	_, err := e.dbSvc.DB().NewUpdate().
		Model((*models.Worker)(nil)).
		Set("last_active_at = ?", at).
		Where("uid = ?", workerUID).
		Exec(e.t.Context())
	require.NoError(e.t, err)
}

// ipv6Of runs the global annotation and returns the verdict for one slug.
func (e *capEnv) ipv6Of(ctx context.Context, slug string) string {
	e.t.Helper()

	defs := []regions.RegionDefinition{{Slug: slug, Name: slug}}
	require.NoError(e.t, e.svc.AnnotateGlobalCapabilities(ctx, defs))

	return defs[0].IPv6Capability()
}

func boolPtr(v bool) *bool { return &v }

// TestRegionCapabilityAnyNotAll is AC3: one v6 worker among three v4-only ones
// makes the region v6-capable, because a job lands on exactly one worker.
func TestRegionCapabilityAnyNotAll(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	env := newCapEnv(t)
	now := time.Now()

	env.cloudWorker("wrk-1", "eu-west-1", boolPtr(false), now)
	env.cloudWorker("wrk-2", "eu-west-2", boolPtr(false), now)
	env.cloudWorker("wrk-3", "eu-west-3", boolPtr(false), now)
	env.cloudWorker("wrk-4", "eu-west-4", boolPtr(true), now)

	r.Equal(regions.CapabilityYes, env.ipv6Of(t.Context(), "eu"))
}

// TestRegionCapabilityNoWhenEveryLiveWorkerSaysNo is the honest negative: live
// workers reported, and none of them has IPv6.
func TestRegionCapabilityNoWhenEveryLiveWorkerSaysNo(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	env := newCapEnv(t)
	now := time.Now()

	env.cloudWorker("wrk-1", "eu-west-1", boolPtr(false), now)
	env.cloudWorker("wrk-2", "eu-west-2", boolPtr(false), now)

	r.Equal(regions.CapabilityNo, env.ipv6Of(t.Context(), "eu"))
}

// TestRegionCapabilityUnknownWhenWorkerNeverReports is AC2 and the single most
// important negative control in this spec: a worker that never reports leaves
// the column null, and the region must advertise UNKNOWN — never "no". Rendering
// it as "no" is precisely the false statement the feature exists to remove.
func TestRegionCapabilityUnknownWhenWorkerNeverReports(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	env := newCapEnv(t)

	worker := env.cloudWorker("legacy", "eu-west-1", nil, time.Now())

	var row models.Worker
	r.NoError(env.dbSvc.DB().NewSelect().Model(&row).
		Where("uid = ?", worker.UID).Scan(t.Context()))
	r.Nil(row.EgressIPv6, "a non-reporting worker must leave the column null")

	r.Equal(regions.CapabilityUnknown, env.ipv6Of(t.Context(), "eu"))
	r.NotEqual(regions.CapabilityNo, env.ipv6Of(t.Context(), "eu"))
}

// TestRegionCapabilityUnknownWithNoWorkersAtAll — an empty region says nothing.
func TestRegionCapabilityUnknownWithNoWorkersAtAll(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	env := newCapEnv(t)

	r.Equal(regions.CapabilityUnknown, env.ipv6Of(t.Context(), "eu"))
}

// TestRegionCapabilityStaleWorkerStopsAdvertising is AC4: a region whose only
// v6 worker fell out of the liveness window stops claiming IPv6.
func TestRegionCapabilityStaleWorkerStopsAdvertising(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	env := newCapEnv(t)
	now := time.Now()

	stale := now.Add(-regions.WorkerLivenessWindow - time.Minute)
	env.cloudWorker("wrk-v6", "eu-west-1", boolPtr(true), stale)
	env.cloudWorker("wrk-v4", "eu-west-2", boolPtr(false), now)

	r.Equal(regions.CapabilityNo, env.ipv6Of(t.Context(), "eu"),
		"a stale v6 worker must not keep the region advertising IPv6")

	// And with the stale worker as the ONLY worker, the answer is unknown —
	// not "no": nothing live has said anything.
	env2 := newCapEnv(t)
	env2.cloudWorker("wrk-v6", "eu-west-1", boolPtr(true), stale)
	r.Equal(regions.CapabilityUnknown, env2.ipv6Of(t.Context(), "eu"))
}

// TestRegionCapabilityIgnoresOtherRegions guards the hierarchical match: a
// worker in us-east must not lend its capability to eu.
func TestRegionCapabilityIgnoresOtherRegions(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	env := newCapEnv(t)

	env.cloudWorker("wrk-us", "us-east-1", boolPtr(true), time.Now())

	r.Equal(regions.CapabilityUnknown, env.ipv6Of(t.Context(), "eu"))
	r.Equal(regions.CapabilityYes, env.ipv6Of(t.Context(), "us"))
}

// TestPrivateRegionCapabilityDoesNotLeakAcrossOrgs is THE `@`-prefix trap.
// `@datacenter` is org-relative and NOT globally unique: two orgs can each
// define it. Org A's IPv6-capable agent must never make org B's identically
// named location look IPv6-capable.
func TestPrivateRegionCapabilityDoesNotLeakAcrossOrgs(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	env := newCapEnv(t)
	ctx := t.Context()
	now := time.Now()

	orgA := env.org("alpha")
	orgB := env.org("bravo")

	region := regions.PrivateRegionSlug("datacenter")
	env.agentWorker(orgA.UID, region, "alpha-agent", boolPtr(true), now)
	env.agentWorker(orgB.UID, region, "bravo-agent", boolPtr(false), now)

	defsA := []regions.RegionDefinition{{Slug: "datacenter", Name: "DC"}}
	r.NoError(env.svc.AnnotatePrivateCapabilities(ctx, orgA.UID, defsA))
	r.Equal(regions.CapabilityYes, defsA[0].IPv6Capability())

	defsB := []regions.RegionDefinition{{Slug: "datacenter", Name: "DC"}}
	r.NoError(env.svc.AnnotatePrivateCapabilities(ctx, orgB.UID, defsB))
	r.Equal(regions.CapabilityNo, defsB[0].IPv6Capability(),
		"org B must see its OWN agent's capability, never org A's")
}

// TestPrivateRegionCapabilityUnknownForNonReportingAgent is AC2 on the private
// path — the case a customer hits when their agent predates the feature.
func TestPrivateRegionCapabilityUnknownForNonReportingAgent(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	env := newCapEnv(t)
	ctx := t.Context()

	org := env.org("acme")
	env.agentWorker(org.UID, regions.PrivateRegionSlug("hq"), "legacy", nil, time.Now())

	defs := []regions.RegionDefinition{{Slug: "hq", Name: "HQ"}}
	r.NoError(env.svc.AnnotatePrivateCapabilities(ctx, org.UID, defs))
	r.Equal(regions.CapabilityUnknown, defs[0].IPv6Capability())
}

// TestPrivateRegionCapabilityStaleAgent — the private twin of AC4.
func TestPrivateRegionCapabilityStaleAgent(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	env := newCapEnv(t)
	ctx := t.Context()

	org := env.org("acme")
	stale := time.Now().Add(-regions.WorkerLivenessWindow - time.Minute)
	env.agentWorker(org.UID, regions.PrivateRegionSlug("hq"), "gone", boolPtr(true), stale)

	defs := []regions.RegionDefinition{{Slug: "hq", Name: "HQ"}}
	r.NoError(env.svc.AnnotatePrivateCapabilities(ctx, org.UID, defs))
	r.Equal(regions.CapabilityUnknown, defs[0].IPv6Capability())
}

// TestCloudRegionIgnoresAgentWorkers pins that a cloud region never inherits a
// private location's capability. An org agent's workers row carries a NULL
// region by design (routing on that path is hard-scoped elsewhere), so it must
// contribute to no cloud region at all — and the DB's own CHECK constraint
// makes a worker row with an `@` region unrepresentable, which is why this
// asserts the NULL-region shape production actually writes.
func TestCloudRegionIgnoresAgentWorkers(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	env := newCapEnv(t)

	org := env.org("acme")
	env.agentWorker(org.UID, regions.PrivateRegionSlug("hq"), "hq-agent", boolPtr(true), time.Now())

	r.Equal(regions.CapabilityUnknown, env.ipv6Of(t.Context(), "default"))
	r.Equal(regions.CapabilityUnknown, env.ipv6Of(t.Context(), "eu"))

	// And a worker that DID somehow carry the reserved prefix is skipped by the
	// aggregation itself, independent of the DB constraint that forbids it.
	badRegion := regions.PrivateRegionSlug("hq")

	bad, err := env.dbSvc.RegisterOrUpdateWorker(t.Context(), &models.Worker{
		UID: uuid.New().String(), Slug: "wrk-bad", Name: "bad",
		Region: &badRegion, EgressIPv6: boolPtr(true),
	})
	if err == nil {
		env.setLastActive(bad.UID, time.Now())
		r.Equal(regions.CapabilityUnknown, env.ipv6Of(t.Context(), "@hq"))
	}
}

// TestCapabilityIndexCoversCloudAndPrivate checks the lookup the check-form
// warning uses: both kinds of region, keyed by the slug stored on a check.
func TestCapabilityIndexCoversCloudAndPrivate(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	env := newCapEnv(t)
	ctx := t.Context()
	now := time.Now()

	org := env.org("acme")
	r.NoError(env.svc.SetOrgCustomRegions(ctx, org.UID, []regions.RegionDefinition{
		{Slug: "hq", Name: "Head office", Emoji: "🏢"},
	}))

	env.cloudWorker("cloud", "default", boolPtr(false), now)
	env.agentWorker(org.UID, regions.PrivateRegionSlug("hq"), "hq-agent", boolPtr(true), now)

	index, err := env.svc.CapabilityIndex(ctx, org.UID)
	r.NoError(err)

	r.Equal(regions.CapabilityNo, index["default"].IPv6Capability())
	r.Equal(regions.CapabilityYes, index["@hq"].IPv6Capability())
}

// TestSetOrgCustomRegionsNeverPersistsCapabilities: capabilities are derived on
// every read, so a persisted copy would go stale and then outrank the truth.
func TestSetOrgCustomRegionsNeverPersistsCapabilities(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	env := newCapEnv(t)
	ctx := t.Context()

	org := env.org("acme")
	r.NoError(env.svc.SetOrgCustomRegions(ctx, org.UID, []regions.RegionDefinition{
		{Slug: "hq", Name: "Head office", Capabilities: map[string]string{
			regions.CapabilityIPv6: regions.CapabilityYes,
		}},
	}))

	stored, err := env.svc.GetOrgCustomRegions(ctx, org.UID)
	r.NoError(err)
	r.Len(stored, 1)
	r.Nil(stored[0].Capabilities, "derived capabilities must never be written back")
}
