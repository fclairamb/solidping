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
	r.Equal(orgUID, agent.OrganizationUID)
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
