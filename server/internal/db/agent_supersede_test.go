package db_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	agentcrypto "github.com/fclairamb/solidping/server/internal/agents"
	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/postgres"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
)

// portAgentSupersede is distinct from every other embedded-Postgres port in
// the repo (see the port-numbering note in
// internal/db/postgres/postgres_headroom_postgres_test.go).
const portAgentSupersede = 15503

// seedSupersedeAgent inserts an agent row directly with a chosen last_seen_at.
// Enrollment goes through a token; the predecessors these tests need are just
// rows already in the table.
func seedSupersedeAgent(ctx context.Context, t *testing.T, svc db.Service, agent *models.Agent, lastSeen *time.Time) {
	t.Helper()

	agent.LastSeenAt = lastSeen

	_, err := svc.DB().NewInsert().Model(agent).Exec(ctx)
	require.NoError(t, err)
}

// supersedeAgentRow reads an agent row back by UID, whatever its state.
func supersedeAgentRow(ctx context.Context, t *testing.T, svc db.Service, uid string) *models.Agent {
	t.Helper()

	var agent models.Agent

	require.NoError(t, svc.DB().NewSelect().Model(&agent).Where("uid = ?", uid).Scan(ctx))

	return &agent
}

// seedSupersedeWorker registers the workers row the WS handler would have
// created for an agent, under the same deterministic slug.
func seedSupersedeWorker(ctx context.Context, t *testing.T, svc db.Service, agent *models.Agent) *models.Worker {
	t.Helper()

	region := agent.Region
	worker, err := svc.RegisterOrUpdateWorker(ctx, &models.Worker{
		UID:    agent.UID,
		Slug:   agentcrypto.WorkerSlug(agent.UID),
		Name:   "agent:" + agent.Name,
		Region: &region,
	})
	require.NoError(t, err)

	return worker
}

// workerDeleted reports whether a workers row has been soft-deleted.
func workerDeleted(ctx context.Context, t *testing.T, svc db.Service, uid string) bool {
	t.Helper()

	var worker models.Worker

	require.NoError(t, svc.DB().NewSelect().Model(&worker).Where("uid = ?", uid).Scan(ctx))

	return worker.DeletedAt != nil
}

// mintSystemToken creates a multi-use platform enrollment token for a region
// and returns its hash (what EnrollAgent is called with).
func mintSystemToken(ctx context.Context, t *testing.T, svc db.Service, region string) string {
	t.Helper()

	_, hash, err := agentcrypto.GenerateEnrollmentToken()
	require.NoError(t, err)

	require.NoError(t, svc.CreateAgentEnrollmentToken(
		ctx, models.NewSystemAgentEnrollmentToken(region, hash, time.Now().Add(time.Hour), nil)))

	return hash
}

// testSupersedeOnEnroll is the whole spec 2026-08-28-04 matrix, run against
// one dialect: a replaced system agent's predecessors are retired inside the
// enrollment transaction, and nothing else is.
func testSupersedeOnEnroll(ctx context.Context, t *testing.T, svc db.Service) {
	t.Helper()
	r := require.New(t)

	org := models.NewOrganization("acme-supersede", "")
	r.NoError(svc.CreateOrganization(ctx, org))

	const (
		region = "supersede-eu-west-1"
		name   = "kansas-city-k8s"
	)

	longAgo := time.Now().Add(-3 * time.Hour)
	justNow := time.Now().Add(-time.Minute)

	// The row a redeploy left behind: same region, same name, silent for hours.
	replaced := models.NewSystemAgent(region, name, "ed-replaced", "x-replaced", "fp-replaced")
	seedSupersedeAgent(ctx, t, svc, replaced, &longAgo)
	replacedWorker := seedSupersedeWorker(ctx, t, svc, replaced)

	// A predecessor that enrolled and never connected at all: no last_seen_at.
	neverSeen := models.NewSystemAgent(region, name, "ed-never", "x-never", "fp-never")
	seedSupersedeAgent(ctx, t, svc, neverSeen, nil)

	// THE POSITIVE CONTROL: a same-name agent that phoned in a minute ago is a
	// live fleet peer, not a replaced machine. Retiring it would kill a working
	// agent, so the last_seen_at guard must protect it.
	livePeer := models.NewSystemAgent(region, name, "ed-live", "x-live", "fp-live")
	seedSupersedeAgent(ctx, t, svc, livePeer, &justNow)
	livePeerWorker := seedSupersedeWorker(ctx, t, svc, livePeer)

	// Same region, different name: a different machine of the same fleet.
	otherName := models.NewSystemAgent(region, "tokyo-fly", "ed-other-name", "x-other-name", "fp-other-name")
	seedSupersedeAgent(ctx, t, svc, otherName, &longAgo)

	// Same name, different region: a different deployment entirely.
	otherRegion := models.NewSystemAgent("supersede-us-east-1", name, "ed-other-reg", "x-other-reg", "fp-other-reg")
	seedSupersedeAgent(ctx, t, svc, otherRegion, &longAgo)

	// A customer-managed agent that happens to carry the same region and name.
	// Org agents are never superseded: offline is not replaced.
	orgAgent := models.NewAgent(org.UID, region, name, "ed-org", "x-org", "fp-org")
	seedSupersedeAgent(ctx, t, svc, orgAgent, &longAgo)
	orgWorker := seedSupersedeWorker(ctx, t, svc, orgAgent)

	hash := mintSystemToken(ctx, t, svc, region)

	newAgent, err := svc.EnrollAgent(ctx, hash, name, "ed-new", "x-new", "fp-new")
	r.NoError(err)
	r.Equal(models.AgentKindSystem, newAgent.Kind)

	// The newcomer itself is untouched.
	fresh := supersedeAgentRow(ctx, t, svc, newAgent.UID)
	r.Nil(fresh.DeletedAt, "the freshly enrolled agent must never supersede itself")
	r.Equal(models.AgentStatusActive, fresh.Status)

	// The replaced machine is retired: same terminal state as RetireSystemAgent.
	retired := supersedeAgentRow(ctx, t, svc, replaced.UID)
	r.NotNil(retired.DeletedAt, "a stale same-(region,name) predecessor must be superseded")
	r.NotNil(retired.RevokedAt)
	r.Equal(models.AgentStatusRevoked, retired.Status)
	r.True(workerDeleted(ctx, t, svc, replacedWorker.UID),
		"the superseded agent's worker row must be soft-deleted with it")

	ghost := supersedeAgentRow(ctx, t, svc, neverSeen.UID)
	r.NotNil(ghost.DeletedAt, "a predecessor that never connected has no last_seen_at to protect it")

	peer := supersedeAgentRow(ctx, t, svc, livePeer.UID)
	r.Nil(peer.DeletedAt, "a same-name agent seen a minute ago is a live fleet peer and must survive")
	r.Equal(models.AgentStatusActive, peer.Status)
	r.False(workerDeleted(ctx, t, svc, livePeerWorker.UID),
		"a live fleet peer's worker row must survive too")

	sibling := supersedeAgentRow(ctx, t, svc, otherName.UID)
	r.Nil(sibling.DeletedAt, "a different-name agent in the same region must survive")

	elsewhere := supersedeAgentRow(ctx, t, svc, otherRegion.UID)
	r.Nil(elsewhere.DeletedAt, "a same-name agent in another region must survive")

	customer := supersedeAgentRow(ctx, t, svc, orgAgent.UID)
	r.Nil(customer.DeletedAt, "org agents are customer-managed and must never be superseded")
	r.Equal(models.AgentStatusActive, customer.Status)
	r.False(workerDeleted(ctx, t, svc, orgWorker.UID),
		"an org agent's worker row must never be cleaned up by an enrollment")
}

