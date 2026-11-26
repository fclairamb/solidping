package checkjobsvc_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"github.com/uptrace/bun"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/checkworker/checkjobsvc"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/postgres"
	"github.com/fclairamb/solidping/server/internal/testsupport"
)

// futureType stands for a built-in type added after the legacy list was frozen.
const futureType = "futuretype"

// checkTypeDB is what both database services offer the scenario.
type checkTypeDB interface {
	CreateCheck(ctx context.Context, check *models.Check) error
	RegisterOrUpdateWorker(ctx context.Context, worker *models.Worker) (*models.Worker, error)
	DB() *bun.DB
}

func createTypedPrivateJob(
	ctx context.Context, t *testing.T, db checkTypeDB, orgUID, checkType, region string,
) *models.CheckJob {
	t.Helper()

	check := models.NewCheck(orgUID, "typed-"+uuid.New().String()[:8], checkType)
	check.Config = models.JSONMap{"host": "acme.example.com"}
	require.NoError(t, db.CreateCheck(ctx, check))

	job := new(models.CheckJob)
	require.NoError(t, db.DB().NewSelect().Model(job).Where("check_uid = ?", check.UID).Scan(ctx))

	due := time.Now().Add(-time.Minute)
	_, err := db.DB().NewUpdate().Model((*models.CheckJob)(nil)).
		Set("scheduled_at = ?", due).Set("effective_scheduled_at = ?", due).
		Set("region = ?", region).Where("uid = ?", job.UID).Exec(ctx)
	require.NoError(t, err)

	return job
}

// runCheckTypeClaimScenario pins the claim predicate of spec 2026-11-26-02 on
// the given database.
func runCheckTypeClaimScenario(ctx context.Context, t *testing.T, db checkTypeDB, orgUID string) {
	t.Helper()

	r := require.New(t)
	svc := checkjobsvc.NewService(db.DB())
	region := "@office"

	worker, err := db.RegisterOrUpdateWorker(ctx, models.NewWorker("agent-"+uuid.NewString()[:8], "Agent"))
	r.NoError(err)

	claim := func(capabilities []string) map[string]bool {
		scope := checkjobsvc.AgentScope{
			OrgUID: orgUID, Region: region,
			CheckTypes: checkjobsvc.CheckTypesFromCapabilities(capabilities),
		}

		jobs, _, claimErr := svc.ClaimJobsForAgent(ctx, worker.UID, scope, "", 50, 5*time.Minute)
		r.NoError(claimErr)

		got := jobUIDs(jobs)
		for _, job := range jobs {
			r.NoError(svc.ReleaseLease(ctx, job.UID, worker.UID, time.Now().Add(-time.Minute)))
		}

		return got
	}

	httpJob := createTypedPrivateJob(ctx, t, db, orgUID, "http", region)
	tcpJob := createTypedPrivateJob(ctx, t, db, orgUID, "tcp", region)
	futureJob := createTypedPrivateJob(ctx, t, db, orgUID, futureType, region)

	// An agent reporting check-type-http only gets http jobs; the others stay
	// queued for another worker.
	got := claim([]string{"ipv4", models.CheckTypeCapability("http")})
	r.True(got[httpJob.UID])
	r.False(got[tcpJob.UID], "tcp was not reported")
	r.False(got[futureJob.UID], "an unreported type stays queued")

	// An agent reporting the future type gets it.
	got = claim([]string{models.CheckTypeCapability(futureType)})
	r.True(got[futureJob.UID])
	r.False(got[httpJob.UID])

	// A legacy agent (a set with no check-type-*, or nil) gets the frozen
	// list's types and never a type outside it.
	for _, legacy := range [][]string{{"ipv4", "ipv6"}, nil} {
		got = claim(legacy)
		r.True(got[httpJob.UID])
		r.True(got[tcpJob.UID])
		r.False(got[futureJob.UID], "a legacy agent must not claim a type newer than its list")
	}
}

//nolint:paralleltest // shares database state, like its siblings in this package
func TestAgentClaimCheckTypes(t *testing.T) {
	dbSvc, ctx := setupTestDB(t)
	defer func() { _ = dbSvc.Close() }()

	org := createTestOrg(t, ctx, dbSvc)

	runCheckTypeClaimScenario(ctx, t, dbSvc, org.UID)
}

func TestAgentClaimCheckTypes_Postgres(t *testing.T) {
	t.Parallel()

	if testing.Short() {
		t.Skip("skipping embedded-postgres test in -short mode")
	}

	ctx := t.Context()

	dbSvc, err := postgres.New(ctx, &postgres.Config{Embedded: true, Port: 15523, RunMode: "test"})
	if err != nil {
		testsupport.PostgresUnavailable(t, err)
	}

	t.Cleanup(func() { _ = dbSvc.Close() })

	if initErr := dbSvc.Initialize(ctx); initErr != nil {
		testsupport.PostgresInitFailed(t, initErr)
	}

	org := models.NewOrganization("typespg", "Types PG Org")
	require.NoError(t, dbSvc.CreateOrganization(ctx, org))

	runCheckTypeClaimScenario(ctx, t, dbSvc, org.UID)
}

func TestCheckTypesFromCapabilities(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	r.Empty(checkjobsvc.CheckTypesFromCapabilities(nil))
	r.Empty(checkjobsvc.CheckTypesFromCapabilities([]string{"ipv4", "x-foo"}))
	r.Equal(
		[]string{"http", string(checkerdef.CheckTypeFreeboxLine)},
		checkjobsvc.CheckTypesFromCapabilities([]string{
			"ipv4", models.CheckTypeCapability("http"), models.CheckTypeCapability(string(checkerdef.CheckTypeFreeboxLine)),
		}),
		"an underscore type round-trips through its slug",
	)
}

// The legacy list is frozen: it only holds registered types, and a type added
// after it was frozen must not be in it.
func TestLegacyCheckTypesAreRegistered(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	registered := map[string]bool{}
	for _, checkType := range checkerdef.ListCheckTypes(nil) {
		registered[string(checkType)] = true
	}

	legacy := checkjobsvc.LegacyCheckTypes()
	r.NotEmpty(legacy)

	for _, name := range legacy {
		r.Truef(registered[name], "legacy type %q is not a registered check type", name)
	}

	r.NotContains(legacy, futureType)
}
