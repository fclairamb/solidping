package jobtypes_test

import (
	"context"
	"encoding/json"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/app/services"
	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	disc "github.com/fclairamb/solidping/server/internal/discovery"
	"github.com/fclairamb/solidping/server/internal/jobs/jobdef"
	"github.com/fclairamb/solidping/server/internal/jobs/jobsvc"
	"github.com/fclairamb/solidping/server/internal/jobs/jobtypes"
	"github.com/fclairamb/solidping/server/internal/notifier"
)

// planFixture stands up an in-memory db, a real job service, an org, and a plan
// job row, so the plan runner can schedule real child jobs.
type planFixture struct {
	dbSvc  db.Service
	jobSvc jobsvc.Service
	org    *models.Organization
	plan   *models.Job
}

func newPlanFixture(t *testing.T) *planFixture {
	t.Helper()

	ctx := t.Context()
	r := require.New(t)

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	org := models.NewOrganization("plan-disc", "Plan Discovery Org")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	jobSvc := jobsvc.NewService(dbSvc.DB(), dbSvc, notifier.NewLocalEventNotifier())

	plan := models.NewJob(&org.UID, string(jobdef.JobTypeNetworkDiscoveryPlan))
	r.NoError(dbSvc.CreateJob(ctx, plan))

	return &planFixture{dbSvc: dbSvc, jobSvc: jobSvc, org: org, plan: plan}
}

func (f *planFixture) runPlan(t *testing.T, cfg disc.Config) error {
	t.Helper()

	r := require.New(t)

	def := &jobtypes.NetworkDiscoveryPlanJobDefinition{}
	cfgBytes, err := json.Marshal(cfg)
	r.NoError(err)

	runner, err := def.CreateJobRun(cfgBytes)
	r.NoError(err)

	jctx := &jobdef.JobContext{
		OrganizationUID: &f.org.UID,
		Job:             f.plan,
		DB:              f.dbSvc.DB(),
		DBService:       f.dbSvc,
		Services:        &services.Registry{Jobs: f.jobSvc},
		Logger:          slog.Default(),
	}

	return runner.Run(context.Background(), jctx)
}

func (f *planFixture) childJobs(t *testing.T) []*models.Job {
	t.Helper()

	jobs, err := f.jobSvc.ListJobs(t.Context(), f.org.UID, jobsvc.ListJobsOptions{
		Type: string(jobdef.JobTypeNetworkDiscovery),
	})
	require.NoError(t, err)

	return jobs
}

func TestNetworkDiscoveryPlanFansOut(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	f := newPlanFixture(t)

	// A /18 splits into 4 ≤/20 chunks → 4 child jobs.
	r.NoError(f.runPlan(t, disc.Config{
		CIDRs:       []string{"10.0.0.0/18"},
		Ports:       []int{22, 80},
		Timeout:     "2s",
		Concurrency: 32,
	}))

	children := f.childJobs(t)
	r.Len(children, 4)

	// Each child carries the plan UID as parentJobUid plus inherited tuning.
	for _, child := range children {
		var cfg disc.Config
		raw, err := json.Marshal(child.Config)
		r.NoError(err)
		r.NoError(json.Unmarshal(raw, &cfg))

		r.Equal(f.plan.UID, cfg.ParentJobUID)
		r.Equal([]int{22, 80}, cfg.Ports)
		r.Equal("2s", cfg.Timeout)
		r.Equal(32, cfg.Concurrency)
		r.NoError(disc.ValidateCIDRs(cfg.CIDRs), "every child chunk must be a valid bounded scan")
	}

	// The plan records the chunk total as the progress denominator.
	r.NotNil(f.plan.Output)
	r.Equal(4, toInt(f.plan.Output["totalChunks"]))
}

func TestNetworkDiscoveryPlanSingleChunk(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	f := newPlanFixture(t)

	r.NoError(f.runPlan(t, disc.Config{CIDRs: []string{"192.168.1.0/24"}}))

	children := f.childJobs(t)
	r.Len(children, 1)
}

func TestNetworkDiscoveryPlanRejectsTooLarge(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	def := &jobtypes.NetworkDiscoveryPlanJobDefinition{}
	cfgBytes, err := json.Marshal(disc.Config{CIDRs: []string{"10.0.0.0/11"}})
	r.NoError(err)

	_, err = def.CreateJobRun(cfgBytes)
	r.ErrorIs(err, disc.ErrRangeTooLarge)
}

// toInt coerces a JSON-decoded numeric value to int for assertions.
func toInt(v any) int {
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	case int64:
		return int(n)
	default:
		return -1
	}
}
