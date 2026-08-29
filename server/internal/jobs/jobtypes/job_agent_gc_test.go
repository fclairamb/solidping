package jobtypes_test

import (
	"context"
	"encoding/json"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	agentcrypto "github.com/fclairamb/solidping/server/internal/agents"
	"github.com/fclairamb/solidping/server/internal/app/services"
	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/jobs/jobdef"
	"github.com/fclairamb/solidping/server/internal/jobs/jobsvc"
	"github.com/fclairamb/solidping/server/internal/jobs/jobtypes"
	"github.com/fclairamb/solidping/server/internal/notifier"
)

// newAgentGCTestContext wires an in-memory SQLite DB, a real job service and a
// services registry so the GC runner exercises its whole path (list, retire,
// worker cleanup, nonce prune, self-reschedule). Returns the context plus the
// organization UID org-owned fixtures hang off.
func newAgentGCTestContext(t *testing.T) (*jobdef.JobContext, db.Service, string) {
	t.Helper()
	r := require.New(t)

	ctx := t.Context()
	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	org := models.NewOrganization("acme", "")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	jobSvc := jobsvc.NewService(dbSvc.DB(), dbSvc, notifier.NewLocalEventNotifier(), nil)

	return &jobdef.JobContext{
		Services:  &services.Registry{Jobs: jobSvc},
		DB:        dbSvc.DB(),
		DBService: dbSvc,
		Logger:    slog.Default(),
	}, dbSvc, org.UID
}

// seedAgent inserts an agent row directly (enrollment goes through a token,
// which these tests do not need) with a chosen last-seen instant.
func seedAgent(t *testing.T, dbSvc db.Service, agent *models.Agent, lastSeen *time.Time, enrolledAgo time.Duration) {
	t.Helper()

	agent.LastSeenAt = lastSeen
	agent.EnrolledAt = time.Now().Add(-enrolledAgo)

	_, err := dbSvc.DB().NewInsert().Model(agent).Exec(t.Context())
	require.NoError(t, err)
}

// liveAgent reads an agent back, reporting whether it is still live (not
// soft-deleted).
func liveAgent(t *testing.T, dbSvc db.Service, uid string) (*models.Agent, bool) {
	t.Helper()

	var agent models.Agent
	err := dbSvc.DB().NewSelect().Model(&agent).Where("uid = ?", uid).Scan(t.Context())
	require.NoError(t, err)

	return &agent, agent.DeletedAt == nil
}

// TestAgentGCRetiresOnlyStaleSystemAgents is the selectivity test: the sweep
// touches a silent PLATFORM agent and nothing else — not a fresh platform
// agent, and never a customer-managed org agent however long it has been away.
func TestAgentGCRetiresOnlyStaleSystemAgents(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	jctx, dbSvc, orgUID := newAgentGCTestContext(t)

	longAgo := time.Now().Add(-30 * 24 * time.Hour)
	recently := time.Now().Add(-time.Minute)

	staleSystem := models.NewSystemAgent("eu-west-1", "fly-machine-dead", "ed-1", "x-1", "fp-1")
	seedAgent(t, dbSvc, staleSystem, &longAgo, 30*24*time.Hour)

	freshSystem := models.NewSystemAgent("eu-west-1", "fly-machine-live", "ed-2", "x-2", "fp-2")
	seedAgent(t, dbSvc, freshSystem, &recently, 30*24*time.Hour)

	// An org agent that has been offline for a month: a customer's business,
	// never the platform's to delete.
	staleOrg := models.NewAgent(orgUID, "@acme/dc1", "customer-agent", "ed-3", "x-3", "fp-3")
	seedAgent(t, dbSvc, staleOrg, &longAgo, 30*24*time.Hour)

	// A platform agent that enrolled long ago and never connected at all: no
	// last_seen_at, judged on enrolled_at.
	neverSeen := models.NewSystemAgent("us-east-1", "fly-machine-ghost", "ed-4", "x-4", "fp-4")
	seedAgent(t, dbSvc, neverSeen, nil, 30*24*time.Hour)

	run := &jobtypes.AgentGCJobRun{}
	r.NoError(run.Run(t.Context(), jctx))

	retired, live := liveAgent(t, dbSvc, staleSystem.UID)
	r.False(live, "a silent platform agent must be retired")
	r.Equal(models.AgentStatusRevoked, retired.Status)
	r.NotNil(retired.RevokedAt)

	_, live = liveAgent(t, dbSvc, freshSystem.UID)
	r.True(live, "a platform agent seen a minute ago must survive")

	orgAgent, live := liveAgent(t, dbSvc, staleOrg.UID)
	r.True(live, "org agents are user-managed and must never be GC'd")
	r.Equal(models.AgentStatusActive, orgAgent.Status)

	_, live = liveAgent(t, dbSvc, neverSeen.UID)
	r.False(live, "a platform agent that never connected is judged on enrolled_at")
}

