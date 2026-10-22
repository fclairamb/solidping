package regions

import (
	"testing"

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
