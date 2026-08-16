package agents_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	agentcrypto "github.com/fclairamb/solidping/server/internal/agents"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/handlers/agents"
	"github.com/fclairamb/solidping/server/internal/regions"
)

// seedLocationAgent creates a private region plus one live agent serving it,
// with the workers row the WS handler registers (region NULL, as in production)
// carrying the given self-reported IPv6 capability.
func seedLocationAgent(t *testing.T, env *setup, slug, name string, v6 *bool) {
	t.Helper()

	r := require.New(t)
	ctx := t.Context()

	agent := models.NewAgent(
		env.org.UID, regions.PrivateRegionSlug(slug), name, "ed-"+name, "x-"+name, "fp-"+name,
	)
	_, err := env.dbSvc.DB().NewInsert().Model(agent).Exec(ctx)
	r.NoError(err)

	worker, err := env.dbSvc.RegisterOrUpdateWorker(ctx, &models.Worker{
		UID: agent.UID, Slug: agentcrypto.WorkerSlug(agent.UID),
		Name: "agent:" + name, Capabilities: capsForV6(v6),
	})
	r.NoError(err)

	_, err = env.dbSvc.DB().NewUpdate().Model((*models.Worker)(nil)).
		Set("last_active_at = ?", time.Now()).
		Where("uid = ?", worker.UID).Exec(ctx)
	r.NoError(err)
}

// TestPrivateLocationReportsIPv6Capability is AC9: the location's own page —
// the one place the user can actually FIX a missing family, by enabling IPv6 on
// the host they own — carries the capability.
func TestPrivateLocationReportsIPv6Capability(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	env := newSetup(t)
	ctx := t.Context()

	_, err := env.svc.CreatePrivateRegion(ctx, env.org.Slug, &agents.CreatePrivateRegionRequest{
		Slug: "hq", Name: "Head office", Emoji: "🏢",
	})
	r.NoError(err)

	_, err = env.svc.CreatePrivateRegion(ctx, env.org.Slug, &agents.CreatePrivateRegionRequest{
		Slug: "dc", Name: "Datacenter", Emoji: "🏭",
	})
	r.NoError(err)

	seedLocationAgent(t, env, "hq", "hq-agent", boolp(false))
	seedLocationAgent(t, env, "dc", "dc-agent", boolp(true))

	resp, err := env.svc.ListPrivateRegions(ctx, env.org.Slug)
	r.NoError(err)

	found := map[string]string{}
	for _, region := range resp.Data {
		found[region.Slug] = region.Capabilities[regions.CapabilityIPv6]
	}

	r.Equal(regions.CapabilityNo, found["hq"], "a v4-only host must say so on its own page")
	r.Equal(regions.CapabilityYes, found["dc"])
}

// TestPrivateLocationWithNoAgentIsUnknown: a location nobody has connected an
// agent to says nothing about IPv6 — it is not "no".
func TestPrivateLocationWithNoAgentIsUnknown(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	env := newSetup(t)
	ctx := t.Context()

	_, err := env.svc.CreatePrivateRegion(ctx, env.org.Slug, &agents.CreatePrivateRegionRequest{
		Slug: "empty", Name: "Empty", Emoji: "📍",
	})
	r.NoError(err)

	resp, err := env.svc.ListPrivateRegions(ctx, env.org.Slug)
	r.NoError(err)
	r.Len(resp.Data, 1)
	r.Equal(regions.CapabilityUnknown, resp.Data[0].Capabilities[regions.CapabilityIPv6])
	r.Zero(resp.Data[0].AgentCount)
}

// TestPrivateLocationLegacyAgentIsUnknown: an agent that predates the
// capability report must render as unknown, never as "no IPv6".
func TestPrivateLocationLegacyAgentIsUnknown(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	env := newSetup(t)
	ctx := t.Context()

	_, err := env.svc.CreatePrivateRegion(ctx, env.org.Slug, &agents.CreatePrivateRegionRequest{
		Slug: "hq", Name: "Head office", Emoji: "🏢",
	})
	r.NoError(err)

	seedLocationAgent(t, env, "hq", "legacy-agent", nil)

	resp, err := env.svc.ListPrivateRegions(ctx, env.org.Slug)
	r.NoError(err)
	r.Len(resp.Data, 1)
	r.Equal(regions.CapabilityUnknown, resp.Data[0].Capabilities[regions.CapabilityIPv6])
	r.Equal(1, resp.Data[0].AgentCount, "the agent is present — it simply has not reported")
}

func boolp(v bool) *bool { return &v }

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