// TestAgentGCDeletesRetiredAgentWorkerRow verifies the retired machine's
// workers row goes away with it — that row is what makes a dead fly machine
// keep showing up in the workers list.
func TestAgentGCDeletesRetiredAgentWorkerRow(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	jctx, dbSvc, _ := newAgentGCTestContext(t)

	longAgo := time.Now().Add(-30 * 24 * time.Hour)
	agent := models.NewSystemAgent("eu-west-1", "fly-machine", "ed-1", "x-1", "fp-1")
	seedAgent(t, dbSvc, agent, &longAgo, 30*24*time.Hour)

	// The workers row the WS handler would have registered for it.
	slug := agentcrypto.WorkerSlug(agent.UID)
	region := "eu-west-1"
	worker, err := dbSvc.RegisterOrUpdateWorker(t.Context(), &models.Worker{
		UID: agent.UID, Slug: slug, Name: "agent:" + agent.Name, Region: &region,
	})
	r.NoError(err)

	run := &jobtypes.AgentGCJobRun{}
	r.NoError(run.Run(t.Context(), jctx))

	var row models.Worker
	r.NoError(dbSvc.DB().NewSelect().Model(&row).Where("uid = ?", worker.UID).Scan(t.Context()))
	r.NotNil(row.DeletedAt, "the retired agent's worker row must be soft-deleted")

	live, err := dbSvc.DB().NewSelect().Model((*models.Worker)(nil)).
		Where("slug = ?", slug).Where("deleted_at IS NULL").Count(t.Context())
	r.NoError(err)
	r.Zero(live, "the retired agent's worker row must no longer resolve by slug")
}

// TestAgentGCPrunesNoncesAndReschedules covers the two housekeeping halves: old
// reconnect nonces are swept (the per-agent prune on insert never fires for a
// machine that stopped reconnecting) and the sweep queues its next run.
func TestAgentGCPrunesNoncesAndReschedules(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	jctx, dbSvc, _ := newAgentGCTestContext(t)

	agent := models.NewSystemAgent("eu-west-1", "fly-machine", "ed-1", "x-1", "fp-1")
	recently := time.Now().Add(-time.Minute)
	seedAgent(t, dbSvc, agent, &recently, time.Hour)

	// One long-consumed nonce and one just consumed.
	r.NoError(dbSvc.CheckAndStoreAgentNonce(
		t.Context(), agent.UID, "old", time.Now().Add(-2*time.Hour), 10*time.Minute))
	r.NoError(dbSvc.CheckAndStoreAgentNonce(
		t.Context(), agent.UID, "new", time.Now(), 10*time.Minute))

	run := &jobtypes.AgentGCJobRun{}
	r.NoError(run.Run(t.Context(), jctx))

	remaining, err := dbSvc.DB().NewSelect().Model((*models.AgentNonce)(nil)).Count(t.Context())
	r.NoError(err)
	r.Equal(1, remaining, "only the expired nonce is pruned")

	pending, err := countPendingAgentGCJobs(t.Context(), dbSvc)
	r.NoError(err)
	r.Equal(1, pending, "the sweep must schedule exactly one follow-up run")
}

// TestAgentGCStaleAfterIsConfigurable verifies the job config overrides the
// 7-day default window.
func TestAgentGCStaleAfterIsConfigurable(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	jctx, dbSvc, _ := newAgentGCTestContext(t)

	tenMinutesAgo := time.Now().Add(-10 * time.Minute)
	agent := models.NewSystemAgent("eu-west-1", "fly-machine", "ed-1", "x-1", "fp-1")
	seedAgent(t, dbSvc, agent, &tenMinutesAgo, time.Hour)

	// Default window (7 days): untouched.
	def := &jobtypes.AgentGCJobDefinition{}
	runner, err := def.CreateJobRun(nil)
	r.NoError(err)
	r.NoError(runner.Run(t.Context(), jctx))

	_, live := liveAgent(t, dbSvc, agent.UID)
	r.True(live, "10 minutes of silence is well inside the default window")

	// A 60-second window retires it.
	runner, err = def.CreateJobRun([]byte(`{"staleAfterSeconds": 60}`))
	r.NoError(err)
	r.NoError(runner.Run(t.Context(), jctx))

	_, live = liveAgent(t, dbSvc, agent.UID)
	r.False(live, "the configured window must be honored")
}

