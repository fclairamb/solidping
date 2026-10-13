package incidents_test

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
)

// Region snapshot on the incident (spec 2026-10-12-15): the `regions` block
// of incident.Details tells the notifications which regions had failed.

// countingDB counts ListCheckRegionStates calls.
type countingDB struct {
	db.Service
	listStates atomic.Int64
}

func (c *countingDB) ListCheckRegionStates(
	ctx context.Context, checkUID string,
) ([]models.CheckRegionState, error) {
	c.listStates.Add(1)

	return c.Service.ListCheckRegionStates(ctx, checkUID)
}

func newCountingQuorumWorld(t *testing.T) (*quorumWorld, *countingDB) {
	t.Helper()

	inner, err := sqlite.New(t.Context(), sqlite.Config{InMemory: true})
	require.NoError(t, err)
	require.NoError(t, inner.Initialize(t.Context()))
	t.Cleanup(func() { _ = inner.Close() })

	counting := &countingDB{Service: inner}

	return newQuorumWorld(t, counting, "regions-snapshot"), counting
}

// roundsUntilIncident feeds `readings` until an incident opens.
func (c *quorumCheck) roundsUntilIncident(t *testing.T, readings ...reading) *models.Incident {
	t.Helper()

	for range 40 {
		c.round(t, readings...)

		if incident := c.incident(t); incident != nil {
			return incident
		}
	}

	require.FailNow(t, "no incident opened")

	return nil
}

func regionsOf(t *testing.T, incident *models.Incident) map[string]any {
	t.Helper()

	raw, ok := incident.Details["regions"]
	require.True(t, ok, "incident.Details has no regions key")

	regions, ok := raw.(map[string]any)
	require.True(t, ok, "regions is %T", raw)

	return regions
}

func failingOf(t *testing.T, regions map[string]any) []string {
	t.Helper()

	list, ok := regions["failingRegions"].([]any)
	require.True(t, ok, "failingRegions is %T", regions["failingRegions"])

	out := make([]string, 0, len(list))
	for _, item := range list {
		slug, isString := item.(string)
		require.True(t, isString)

		out = append(out, slug)
	}

	return out
}

func TestIncidentRegionsQuorumSnapshot(t *testing.T) {
	t.Parallel()

	w := newSQLiteQuorumWorld(t)
	c := w.newCheck(t, "three", []string{"eu-west", "us-east", "ap-south"}, strPtr("2"))

	incident := c.roundsUntilIncident(t, down("eu-west"), down("us-east"), up("ap-south"))

	regions := regionsOf(t, incident)
	require.ElementsMatch(t, []string{"eu-west", "us-east"}, failingOf(t, regions))
	require.EqualValues(t, 3, regions["regionCount"])
	require.EqualValues(t, 2, regions["failQuorum"])
}

func TestIncidentRegionsAllQuorumStoresBothRegions(t *testing.T) {
	t.Parallel()

	w := newSQLiteQuorumWorld(t)
	c := w.newCheck(t, "pair", []string{"eu-west", "us-east"}, strPtr("all"))

	incident := c.roundsUntilIncident(t, down("eu-west"), down("us-east"))

	regions := regionsOf(t, incident)
	require.ElementsMatch(t, []string{"eu-west", "us-east"}, failingOf(t, regions))
	require.EqualValues(t, 2, regions["regionCount"])
	require.EqualValues(t, 2, regions["failQuorum"])
}

func TestIncidentRegionsSingleRegionHasNoKey(t *testing.T) {
	t.Parallel()

	w := newSQLiteQuorumWorld(t)
	c := w.newCheck(t, "solo", []string{"eu-west"}, nil)

	incident := c.roundsUntilIncident(t, down("eu-west"))

	require.NotContains(t, incident.Details, "regions")
	require.Contains(t, incident.Details, "first_result", "the first_result fallback is still there")
}

func TestIncidentRegionsEscalationRewritesSnapshot(t *testing.T) {
	t.Parallel()

	w := newSQLiteQuorumWorld(t)
	c := w.newCheck(t, "escalate", []string{"eu-west", "us-east", "ap-south"}, strPtr("2"))

	incident := c.roundsUntilIncident(t, down("eu-west"), down("us-east"), up("ap-south"))
	require.ElementsMatch(t, []string{"eu-west", "us-east"}, failingOf(t, regionsOf(t, incident)))
	require.Nil(t, incident.EscalatedAt)

	// A third region fails and the escalation threshold is reached.
	c.snapshot.EscalationThreshold = incident.FailureCount + 1
	c.submit(t, "ap-south", models.ResultStatusDown)

	escalated := c.incident(t)
	require.NotNil(t, escalated.EscalatedAt, "the incident escalated")
	require.ElementsMatch(t, []string{"eu-west", "us-east", "ap-south"}, failingOf(t, regionsOf(t, escalated)))
	require.Contains(t, escalated.Details, "first_result", "the rest of the details is kept")
}

func TestIncidentRegionsQueryOncePerOpen(t *testing.T) {
	t.Parallel()

	w, counting := newCountingQuorumWorld(t)

	// `all` over 2 regions is not quorum mode: no per-result region query.
	c := w.newCheck(t, "pair-all", []string{"eu-west", "us-east"}, strPtr("all"))

	// Positive control: results flow and nothing queries the region states
	// until the incident opens.
	for range 4 {
		c.round(t, down("eu-west"), down("us-east"))
		w.clk.Advance(10 * time.Second)
	}

	if c.incident(t) == nil {
		c.roundsUntilIncident(t, down("eu-west"), down("us-east"))
	}

	require.EqualValues(t, 1, counting.listStates.Load(),
		"one region-state query for the open, none per result")
}
