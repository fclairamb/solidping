package jobsvc_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/jobs/jobdef"
	"github.com/fclairamb/solidping/server/internal/jobs/jobsvc"
	"github.com/fclairamb/solidping/server/internal/notifier"
)

// TestListJobsTypesFilter verifies the Types option returns jobs matching any
// of the given types (used by the discovery ListScans endpoint to surface both
// network_discovery and freebox_lan_discovery runs).
func TestListJobsTypesFilter(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	r := require.New(t)

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	org := models.NewOrganization("jobs", "Jobs Org")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	svc := jobsvc.NewService(dbSvc.DB(), dbSvc, notifier.NewLocalEventNotifier(), nil)

	mkJob := func(jobType jobdef.JobType) {
		job := models.NewJob(&org.UID, string(jobType))
		r.NoError(dbSvc.CreateJob(ctx, job))
	}

	mkJob(jobdef.JobTypeNetworkDiscovery)
	mkJob(jobdef.JobTypeFreeboxLanDiscovery)
	mkJob(jobdef.JobTypeAggregation) // must be excluded

	jobs, err := svc.ListJobs(ctx, org.UID, jobsvc.ListJobsOptions{
		Types: []string{
			string(jobdef.JobTypeNetworkDiscovery),
			string(jobdef.JobTypeFreeboxLanDiscovery),
		},
	})
	r.NoError(err)
	r.Len(jobs, 2, "both discovery job types must be returned, aggregation excluded")

	types := map[string]bool{}
	for _, j := range jobs {
		types[j.Type] = true
	}
	r.True(types[string(jobdef.JobTypeNetworkDiscovery)])
	r.True(types[string(jobdef.JobTypeFreeboxLanDiscovery)])
}
