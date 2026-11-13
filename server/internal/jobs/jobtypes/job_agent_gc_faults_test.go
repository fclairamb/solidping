package jobtypes

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/app/services"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/jobs/jobdef"
)

// agentGCWorld holds one platform agent silent for a month and one agent
// revoked three days ago: one candidate for each half of the sweep.
type agentGCWorld struct {
	db      *sqlite.Service
	jobs    *recordingJobs
	jctx    *jobdef.JobContext
	stale   *models.Agent
	revoked *models.Agent
}

func newAgentGCWorld(t *testing.T) *agentGCWorld {
	t.Helper()

	r := require.New(t)
	ctx := t.Context()

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	org := models.NewOrganization("acme", "")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	longAgo := time.Now().Add(-30 * 24 * time.Hour)
	threeDaysAgo := time.Now().Add(-3 * 24 * time.Hour)

	stale := models.NewSystemAgent("eu-west-1", "machine-dead", "ed-1", "x-1", "fp-1")
	stale.LastSeenAt = &longAgo
	stale.EnrolledAt = longAgo

	revoked := models.NewAgent(org.UID, "@acme/dc1", "old-revoked", "ed-2", "x-2", "fp-2")
	revoked.Status = models.AgentStatusRevoked
	revoked.RevokedAt = &threeDaysAgo
	revoked.EnrolledAt = longAgo

	for _, agent := range []*models.Agent{stale, revoked} {
		_, err = dbSvc.DB().NewInsert().Model(agent).Exec(ctx)
		r.NoError(err)
	}

	// A consumed reconnect nonce past its retention, for the prune half.
	r.NoError(dbSvc.CheckAndStoreAgentNonce(ctx, stale.UID, "old", time.Now().Add(-2*time.Hour), 10*time.Minute))

	jobs := &recordingJobs{}

	return &agentGCWorld{
		db: dbSvc, jobs: jobs, stale: stale, revoked: revoked,
		jctx: &jobdef.JobContext{
			Services:  &services.Registry{Jobs: jobs},
			DB:        dbSvc.DB(),
			DBService: dbSvc,
			Logger:    slog.New(slog.DiscardHandler),
		},
	}
}

func (w *agentGCWorld) live(t *testing.T, agent *models.Agent) bool {
	t.Helper()

	count, err := w.db.DB().NewSelect().TableExpr("agents").
		Where("uid = ?", agent.UID).Where("deleted_at IS NULL").Count(t.Context())
	require.NoError(t, err)

	return count == 1
}

// A failure on one agent is logged and the sweep moves on: the other half
// still runs and the sweep re-arms.
func TestAgentGC_OneFailingAgentDoesNotStopTheSweep(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		fail        []string
		staleLive   bool
		revokedLive bool
	}{
		{name: "control: nothing fails"},
		{
			name:      "retiring the stale agent fails",
			fail:      []string{`UPDATE "agents"`, "SET status = 'revoked'"},
			staleLive: true,
		},
		{
			name:        "purging the revoked agent fails",
			fail:        []string{`UPDATE "agents"`, "SET deleted_at"},
			revokedLive: true,
		},
		{
			// The agent is gone either way; its worker row is cleanup.
			name: "worker row cleanup fails",
			fail: []string{`"workers"`},
		},
		{
			name: "nonce prune fails",
			fail: []string{`DELETE FROM "agent_nonces"`},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			r := require.New(t)
			world := newAgentGCWorld(t)

			var fault *queryFault
			if tt.fail != nil {
				fault = failQueries(world.db.DB(), tt.fail...)
			}

			r.NoError((&AgentGCJobRun{}).Run(t.Context(), world.jctx))

			if fault != nil {
				fault.requireFired(t)
			}

			r.Equal(tt.staleLive, world.live(t, world.stale))
			r.Equal(tt.revokedLive, world.live(t, world.revoked))
			r.Equal([]string{string(jobdef.JobTypeAgentGC)}, world.jobs.calls, "the sweep re-arms itself")
		})
	}
}

// When a candidate listing fails the sweep asks for a retry.
func TestAgentGC_ListingFailuresAreRetryable(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		fail    []string
		wantErr string
		// staleLive: the stale agent is retired before the revoked listing runs.
		staleLive bool
	}{
		{
			name:      "stale system agents",
			fail:      []string{`FROM "agents"`, "kind = 'system'"},
			wantErr:   "list stale system agents",
			staleLive: true,
		},
		{
			name:    "purgeable revoked agents",
			fail:    []string{`FROM "agents"`, "COALESCE(revoked_at, updated_at)"},
			wantErr: "list purgeable revoked agents",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			r := require.New(t)
			world := newAgentGCWorld(t)
			fault := failQueries(world.db.DB(), tt.fail...)

			err := (&AgentGCJobRun{}).Run(t.Context(), world.jctx)
			fault.requireFired(t)

			r.ErrorContains(err, tt.wantErr)
			r.ErrorIs(err, context.Canceled)
			r.True(jobdef.IsRetryable(err))
			r.Equal(tt.staleLive, world.live(t, world.stale))
			r.True(world.live(t, world.revoked), "the revoked agent is not purged")
		})
	}
}

// Without a job service the sweep does its work and queues nothing.
func TestAgentGC_WithoutJobService(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	world := newAgentGCWorld(t)
	world.jctx.Services = nil

	r.NoError((&AgentGCJobRun{}).Run(t.Context(), world.jctx))
	r.False(world.live(t, world.stale))
	r.False(world.live(t, world.revoked))
	r.Empty(world.jobs.calls)
}