// TestAgentGCNoDBServiceIsNoop verifies the runner degrades to a no-op rather
// than panicking in a minimal (no-DB) job context.
func TestAgentGCNoDBServiceIsNoop(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	run := &jobtypes.AgentGCJobRun{}
	r.NoError(run.Run(t.Context(), &jobdef.JobContext{Logger: slog.Default()}))
}

// TestAgentGCPurgesOldRevokedAgentsOfAnyKind is the job-level test for spec
// 2026-08-16-03: a revoked agent older than the purge window disappears from
// ListAgents after one sweep, its worker row goes with it, a revoked agent
// still inside the window survives, and the clock is revoked_at — not
// last_seen_at, which would otherwise keep a still-fresh-looking revoked
// agent alive forever.
func TestAgentGCPurgesOldRevokedAgentsOfAnyKind(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	jctx, dbSvc, orgUID := newAgentGCTestContext(t)
	ctx := t.Context()

	threeDaysAgo := time.Now().Add(-3 * 24 * time.Hour)
	oneDayAgo := time.Now().Add(-24 * time.Hour)
	oneMinuteAgo := time.Now().Add(-time.Minute)

	// Revoked 3 days ago (past the 2-day default) — org kind.
	oldRevokedOrg := models.NewAgent(orgUID, "@acme/dc1", "old-revoked-org", "ed-1", "x-1", "fp-1")
	oldRevokedOrg.Status = models.AgentStatusRevoked
	oldRevokedOrg.RevokedAt = &threeDaysAgo
	seedAgent(t, dbSvc, oldRevokedOrg, nil, time.Hour)

	// The workers row the WS handler would have registered for it.
	slug := agentcrypto.WorkerSlug(oldRevokedOrg.UID)
	region := "@acme/dc1"
	worker, err := dbSvc.RegisterOrUpdateWorker(ctx, &models.Worker{
		UID: oldRevokedOrg.UID, Slug: slug, Name: "agent:" + oldRevokedOrg.Name, Region: &region,
	})
	r.NoError(err)

	// Revoked 3 days ago too, but seen a minute ago — the positive control:
	// the clock is revoked_at, not last_seen_at.
	oldRevokedFreshlySeen := models.NewSystemAgent("eu-west-1", "old-revoked-fresh-seen", "ed-2", "x-2", "fp-2")
	oldRevokedFreshlySeen.Status = models.AgentStatusRevoked
	oldRevokedFreshlySeen.RevokedAt = &threeDaysAgo
	seedAgent(t, dbSvc, oldRevokedFreshlySeen, &oneMinuteAgo, 4*24*time.Hour)

	// Revoked only 1 day ago — inside the 2-day window, must survive.
	recentlyRevoked := models.NewAgent(orgUID, "@acme/dc1", "recently-revoked", "ed-3", "x-3", "fp-3")
	recentlyRevoked.Status = models.AgentStatusRevoked
	recentlyRevoked.RevokedAt = &oneDayAgo
	seedAgent(t, dbSvc, recentlyRevoked, nil, 2*24*time.Hour)

	// Still active, however old — never purged.
	stillActive := models.NewAgent(orgUID, "@acme/dc1", "still-active", "ed-4", "x-4", "fp-4")
	seedAgent(t, dbSvc, stillActive, nil, 30*24*time.Hour)

	run := &jobtypes.AgentGCJobRun{}
	r.NoError(run.Run(ctx, jctx))

	_, live := liveAgent(t, dbSvc, oldRevokedOrg.UID)
	r.False(live, "a revoked agent past the purge window must be purged")

	_, live = liveAgent(t, dbSvc, oldRevokedFreshlySeen.UID)
	r.False(live, "revoked_at is the clock: a fresh last_seen_at must not save it")

	_, live = liveAgent(t, dbSvc, recentlyRevoked.UID)
	r.True(live, "a revoked agent inside the purge window must survive")

	_, live = liveAgent(t, dbSvc, stillActive.UID)
	r.True(live, "an active agent must never be purged")

	var workerRow models.Worker
	r.NoError(dbSvc.DB().NewSelect().Model(&workerRow).Where("uid = ?", worker.UID).Scan(ctx))
	r.NotNil(workerRow.DeletedAt, "the purged agent's worker row must be soft-deleted")
}

