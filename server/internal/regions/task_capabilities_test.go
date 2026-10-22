package regions

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/agents"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
)

func TestStripCapabilities(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	names := []string{"x-task"}

	r.Nil(stripCapabilities(nil, names), "unknown stays unknown")
	r.Equal([]string{"ipv4"}, stripCapabilities([]string{"ipv4", "x-task"}, names))
	r.Equal([]string{}, stripCapabilities([]string{"x-task"}, names), "a reported set stays reported")
	r.Equal([]string{"ipv4"}, StripTaskCapabilities([]string{"ipv4"}))
}

// A worker-task capability is aggregated from the org's agents for a private
// region, and a system agent's report (stripped by the agent socket before it
// is stored) never makes a cloud region claim it.
func TestTaskCapabilityFromOrgAgentsOnly(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := t.Context()

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	names := []string{"x-task"}
	svc := &Service{db: dbSvc, taskCaps: names}

	org := models.NewOrganization("acme", "Acme")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	reported := []string{"ipv4", "x-task"}

	orgAgent := models.NewAgent(org.UID, "@dc1", "dc1", "ed-1", "x-1", "fp-1")
	_, err = dbSvc.DB().NewInsert().Model(orgAgent).Exec(ctx)
	r.NoError(err)

	_, err = dbSvc.RegisterOrUpdateWorker(ctx, &models.Worker{
		UID: orgAgent.UID, Slug: agents.WorkerSlug(orgAgent.UID), Name: "agent:dc1", Capabilities: reported,
	})
	r.NoError(err)

	systemRegion := "eu-west"
	systemAgent := models.NewSystemAgent(systemRegion, "sys", "ed-2", "x-2", "fp-2")
	_, err = dbSvc.DB().NewInsert().Model(systemAgent).Exec(ctx)
	r.NoError(err)

	_, err = dbSvc.RegisterOrUpdateWorker(ctx, &models.Worker{
		UID: systemAgent.UID, Slug: agents.WorkerSlug(systemAgent.UID), Name: "agent:sys",
		Region: &systemRegion, Capabilities: stripCapabilities(reported, names),
	})
	r.NoError(err)

	cloudRegion := "us-east"
	_, err = dbSvc.RegisterOrUpdateWorker(ctx, &models.Worker{
		UID: "00000000-0000-0000-0000-0000000000c1", Slug: "cloud-us", Name: "cloud-us",
		Region: &cloudRegion, Capabilities: reported,
	})
	r.NoError(err)

	private := []RegionDefinition{{Slug: "dc1", Name: "dc1"}}
	r.NoError(svc.AnnotatePrivateCapabilities(ctx, org.UID, private))
	r.Equal(CapabilityYes, private[0].Capabilities["x-task"], "an org agent's report counts")

	global := []RegionDefinition{{Slug: systemRegion, Name: systemRegion}, {Slug: cloudRegion, Name: cloudRegion}}
	r.NoError(svc.AnnotateGlobalCapabilities(ctx, global))
	r.Equal(CapabilityNo, global[0].Capabilities["x-task"], "a system agent's report never counts")
	r.Equal(CapabilityYes, global[0].Capabilities[CapabilityIPv4], "the rest of its report does")
	r.Equal(CapabilityYes, global[1].Capabilities["x-task"], "a cloud worker's report counts")
}

// HasLiveAgentWith needs a live org agent of the exact region whose worker
// reports the capability: an agent without it, a stale one or one of another
// region does not count.
func TestHasLiveAgentWith(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := t.Context()

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	svc := NewService(dbSvc)

	org := models.NewOrganization("acme", "Acme")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	addAgent := func(region, name string, caps []string, seen time.Time) {
		agent := models.NewAgent(org.UID, region, name, "ed-"+name, "x-"+name, "fp-"+name)
		_, insertErr := dbSvc.DB().NewInsert().Model(agent).Exec(ctx)
		r.NoError(insertErr)
		r.NoError(dbSvc.UpdateAgentLastSeen(ctx, agent.UID, seen))

		_, insertErr = dbSvc.RegisterOrUpdateWorker(ctx, &models.Worker{
			UID: agent.UID, Slug: agents.WorkerSlug(agent.UID), Name: "agent:" + name, Capabilities: caps,
		})
		r.NoError(insertErr)
	}

	addAgent("@dc1", "plain", []string{"ipv4"}, time.Now())
	addAgent("@dc1", "stale", []string{"ipv4", CapabilityDiscovery}, time.Now().Add(-time.Hour))
	addAgent("@dc2", "other", []string{"ipv4", CapabilityDiscovery}, time.Now())

	ok, err := svc.HasLiveAgentWith(ctx, org.UID, "@dc1", CapabilityDiscovery)
	r.NoError(err)
	r.False(ok, "neither the plain agent nor the stale one counts")

	ok, err = svc.HasLiveAgentWith(ctx, org.UID, "@dc2", CapabilityDiscovery)
	r.NoError(err)
	r.True(ok, "positive control: a live agent reporting discovery")

	addAgent("@dc1", "live", []string{CapabilityDiscovery}, time.Now())

	ok, err = svc.HasLiveAgentWith(ctx, org.UID, "@dc1", CapabilityDiscovery)
	r.NoError(err)
	r.True(ok)
}
