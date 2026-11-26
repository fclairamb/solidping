package agents_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	agentcrypto "github.com/fclairamb/solidping/server/internal/agents"
	"github.com/fclairamb/solidping/server/internal/db/models"
)

// TestListAgentsSurfacesWorkerVersion is the end-to-end proof for spec
// 2026-08-19-07: a version reported on the agent's worker row (workers.version
// — the single source of truth, resolved via agentcrypto.WorkerSlug rather
// than a denormalized agents.version column) reaches AgentResponse.
func TestListAgentsSurfacesWorkerVersion(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	env := newSetup(t)
	ctx := t.Context()

	agent := models.NewAgent(env.org.UID, "@dc1", "reporting-agent", "ed1", "age1x", "fp1")
	_, err := env.dbSvc.DB().NewInsert().Model(agent).Exec(ctx)
	r.NoError(err)

	reported := "0.17.0"
	_, err = env.dbSvc.RegisterOrUpdateWorker(ctx, &models.Worker{
		UID: agent.UID, Slug: agentcrypto.WorkerSlug(agent.UID),
		Name: "agent:" + agent.Name, Version: &reported,
	})
	r.NoError(err)

	list, err := env.svc.ListAgents(ctx, env.org.Slug)
	r.NoError(err)
	r.Len(list.Data, 1)
	r.NotNil(list.Data[0].Version)
	r.Equal(reported, *list.Data[0].Version)
}

// TestListAgentsUnreportedVersionIsNilNotEmpty: an agent whose worker row
// exists but has never reported a version (nil column — never sent a claim
// frame yet) must surface as a nil Version, i.e. "unknown" — the direct
// regression guard for the tri-state discipline the spec calls out: NULL must
// never render as drifted.
func TestListAgentsUnreportedVersionIsNilNotEmpty(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	env := newSetup(t)
	ctx := t.Context()

	agent := models.NewAgent(env.org.UID, "@dc1", "silent-agent", "ed1", "age1x", "fp1")
	_, err := env.dbSvc.DB().NewInsert().Model(agent).Exec(ctx)
	r.NoError(err)

	_, err = env.dbSvc.RegisterOrUpdateWorker(ctx, &models.Worker{
		UID: agent.UID, Slug: agentcrypto.WorkerSlug(agent.UID), Name: "agent:" + agent.Name,
	})
	r.NoError(err)

	list, err := env.svc.ListAgents(ctx, env.org.Slug)
	r.NoError(err)
	r.Len(list.Data, 1)
	r.Nil(list.Data[0].Version)
}

// TestListAgentsMissingWorkerRowIsNilVersion: an agent record that (for
// whatever reason) has no matching worker row yet must not error the whole
// listing — version is advisory-only, so a resolution miss degrades to
// unknown, exactly like "never reported".
func TestListAgentsMissingWorkerRowIsNilVersion(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	env := newSetup(t)
	ctx := t.Context()

	agent := models.NewAgent(env.org.UID, "@dc1", "orphan-agent", "ed1", "age1x", "fp1")
	_, err := env.dbSvc.DB().NewInsert().Model(agent).Exec(ctx)
	r.NoError(err)

	list, err := env.svc.ListAgents(ctx, env.org.Slug)
	r.NoError(err)
	r.Len(list.Data, 1)
	r.Nil(list.Data[0].Version)
}

// TestListAgentsSurfacesVersionPin: an agent pinned with
// SP_AGENT_IGNORE_VERSION_DRIFT (workers.version_pinned) is reported as such,
// on the org view and the fleet view (spec 2026-11-22-04).
func TestListAgentsSurfacesVersionPin(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	env := newSetup(t)
	ctx := t.Context()

	agent := models.NewAgent(env.org.UID, "@dc1", "pinned-agent", "ed1", "age1x", "fp1")
	_, err := env.dbSvc.DB().NewInsert().Model(agent).Exec(ctx)
	r.NoError(err)

	reported := "0.17.0"
	worker, err := env.dbSvc.RegisterOrUpdateWorker(ctx, &models.Worker{
		UID: agent.UID, Slug: agentcrypto.WorkerSlug(agent.UID),
		Name: "agent:" + agent.Name, Version: &reported,
	})
	r.NoError(err)

	list, err := env.svc.ListAgents(ctx, env.org.Slug)
	r.NoError(err)
	r.Len(list.Data, 1)
	r.False(list.Data[0].VersionPinned, "not pinned by default")

	_, err = env.dbSvc.DB().NewUpdate().Model((*models.Worker)(nil)).
		Set("version_pinned = ?", true).Where("uid = ?", worker.UID).Exec(ctx)
	r.NoError(err)

	list, err = env.svc.ListAgents(ctx, env.org.Slug)
	r.NoError(err)
	r.True(list.Data[0].VersionPinned)

	fleet, err := env.svc.ListAllAgents(ctx)
	r.NoError(err)
	r.Len(fleet.Data, 1)
	r.True(fleet.Data[0].VersionPinned)
}

// TestListAgentsFlagsUnsupportedVersion: a version below
// agentcrypto.MinSupportedAgentVersion surfaces versionSupported=false; a newer
// one and an unreported one stay supported (unknown is never unsupported).
func TestListAgentsFlagsUnsupportedVersion(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	env := newSetup(t)
	ctx := t.Context()

	for name, reported := range map[string]*string{
		"old-agent": ptr("0.1.0"), "new-agent": ptr("99.0.0"), "silent-agent": nil,
	} {
		agent := models.NewAgent(env.org.UID, "@dc1", name, "ed-"+name, "age-"+name, "fp-"+name)
		_, err := env.dbSvc.DB().NewInsert().Model(agent).Exec(ctx)
		r.NoError(err)

		_, err = env.dbSvc.RegisterOrUpdateWorker(ctx, &models.Worker{
			UID: agent.UID, Slug: agentcrypto.WorkerSlug(agent.UID), Name: "agent:" + agent.Name, Version: reported,
		})
		r.NoError(err)
	}

	list, err := env.svc.ListAgents(ctx, env.org.Slug)
	r.NoError(err)
	r.Len(list.Data, 3)

	for _, item := range list.Data {
		r.Equal(item.Name != "old-agent", item.VersionSupported, item.Name)
	}
}

func ptr(s string) *string { return &s }
