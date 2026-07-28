package sqlite

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/agents"
	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
)

func newAgentTestService(t *testing.T) (*Service, string) {
	t.Helper()

	ctx := t.Context()
	svc, err := New(ctx, Config{InMemory: true})
	require.NoError(t, err)
	t.Cleanup(func() { _ = svc.Close() })
	require.NoError(t, svc.Initialize(ctx))

	orgUID := uuid.New().String()
	_, err = svc.db.NewRaw(
		"INSERT INTO organizations (uid, slug, name) VALUES (?, ?, ?)", orgUID, "agt-org", "Agent Org",
	).Exec(ctx)
	require.NoError(t, err)

	return svc, orgUID
}

func mintToken(t *testing.T, svc *Service, orgUID, region string, expiresAt time.Time) string {
	t.Helper()

	_, hash, err := agents.GenerateEnrollmentToken()
	require.NoError(t, err)

	tok := models.NewAgentEnrollmentToken(orgUID, region, hash, expiresAt, nil)
	require.NoError(t, svc.CreateAgentEnrollmentToken(t.Context(), tok))

	return hash
}

func TestEnrollAgentSingleUse(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	svc, orgUID := newAgentTestService(t)
	ctx := t.Context()

	region := "@agt-org/dc1"
	hash := mintToken(t, svc, orgUID, region, time.Now().Add(time.Hour))

	agent, err := svc.EnrollAgent(ctx, hash, "agent-1", "ed-pub", "age1recipient", "fp1")
	r.NoError(err)
	r.Equal(orgUID, agent.OrgUID())
	r.Equal(models.AgentKindOrg, agent.Kind, "an org token enrolls an org agent")
	r.False(agent.IsSystem())
	r.Equal(region, agent.Region)
	r.Equal(models.AgentStatusActive, agent.Status)

	// Second enrollment with the same token is rejected (single-use).
	_, err = svc.EnrollAgent(ctx, hash, "agent-1b", "ed-pub-2", "age1recipient2", "fp2")
	r.ErrorIs(err, db.ErrEnrollmentTokenInvalid)
}

// TestEnrollAgentSingleUseUnderConcurrency is the spec's "single-use race":
// N agents redeem the SAME token simultaneously. Exactly one must win; every
// loser must get ErrEnrollmentTokenInvalid, and exactly one agent row must
// exist afterwards (the conditional `used_at IS NULL` UPDATE is the guard).
func TestEnrollAgentSingleUseUnderConcurrency(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	svc, orgUID := newAgentTestService(t)
	ctx := t.Context()

	region := "@agt-org/dc1"
	hash := mintToken(t, svc, orgUID, region, time.Now().Add(time.Hour))

	const racers = 8

	var (
		start    sync.WaitGroup
		done     sync.WaitGroup
		mu       sync.Mutex
		winners  []*models.Agent
		failures []error
	)

	start.Add(1)

	for i := range racers {
		done.Add(1)

		go func(idx int) {
			defer done.Done()

			start.Wait() // release all goroutines at once

			agent, err := svc.EnrollAgent(
				ctx, hash,
				fmt.Sprintf("racer-%d", idx),
				fmt.Sprintf("ed-pub-%d", idx),
				fmt.Sprintf("age1recipient%d", idx),
				fmt.Sprintf("fp%d", idx),
			)

			// Collect only — every assertion happens on the test goroutine
			// after the wait (testify is not goroutine-safe).
			mu.Lock()
			defer mu.Unlock()

			if err != nil {
				failures = append(failures, err)

				return
			}

			winners = append(winners, agent)
		}(i)
	}

	start.Done()
	done.Wait()

	r.Len(winners, 1, "exactly one racer may consume the token")
	r.Len(failures, racers-1, "every other racer must lose")

	for _, err := range failures {
		r.ErrorIs(err, db.ErrEnrollmentTokenInvalid,
			"a losing racer must fail with the single-use error, not something else")
	}

	// The DB agrees: exactly one agent row, and it is the winner.
	all, err := svc.ListAgents(ctx, orgUID)
	r.NoError(err)
	r.Len(all, 1, "a raced token must never create two agents")
	r.Equal(winners[0].UID, all[0].UID)
}

func TestEnrollAgentRejectsExpiredToken(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	svc, orgUID := newAgentTestService(t)

	hash := mintToken(t, svc, orgUID, "@agt-org/dc1", time.Now().Add(-time.Minute))

	_, err := svc.EnrollAgent(t.Context(), hash, "agent-x", "ed", "age1x", "fpx")
	r.ErrorIs(err, db.ErrEnrollmentTokenInvalid)
}

func TestEnrollAgentRejectsUnknownToken(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	svc, _ := newAgentTestService(t)

	_, err := svc.EnrollAgent(t.Context(), "no-such-hash", "agent-x", "ed", "age1x", "fpx")
	r.ErrorIs(err, db.ErrEnrollmentTokenInvalid)
}

