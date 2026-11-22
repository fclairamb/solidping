package agentws

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/postgres"
	"github.com/fclairamb/solidping/server/internal/testsupport"
)

// portUpgradePG is distinct from every other embedded-Postgres port in this
// repo.
const portUpgradePG = 15931

// TestUpgradeSlotOnPostgres runs the rolling-upgrade coordination (spec
// 2026-11-22-04) on Postgres: the upsert with its CASE on the old row, the
// compare-and-swap on the slot, the release, and the hand-over to the next
// agent, with two handler instances (two replicas) sharing the database.
//
//nolint:paralleltest // embedded Postgres on a fixed port
func TestUpgradeSlotOnPostgres(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping embedded-postgres test in -short mode")
	}

	ctx := t.Context()
	r := require.New(t)

	svc, err := postgres.New(ctx, &postgres.Config{Embedded: true, Port: portUpgradePG, RunMode: "test"})
	if err != nil {
		testsupport.PostgresUnavailable(t, err)
	}

	t.Cleanup(func() { _ = svc.Close() })

	if initErr := svc.Initialize(ctx); initErr != nil {
		testsupport.PostgresInitFailed(t, initErr)
	}

	replicaA := NewHandler(&config.Config{}, svc, nil, nil, nil, nil, nil, nil)
	replicaB := NewHandler(&config.Config{}, svc, nil, nil, nil, nil, nil, nil)

	orgUID := uuid.NewString()
	newState := func() *connState {
		return &connState{
			agent: &models.Agent{
				UID: uuid.NewString(), OrganizationUID: &orgUID, Kind: models.AgentKindOrg, Region: "@dc1",
			},
			upgrade: upgradeState{agentVersion: "1.2.0"},
		}
	}

	agentA, agentB := newState(), newState()

	_, err = replicaA.touchCandidate(ctx, agentA.agent, "1.2.3", time.Now())
	r.NoError(err)
	_, err = replicaB.touchCandidate(ctx, agentB.agent, "1.2.3", time.Now().Add(time.Millisecond))
	r.NoError(err)

	won, err := replicaB.tryAcquireUpgradeSlot(ctx, agentB, "1.2.3")
	r.NoError(err)
	r.False(won, "B is not next: A has waited longer")

	won, err = replicaA.tryAcquireUpgradeSlot(ctx, agentA, "1.2.3")
	r.NoError(err)
	r.True(won, "A takes the region's slot")

	won, err = replicaA.tryAcquireUpgradeSlot(ctx, agentA, "1.2.3")
	r.NoError(err)
	r.False(won, "A already holds it: no second request")

	won, err = replicaB.tryAcquireUpgradeSlot(ctx, agentB, "1.2.3")
	r.NoError(err)
	r.False(won, "one agent per region at a time")

	// A came back on the server's version, through the other replica.
	replicaB.completeUpgrade(ctx, agentA.agent, "1.2.3")

	won, err = replicaB.tryAcquireUpgradeSlot(ctx, agentB, "1.2.3")
	r.NoError(err)
	r.True(won, "the slot is handed to B")

	stored, err := replicaB.touchCandidate(ctx, agentB.agent, "1.2.3", time.Now())
	r.NoError(err)
	r.Equal(1, stored.Attempts)

	// A new target version resets the attempts.
	stored, err = replicaB.touchCandidate(ctx, agentB.agent, "1.3.0", time.Now())
	r.NoError(err)
	r.Equal(0, stored.Attempts)
}
