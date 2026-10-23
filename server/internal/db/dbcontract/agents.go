package dbcontract

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
)

func runAgents(t *testing.T, fix *fixture) {
	t.Helper()

	ctx := t.Context()
	must := require.New(t)
	svc := fix.svc
	future := time.Now().Add(time.Hour)

	// Org token: list, lookup, enroll, one-shot guard.
	tok := models.NewAgentEnrollmentToken(fix.org.UID, "eu-home", "hash-org-1", future, &fix.user.UID)
	must.NoError(svc.CreateAgentEnrollmentToken(ctx, tok))

	listed, err := svc.ListAgentEnrollmentTokens(ctx, fix.org.UID)
	must.NoError(err)
	must.Len(listed, 1)

	byHash, err := svc.GetAgentEnrollmentTokenByHash(ctx, "hash-org-1")
	must.NoError(err)
	must.Equal(tok.UID, byHash.UID)

	_, err = svc.GetAgentEnrollmentTokenByHash(ctx, "missing")
	must.ErrorIs(err, db.ErrEnrollmentTokenInvalid)

	agent, err := svc.EnrollAgent(ctx, "hash-org-1", "box", "ed-pub", "x-pub", "fp-1")
	must.NoError(err)
	must.Equal(models.AgentKindOrg, agent.Kind)
	must.Equal(fix.org.UID, agent.OrgUID())

	_, err = svc.EnrollAgent(ctx, "hash-org-1", "box2", "ed-pub", "x-pub", "fp-2")
	must.ErrorIs(err, db.ErrEnrollmentTokenInvalid)

	_, err = svc.GetAgentEnrollmentTokenByHash(ctx, "hash-org-1")
	must.ErrorIs(err, db.ErrEnrollmentTokenInvalid)

	// Cancelled token is invisible and cannot enroll.
	cancelled := models.NewAgentEnrollmentToken(fix.org.UID, "eu-home", "hash-org-2", future, nil)
	must.NoError(svc.CreateAgentEnrollmentToken(ctx, cancelled))
	must.NoError(svc.DeleteAgentEnrollmentToken(ctx, fix.org.UID, cancelled.UID))
	_, err = svc.EnrollAgent(ctx, "hash-org-2", "box3", "ed", "x", "fp-3")
	must.ErrorIs(err, db.ErrEnrollmentTokenInvalid)

	// Agents listing and lifecycle.
	got, err := svc.GetAgent(ctx, agent.UID)
	must.NoError(err)
	must.Equal("box", got.Name)

	orgAgents, err := svc.ListAgents(ctx, fix.org.UID)
	must.NoError(err)
	must.Len(orgAgents, 1)

	active, err := svc.ListActiveAgentsByRegion(ctx, fix.org.UID, "eu-home")
	must.NoError(err)
	must.Len(active, 1)

	seen := time.Now().Add(-time.Minute)
	must.NoError(svc.UpdateAgentLastSeen(ctx, agent.UID, seen))

	must.NoError(svc.RetireAgentWorkerRow(ctx, agent.UID))

	must.NoError(svc.RevokeAgent(ctx, fix.org.UID, agent.UID))
	got, err = svc.GetAgent(ctx, agent.UID)
	must.NoError(err)
	must.Equal(models.AgentStatusRevoked, got.Status)

	active, err = svc.ListActiveAgentsByRegion(ctx, fix.org.UID, "eu-home")
	must.NoError(err)
	must.Empty(active)

	purgeable, err := svc.ListPurgeableRevokedAgents(ctx, time.Now().Add(time.Minute))
	must.NoError(err)
	must.Len(purgeable, 1)
	must.NoError(svc.PurgeAgent(ctx, agent.UID))

	_, err = svc.GetAgent(ctx, agent.UID)
	must.Error(err)

	// System tokens: upsert, multi-use enrollment, revoke-except, retire.
	maxUses := 2
	sys := models.NewSystemAgentEnrollmentToken("global-eu", "hash-sys-1", future, &maxUses)
	must.NoError(svc.UpsertSystemAgentEnrollmentToken(ctx, sys))

	refreshed := models.NewSystemAgentEnrollmentToken("global-eu", "hash-sys-1", future.Add(time.Hour), nil)
	must.NoError(svc.UpsertSystemAgentEnrollmentToken(ctx, refreshed))

	other := models.NewSystemAgentEnrollmentToken("global-us", "hash-sys-2", future, nil)
	must.NoError(svc.UpsertSystemAgentEnrollmentToken(ctx, other))

	sysTokens, err := svc.ListSystemAgentEnrollmentTokens(ctx)
	must.NoError(err)
	must.Len(sysTokens, 2)

	sysAgent, err := svc.EnrollAgent(ctx, "hash-sys-1", "sys-a", "ed-s", "x-s", "fp-s")
	must.NoError(err)
	must.True(sysAgent.IsSystem())

	all, err := svc.ListAllAgents(ctx)
	must.NoError(err)
	must.NotEmpty(all)

	stale, err := svc.ListStaleSystemAgents(ctx, time.Now().Add(time.Hour))
	must.NoError(err)
	must.Len(stale, 1)

	stale, err = svc.ListStaleSystemAgents(ctx, time.Now().Add(-time.Hour))
	must.NoError(err)
	must.Empty(stale)

	must.NoError(svc.RetireSystemAgent(ctx, sysAgent.UID))

	revoked, err := svc.RevokeSystemAgentEnrollmentTokensExcept(ctx, []string{"hash-sys-2"})
	must.NoError(err)
	must.EqualValues(1, revoked)

	revoked, err = svc.RevokeSystemAgentEnrollmentTokensExcept(ctx, nil)
	must.NoError(err)
	must.EqualValues(1, revoked)

	// Reconnect nonces: first use stored, replay refused, prune removes it.
	nonceAgent := "7d0e6b3a-1c5f-4e2a-9b8d-3f4a5c6d7e8f"
	now := time.Now()
	must.NoError(svc.CheckAndStoreAgentNonce(ctx, nonceAgent, "n1", now, time.Hour))
	must.ErrorIs(svc.CheckAndStoreAgentNonce(ctx, nonceAgent, "n1", now, time.Hour), db.ErrAgentNonceReplayed)

	pruned, err := svc.PruneAgentNonces(ctx, now.Add(time.Minute))
	must.NoError(err)
	must.EqualValues(1, pruned)
	must.NoError(svc.CheckAndStoreAgentNonce(ctx, nonceAgent, "n1", now, time.Hour))
}