// TestAgentGCRevokedPurgeAfterIsConfigurable verifies the job config overrides
// the 2-day default revoked-agent retention window.
func TestAgentGCRevokedPurgeAfterIsConfigurable(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	jctx, dbSvc, orgUID := newAgentGCTestContext(t)
	ctx := t.Context()

	tenMinutesAgo := time.Now().Add(-10 * time.Minute)
	agent := models.NewAgent(orgUID, "@acme/dc1", "revoked-agent", "ed-1", "x-1", "fp-1")
	agent.Status = models.AgentStatusRevoked
	agent.RevokedAt = &tenMinutesAgo
	seedAgent(t, dbSvc, agent, nil, time.Hour)

	// Default window (2 days): untouched.
	def := &jobtypes.AgentGCJobDefinition{}
	runner, err := def.CreateJobRun(nil)
	r.NoError(err)
	r.NoError(runner.Run(ctx, jctx))

	_, live := liveAgent(t, dbSvc, agent.UID)
	r.True(live, "10 minutes since revocation is well inside the default window")

	// A 60-second window purges it.
	runner, err = def.CreateJobRun([]byte(`{"revokedPurgeAfterSeconds": 60}`))
	r.NoError(err)
	r.NoError(runner.Run(ctx, jctx))

	_, live = liveAgent(t, dbSvc, agent.UID)
	r.False(live, "the configured window must be honored")
}

func countPendingAgentGCJobs(ctx context.Context, dbSvc db.Service) (int, error) {
	return dbSvc.DB().NewSelect().
		Model((*models.Job)(nil)).
		Where("type = ?", string(jobdef.JobTypeAgentGC)).
		Where("status = ?", models.JobStatusPending).
		Where("deleted_at IS NULL").
		Count(ctx)
}

// TestAgentGCRescheduleKeepsConfig covers the second half of spec
// 2026-08-28-04: the sweep used to reschedule itself with a nil config, so an
// operator override of the windows survived exactly one run and then silently
// reverted to the 7-day / 2-day defaults.
func TestAgentGCRescheduleKeepsConfig(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	jctx, dbSvc, _ := newAgentGCTestContext(t)
	ctx := t.Context()

	def := &jobtypes.AgentGCJobDefinition{}
	runner, err := def.CreateJobRun([]byte(`{"staleAfterSeconds":60,"revokedPurgeAfterSeconds":120}`))
	r.NoError(err)
	r.NoError(runner.Run(ctx, jctx))

	var followUp models.Job
	r.NoError(dbSvc.DB().NewSelect().
		Model(&followUp).
		Where("type = ?", string(jobdef.JobTypeAgentGC)).
		Where("status = ?", models.JobStatusPending).
		Where("deleted_at IS NULL").
		Scan(ctx), "the sweep must schedule a follow-up run")

	r.EqualValues(60, followUp.Config["staleAfterSeconds"],
		"the follow-up run must inherit the configured stale window")
	r.EqualValues(120, followUp.Config["revokedPurgeAfterSeconds"],
		"the follow-up run must inherit the configured revoked-purge window")

	// And the configured windows still apply after that hop: running the
	// follow-up's own runner retires an agent the defaults would have spared.
	tenMinutesAgo := time.Now().Add(-10 * time.Minute)
	agent := models.NewSystemAgent("eu-west-1", "fly-machine", "ed-1", "x-1", "fp-1")
	seedAgent(t, dbSvc, agent, &tenMinutesAgo, time.Hour)

	config, err := json.Marshal(followUp.Config)
	r.NoError(err)

	next, err := def.CreateJobRun(config)
	r.NoError(err)
	r.NoError(next.Run(ctx, jctx))

	_, live := liveAgent(t, dbSvc, agent.UID)
	r.False(live, "the config must still be in force one reschedule later")
}

// TestAgentGCRescheduleDefaultConfigStaysEmpty guards the dedup contract: the
// zero config must serialize to exactly what the job service derives from a
// nil config, or every sweep would queue a second, differently-configured
// pending job instead of updating the existing one.
func TestAgentGCRescheduleDefaultConfigStaysEmpty(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	jctx, dbSvc, _ := newAgentGCTestContext(t)
	ctx := t.Context()

	def := &jobtypes.AgentGCJobDefinition{}
	runner, err := def.CreateJobRun(nil)
	r.NoError(err)
	r.NoError(runner.Run(ctx, jctx))
	r.NoError(runner.Run(ctx, jctx))

	pending, err := countPendingAgentGCJobs(ctx, dbSvc)
	r.NoError(err)
	r.Equal(1, pending, "a default-config sweep must keep bouncing the same pending job")

	var followUp models.Job
	r.NoError(dbSvc.DB().NewSelect().
		Model(&followUp).
		Where("type = ?", string(jobdef.JobTypeAgentGC)).
		Where("status = ?", models.JobStatusPending).
		Where("deleted_at IS NULL").
		Scan(ctx))
	r.Empty(followUp.Config, "the default config must stay an empty map")
}
