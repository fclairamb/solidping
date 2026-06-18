package jobsadmin_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/handlers/jobsadmin"
)

func newTestService(t *testing.T) (context.Context, *require.Assertions, *sqlite.Service, jobsadmin.Service) {
	t.Helper()

	ctx := t.Context()
	r := require.New(t)

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	return ctx, r, dbSvc, jobsadmin.NewService(dbSvc.DB())
}

func TestListSystemJobsIncludesNullOrgJobs(t *testing.T) {
	t.Parallel()

	ctx, r, dbSvc, svc := newTestService(t)

	org := models.NewOrganization("jaorg", "JA")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	orgJob := models.NewJob(&org.UID, "email")
	r.NoError(dbSvc.CreateJob(ctx, orgJob))

	// System job: organization_uid IS NULL.
	sysJob := models.NewJob(nil, "aggregation")
	r.NoError(dbSvc.CreateJob(ctx, sysJob))

	// System mode (orgUID empty) sees both.
	all, err := svc.ListJobs(ctx, "", jobsadmin.ListOptions{Limit: 100})
	r.NoError(err)
	r.Len(all, 2)

	// Org mode sees only the org job, never the NULL-org system job.
	orgJobs, err := svc.ListJobs(ctx, org.UID, jobsadmin.ListOptions{Limit: 100})
	r.NoError(err)
	r.Len(orgJobs, 1)
	r.Equal(orgJob.UID, orgJobs[0].UID)
}

func TestListJobsFiltersAndActiveSort(t *testing.T) {
	t.Parallel()

	ctx, r, dbSvc, svc := newTestService(t)

	org := models.NewOrganization("jforg", "JF")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	mk := func(status models.JobStatus, jobType string) {
		job := models.NewJob(&org.UID, jobType)
		job.Status = status
		r.NoError(dbSvc.CreateJob(ctx, job))
	}

	mk(models.JobStatusSuccess, "email")
	mk(models.JobStatusPending, "webhook")
	mk(models.JobStatusRunning, "email")

	// Status filter.
	pending, err := svc.ListJobs(ctx, org.UID, jobsadmin.ListOptions{Status: "pending", Limit: 100})
	r.NoError(err)
	r.Len(pending, 1)
	r.Equal(models.JobStatusPending, pending[0].Status)

	// Type filter.
	emails, err := svc.ListJobs(ctx, org.UID, jobsadmin.ListOptions{Type: "email", Limit: 100})
	r.NoError(err)
	r.Len(emails, 2)

	// Active-first sort: pending/running before success.
	all, err := svc.ListJobs(ctx, org.UID, jobsadmin.ListOptions{Limit: 100})
	r.NoError(err)
	r.Len(all, 3)
	r.Contains([]models.JobStatus{models.JobStatusPending, models.JobStatusRunning}, all[0].Status)
	r.Contains([]models.JobStatus{models.JobStatusPending, models.JobStatusRunning}, all[1].Status)
	r.Equal(models.JobStatusSuccess, all[2].Status)
}

func TestGetJobOrgScoping(t *testing.T) {
	t.Parallel()

	ctx, r, dbSvc, svc := newTestService(t)

	orgA := models.NewOrganization("gaorg", "GA")
	orgB := models.NewOrganization("gborg", "GB")
	r.NoError(dbSvc.CreateOrganization(ctx, orgA))
	r.NoError(dbSvc.CreateOrganization(ctx, orgB))

	job := models.NewJob(&orgA.UID, "email")
	r.NoError(dbSvc.CreateJob(ctx, job))

	_, err := svc.GetJob(ctx, orgB.UID, job.UID)
	r.ErrorIs(err, jobsadmin.ErrJobNotFound)

	got, err := svc.GetJob(ctx, orgA.UID, job.UID)
	r.NoError(err)
	r.Equal(job.UID, got.UID)

	gotSys, err := svc.GetJob(ctx, "", job.UID)
	r.NoError(err)
	r.Equal(job.UID, gotSys.UID)
}

func TestRetryChain(t *testing.T) {
	t.Parallel()

	ctx, r, dbSvc, svc := newTestService(t)

	org := models.NewOrganization("rcorg", "RC")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	// Build chain: j1 (failed) -> j2 (failed) -> j3 (success)
	j1 := models.NewJob(&org.UID, "email")
	j1.Status = models.JobStatusRetried
	r.NoError(dbSvc.CreateJob(ctx, j1))

	j2 := models.NewJob(&org.UID, "email")
	j2.Status = models.JobStatusRetried
	j2.PreviousJobUID = &j1.UID
	r.NoError(dbSvc.CreateJob(ctx, j2))

	j3 := models.NewJob(&org.UID, "email")
	j3.Status = models.JobStatusSuccess
	j3.PreviousJobUID = &j2.UID
	r.NoError(dbSvc.CreateJob(ctx, j3))

	// From the middle job, the full chain must be reconstructed in order.
	chain, err := svc.RetryChain(ctx, org.UID, j2.UID)
	r.NoError(err)
	r.Len(chain, 3)
	r.Equal(j1.UID, chain[0].UID)
	r.Equal(j2.UID, chain[1].UID)
	r.Equal(j3.UID, chain[2].UID)

	// From the root.
	chainFromRoot, err := svc.RetryChain(ctx, org.UID, j1.UID)
	r.NoError(err)
	r.Len(chainFromRoot, 3)
	r.Equal(j1.UID, chainFromRoot[0].UID)

	// Single job with no chain.
	solo := models.NewJob(&org.UID, "webhook")
	r.NoError(dbSvc.CreateJob(ctx, solo))
	chainSolo, err := svc.RetryChain(ctx, org.UID, solo.UID)
	r.NoError(err)
	r.Len(chainSolo, 1)
	r.Equal(solo.UID, chainSolo[0].UID)
}