func TestAgentListingAndRevoke(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	svc, orgUID := newAgentTestService(t)
	ctx := t.Context()

	region := "@agt-org/dc1"
	h1 := mintToken(t, svc, orgUID, region, time.Now().Add(time.Hour))
	h2 := mintToken(t, svc, orgUID, region, time.Now().Add(time.Hour))

	a1, err := svc.EnrollAgent(ctx, h1, "a1", "ed1", "age1a", "fpa")
	r.NoError(err)
	_, err = svc.EnrollAgent(ctx, h2, "a2", "ed2", "age1b", "fpb")
	r.NoError(err)

	// Both active in the region (HA).
	active, err := svc.ListActiveAgentsByRegion(ctx, orgUID, region)
	r.NoError(err)
	r.Len(active, 2)

	all, err := svc.ListAgents(ctx, orgUID)
	r.NoError(err)
	r.Len(all, 2)

	// Revoke one; it drops out of the active/recipient set.
	r.NoError(svc.RevokeAgent(ctx, orgUID, a1.UID))
	active, err = svc.ListActiveAgentsByRegion(ctx, orgUID, region)
	r.NoError(err)
	r.Len(active, 1)

	got, err := svc.GetAgent(ctx, a1.UID)
	r.NoError(err)
	r.Equal(models.AgentStatusRevoked, got.Status)
	r.NotNil(got.RevokedAt)
}

// TestListActiveAgentsByRegionIsExactScoped asserts the recipient/claim scope is
// the agent's exact bound region — an agent enrolled in one private region never
// appears in another region's active set (no prefix matching on the agent path).
func TestListActiveAgentsByRegionIsExactScoped(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	svc, orgUID := newAgentTestService(t)
	ctx := t.Context()

	dc1 := "@agt-org/dc1"
	dc2 := "@agt-org/dc2"

	h1 := mintToken(t, svc, orgUID, dc1, time.Now().Add(time.Hour))
	h2 := mintToken(t, svc, orgUID, dc2, time.Now().Add(time.Hour))

	a1, err := svc.EnrollAgent(ctx, h1, "dc1-agent", "ed1", "age1dc1", "fp1")
	r.NoError(err)
	a2, err := svc.EnrollAgent(ctx, h2, "dc2-agent", "ed2", "age1dc2", "fp2")
	r.NoError(err)

	// The agent's region is taken from the token it consumed, not from input.
	r.Equal(dc1, a1.Region)
	r.Equal(dc2, a2.Region)

	in1, err := svc.ListActiveAgentsByRegion(ctx, orgUID, dc1)
	r.NoError(err)
	r.Len(in1, 1)
	r.Equal(a1.UID, in1[0].UID)

	in2, err := svc.ListActiveAgentsByRegion(ctx, orgUID, dc2)
	r.NoError(err)
	r.Len(in2, 1)
	r.Equal(a2.UID, in2[0].UID)

	// A different org sees neither.
	other, err := svc.ListActiveAgentsByRegion(ctx, uuid.New().String(), dc1)
	r.NoError(err)
	r.Empty(other)
}

// systemTestRegion is the shared cloud region the platform-agent fixtures use.
const systemTestRegion = "eu-west-1"

// mintSystemToken seeds a platform (kind='system') enrollment token bound to
// systemTestRegion, returning its hash. maxUses nil = unlimited.
func mintSystemToken(t *testing.T, svc *Service, maxUses *int) string {
	t.Helper()

	_, hash, err := agents.GenerateEnrollmentToken()
	require.NoError(t, err)

	require.NoError(t, svc.UpsertSystemAgentEnrollmentToken(t.Context(),
		models.NewSystemAgentEnrollmentToken(systemTestRegion, hash, time.Now().Add(time.Hour), maxUses)))

	return hash
}

// TestEnrollAgentSystemTokenIsMultiUse is the kind-aware half of enrollment: an
// org token is consumed atomically (asserted above), while a platform token
// stays usable so every machine of a fly fleet can enroll on boot with its OWN
// keypair — no private key is ever shared between machines.
func TestEnrollAgentSystemTokenIsMultiUse(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	svc, _ := newAgentTestService(t)
	ctx := t.Context()

	hash := mintSystemToken(t, svc, nil)

	first, err := svc.EnrollAgent(ctx, hash, "fly-1", "ed1", "age1a", "fp1")
	r.NoError(err)
	r.True(first.IsSystem())
	r.Nil(first.OrganizationUID, "a system agent has no owning organization")
	r.Equal(systemTestRegion, first.Region)

	second, err := svc.EnrollAgent(ctx, hash, "fly-2", "ed2", "age1b", "fp2")
	r.NoError(err, "a platform token must survive its first use")
	r.NotEqual(first.UID, second.UID)

	token, err := svc.GetAgentEnrollmentTokenByHash(ctx, hash)
	r.NoError(err)
	r.Equal(2, token.UseCount)
	r.True(token.HasUsesLeft())
	r.Equal(second.UID, *token.UsedByAgentUID, "used_by records the LATEST enrollment")
}