// testSupersedeSkipsOrgEnrollment is the org exclusion seen from the other
// side: enrolling an ORG agent supersedes nothing, however stale its
// same-(region,name) namesake is.
func testSupersedeSkipsOrgEnrollment(ctx context.Context, t *testing.T, svc db.Service) {
	t.Helper()
	r := require.New(t)

	org := models.NewOrganization("acme-supersede-org", "")
	r.NoError(svc.CreateOrganization(ctx, org))

	region := "@acme-supersede-org/dc1"
	longAgo := time.Now().Add(-30 * 24 * time.Hour)

	existing := models.NewAgent(org.UID, region, "on-prem-1", "ed-existing-org", "x-existing-org", "fp-existing-org")
	seedSupersedeAgent(ctx, t, svc, existing, &longAgo)

	_, hash, err := agentcrypto.GenerateEnrollmentToken()
	r.NoError(err)
	r.NoError(svc.CreateAgentEnrollmentToken(
		ctx, models.NewAgentEnrollmentToken(org.UID, region, hash, time.Now().Add(time.Hour), nil)))

	_, err = svc.EnrollAgent(ctx, hash, "on-prem-1", "ed-new-org", "x-new-org", "fp-new-org")
	r.NoError(err)

	survivor := supersedeAgentRow(ctx, t, svc, existing.UID)
	r.Nil(survivor.DeletedAt, "an org enrollment must never retire the customer's other agents")
	r.Equal(models.AgentStatusActive, survivor.Status)
}

func TestAgentSupersedeOnEnrollSQLite(t *testing.T) {
	t.Parallel()

	ctx := t.Context()

	svc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	require.NoError(t, err)

	t.Cleanup(func() { _ = svc.Close() })
	require.NoError(t, svc.Initialize(ctx))

	testSupersedeOnEnroll(ctx, t, svc)
	testSupersedeSkipsOrgEnrollment(ctx, t, svc)
}

// TestAgentSupersedeOnEnrollPostgres runs the identical matrix against real
// PostgreSQL: the two dialects carry parallel EnrollAgent implementations and
// this is what keeps the supersede behavior from drifting between them.
// Self-skips under -short, like every other embedded-Postgres test.
//
//nolint:paralleltest // shares dev-machine resources (embedded-postgres-go's pwfile extraction)
func TestAgentSupersedeOnEnrollPostgres(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping embedded-Postgres test in short mode")
	}

	ctx := t.Context()

	svc, err := postgres.New(ctx, &postgres.Config{
		Embedded: true, Port: portAgentSupersede, RunMode: "test",
	})
	if err != nil {
		t.Skipf("embedded postgres unavailable: %v", err)
	}

	t.Cleanup(func() { _ = svc.Close() })

	if initErr := svc.Initialize(ctx); initErr != nil {
		t.Skipf("embedded postgres init failed: %v", initErr)
	}

	testSupersedeOnEnroll(ctx, t, svc)
	testSupersedeSkipsOrgEnrollment(ctx, t, svc)
}