// TestEnrollAgentSystemTokenRespectsMaxUses: the multi-use budget is real. Once
// exhausted the token stops resolving at all, so enrollment is refused before
// any agent row is created.
func TestEnrollAgentSystemTokenRespectsMaxUses(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	svc, _ := newAgentTestService(t)
	ctx := t.Context()

	budget := 2
	hash := mintSystemToken(t, svc, &budget)

	_, err := svc.EnrollAgent(ctx, hash, "fly-1", "ed1", "age1a", "fp1")
	r.NoError(err)
	_, err = svc.EnrollAgent(ctx, hash, "fly-2", "ed2", "age1b", "fp2")
	r.NoError(err)

	_, err = svc.EnrollAgent(ctx, hash, "fly-3", "ed3", "age1c", "fp3")
	r.ErrorIs(err, db.ErrEnrollmentTokenInvalid)

	_, err = svc.GetAgentEnrollmentTokenByHash(ctx, hash)
	r.ErrorIs(err, db.ErrEnrollmentTokenInvalid)
}

// TestSystemAgentsAreInvisibleToOrgAdmin: the org-admin surfaces stay
// org-filtered, so a platform agent (or its token) never leaks into a
// customer's private-locations UI.
func TestSystemAgentsAreInvisibleToOrgAdmin(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	svc, orgUID := newAgentTestService(t)
	ctx := t.Context()

	sysHash := mintSystemToken(t, svc, nil)
	_, err := svc.EnrollAgent(ctx, sysHash, "fly-1", "ed1", "age1a", "fp1")
	r.NoError(err)

	orgHash := mintToken(t, svc, orgUID, "@agt-org/dc1", time.Now().Add(time.Hour))
	orgAgent, err := svc.EnrollAgent(ctx, orgHash, "dc1", "ed2", "age1b", "fp2")
	r.NoError(err)

	listed, err := svc.ListAgents(ctx, orgUID)
	r.NoError(err)
	r.Len(listed, 1)
	r.Equal(orgAgent.UID, listed[0].UID)

	tokens, err := svc.ListAgentEnrollmentTokens(ctx, orgUID)
	r.NoError(err)
	for _, token := range tokens {
		r.False(token.IsSystem(), "system tokens must never surface on the org-admin API")
	}

	sysTokens, err := svc.ListSystemAgentEnrollmentTokens(ctx)
	r.NoError(err)
	r.Len(sysTokens, 1)
}

// TestListStaleSystemAgentsExcludesOrgAgents backs the agent_gc selectivity at
// the query level: the GC candidate set is platform agents only, however long a
// customer's agent has been offline.
func TestListStaleSystemAgentsExcludesOrgAgents(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	svc, orgUID := newAgentTestService(t)
	ctx := t.Context()

	sysHash := mintSystemToken(t, svc, nil)
	sysAgent, err := svc.EnrollAgent(ctx, sysHash, "fly-1", "ed1", "age1a", "fp1")
	r.NoError(err)

	orgHash := mintToken(t, svc, orgUID, "@agt-org/dc1", time.Now().Add(time.Hour))
	orgAgent, err := svc.EnrollAgent(ctx, orgHash, "dc1", "ed2", "age1b", "fp2")
	r.NoError(err)

	// Both went silent a month ago.
	longAgo := time.Now().Add(-30 * 24 * time.Hour)
	r.NoError(svc.UpdateAgentLastSeen(ctx, sysAgent.UID, longAgo))
	r.NoError(svc.UpdateAgentLastSeen(ctx, orgAgent.UID, longAgo))

	stale, err := svc.ListStaleSystemAgents(ctx, time.Now().Add(-7*24*time.Hour))
	r.NoError(err)
	r.Len(stale, 1)
	r.Equal(sysAgent.UID, stale[0].UID)

	// Retiring is likewise scoped to kind='system'.
	r.NoError(svc.RetireSystemAgent(ctx, sysAgent.UID))
	r.NoError(svc.RetireSystemAgent(ctx, orgAgent.UID))

	survivors, err := svc.ListAgents(ctx, orgUID)
	r.NoError(err)
	r.Len(survivors, 1, "RetireSystemAgent must never touch a customer's agent")
}

// TestAgentNonceStoreIsAReplayGuard covers the shared store's contract: a
// consumed (agent, nonce) pair is refused inside the window, forgotten outside
// it, and prunable.
func TestAgentNonceStoreIsAReplayGuard(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	svc, _ := newAgentTestService(t)
	ctx := t.Context()

	now := time.Now()
	retain := 10 * time.Minute

	r.NoError(svc.CheckAndStoreAgentNonce(ctx, "agent-1", "n1", now, retain))
	r.ErrorIs(svc.CheckAndStoreAgentNonce(ctx, "agent-1", "n1", now, retain), db.ErrAgentNonceReplayed)
	r.NoError(svc.CheckAndStoreAgentNonce(ctx, "agent-1", "n2", now, retain))
	r.NoError(svc.CheckAndStoreAgentNonce(ctx, "agent-2", "n1", now, retain),
		"the guard is keyed per agent")

	// Past the retention window the pair is forgotten (the per-agent prune runs
	// before the insert).
	r.NoError(svc.CheckAndStoreAgentNonce(ctx, "agent-1", "n1", now.Add(2*retain), retain))

	pruned, err := svc.PruneAgentNonces(ctx, now.Add(3*retain))
	r.NoError(err)
	r.Positive(pruned)
}
