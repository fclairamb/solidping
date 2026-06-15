package checkjobs_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/handlers/checkjobs"
	"github.com/fclairamb/solidping/server/internal/utils/timeutils"
)

// newTestService spins up an in-memory SQLite DB and returns the service plus
// the db handle so tests can seed rows directly.
func newTestService(t *testing.T) (context.Context, *require.Assertions, *sqlite.Service, checkjobs.Service) {
	t.Helper()

	ctx := t.Context()
	r := require.New(t)

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	return ctx, r, dbSvc, checkjobs.NewService(dbSvc.DB())
}

func ptrStr(s string) *string        { return &s }
func ptrTime(t time.Time) *time.Time { return &t }

// newCheck creates a check with auto check-job creation disabled so tests can
// seed check_jobs rows explicitly. CreateCheck otherwise materializes a
// check_job for every enabled check.
func newCheck(
	ctx context.Context, r *require.Assertions, dbSvc *sqlite.Service, orgUID, slug, name string,
) *models.Check {
	check := models.NewCheck(orgUID, slug, "http")
	check.Enabled = false
	if name != "" {
		check.Name = ptrStr(name)
	}
	r.NoError(dbSvc.CreateCheck(ctx, check))

	return check
}

func TestDerivedStateComputation(t *testing.T) {
	t.Parallel()

	ctx, r, dbSvc, svc := newTestService(t)

	org := models.NewOrganization("cjorg", "CJ Org")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	now := time.Now()

	// One check per job: check_jobs has a UNIQUE(check_uid, region) index.
	mk := func(modify func(cj *models.CheckJob)) string {
		check := newCheck(ctx, r, dbSvc, org.UID, "", "Web Check")
		cj := models.NewCheckJob(org.UID, check.UID, timeutils.Duration(time.Minute))
		cj.Type = "http"
		modify(cj)
		r.NoError(dbSvc.CreateCheckJob(ctx, cj))

		return cj.UID
	}

	idleUID := mk(func(_ *models.CheckJob) {})
	inFlightUID := mk(func(cj *models.CheckJob) {
		cj.LeaseExpiresAt = ptrTime(now.Add(30 * time.Second))
	})
	stalledUID := mk(func(cj *models.CheckJob) {
		cj.LeaseExpiresAt = ptrTime(now.Add(-30 * time.Second))
	})
	crashUID := mk(func(cj *models.CheckJob) {
		cj.LeaseStarts = 12
	})

	byUID := map[string]checkjobs.DerivedState{}

	views, err := svc.ListCheckJobs(ctx, org.UID, checkjobs.ListOptions{Limit: 100})
	r.NoError(err)
	r.Len(views, 4)

	for _, v := range views {
		byUID[v.UID] = v.State
		// Every view must carry the resolved check name.
		r.NotNil(v.CheckName)
		r.Equal("Web Check", *v.CheckName)
		r.Equal(int64(60), v.PeriodSeconds)
	}

	r.Equal(checkjobs.StateIdle, byUID[idleUID])
	r.Equal(checkjobs.StateInFlight, byUID[inFlightUID])
	r.Equal(checkjobs.StateStalled, byUID[stalledUID])
	r.Equal(checkjobs.StateCrashLooping, byUID[crashUID])
}

func TestSecretsRedaction(t *testing.T) {
	t.Parallel()

	ctx, r, dbSvc, svc := newTestService(t)

	org := models.NewOrganization("redactorg", "Redact Org")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	check := newCheck(ctx, r, dbSvc, org.UID, "secret-check", "Secret Check")

	cj := models.NewCheckJob(org.UID, check.UID, timeutils.Duration(time.Minute))
	cj.Type = "http"
	cj.Config = models.JSONMap{"url": "https://example.com"}
	cj.Encrypted = true
	// Simulate an encrypted envelope + key-name index.
	cj.ConfigPrivate = ptrStr(`{"ciphertext":"SUPER-SECRET-DO-NOT-LEAK"}`)
	cj.ConfigPrivateKeys = ptrStr(`["password","token"]`)
	r.NoError(dbSvc.CreateCheckJob(ctx, cj))

	// List view must redact.
	views, err := svc.ListCheckJobs(ctx, org.UID, checkjobs.ListOptions{Limit: 100})
	r.NoError(err)
	r.Len(views, 1)
	r.True(views[0].Encrypted)
	r.ElementsMatch([]string{"password", "token"}, views[0].EncryptedKeys)
	r.NotContains(views[0].Config, "password")
	r.NotContains(views[0].Config, "token")

	// Detail view must redact too.
	detail, err := svc.GetCheckJob(ctx, org.UID, cj.UID)
	r.NoError(err)
	r.ElementsMatch([]string{"password", "token"}, detail.EncryptedKeys)
	r.Equal("https://example.com", detail.Config["url"])
}

func TestListPaginationAndOrgScoping(t *testing.T) {
	t.Parallel()

	ctx, r, dbSvc, svc := newTestService(t)

	orgA := models.NewOrganization("orgaaa", "Org A")
	orgB := models.NewOrganization("orgbbb", "Org B")
	r.NoError(dbSvc.CreateOrganization(ctx, orgA))
	r.NoError(dbSvc.CreateOrganization(ctx, orgB))

	seed := func(org *models.Organization, count int) {
		for i := 0; i < count; i++ {
			check := newCheck(ctx, r, dbSvc, org.UID, "", "")
			cj := models.NewCheckJob(org.UID, check.UID, timeutils.Duration(time.Minute))
			cj.Type = "http"
			r.NoError(dbSvc.CreateCheckJob(ctx, cj))
		}
	}

	seed(orgA, 5)
	seed(orgB, 3)

	// Org-scoped: only org A's rows.
	a, err := svc.ListCheckJobs(ctx, orgA.UID, checkjobs.ListOptions{Limit: 100})
	r.NoError(err)
	r.Len(a, 5)

	for _, v := range a {
		r.Equal(orgA.UID, v.OrganizationUID)
	}

	// Pagination.
	page1, err := svc.ListCheckJobs(ctx, orgA.UID, checkjobs.ListOptions{Limit: 2})
	r.NoError(err)
	r.Len(page1, 2)

	page2, err := svc.ListCheckJobs(ctx, orgA.UID, checkjobs.ListOptions{Limit: 2, Offset: 2})
	r.NoError(err)
	r.Len(page2, 2)
	r.NotEqual(page1[0].UID, page2[0].UID)

	// System (all orgs).
	all, err := svc.ListCheckJobs(ctx, "", checkjobs.ListOptions{Limit: 100})
	r.NoError(err)
	r.Len(all, 8)
}

func TestGetCheckJobOrgScopeMismatch(t *testing.T) {
	t.Parallel()

	ctx, r, dbSvc, svc := newTestService(t)

	orgA := models.NewOrganization("gaorg", "GA")
	orgB := models.NewOrganization("gborg", "GB")
	r.NoError(dbSvc.CreateOrganization(ctx, orgA))
	r.NoError(dbSvc.CreateOrganization(ctx, orgB))

	check := newCheck(ctx, r, dbSvc, orgA.UID, "", "")
	cj := models.NewCheckJob(orgA.UID, check.UID, timeutils.Duration(time.Minute))
	cj.Type = "http"
	r.NoError(dbSvc.CreateCheckJob(ctx, cj))

	// Wrong org: not found.
	_, err := svc.GetCheckJob(ctx, orgB.UID, cj.UID)
	r.ErrorIs(err, checkjobs.ErrCheckJobNotFound)

	// Right org: found.
	got, err := svc.GetCheckJob(ctx, orgA.UID, cj.UID)
	r.NoError(err)
	r.Equal(cj.UID, got.UID)

	// System mode: found regardless of org.
	gotSys, err := svc.GetCheckJob(ctx, "", cj.UID)
	r.NoError(err)
	r.Equal(cj.UID, gotSys.UID)
}

func TestStats(t *testing.T) {
	t.Parallel()

	ctx, r, dbSvc, svc := newTestService(t)

	org := models.NewOrganization("statsorg", "Stats Org")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	// Background jobs in various statuses.
	mkJob := func(status models.JobStatus, updatedAgo time.Duration) {
		job := models.NewJob(&org.UID, "email")
		job.Status = status
		job.UpdatedAt = time.Now().Add(-updatedAgo)
		r.NoError(dbSvc.CreateJob(ctx, job))
	}

	mkJob(models.JobStatusPending, 0)
	mkJob(models.JobStatusPending, 0)
	mkJob(models.JobStatusRunning, 0)
	mkJob(models.JobStatusFailed, time.Hour)    // within 24h
	mkJob(models.JobStatusFailed, 48*time.Hour) // outside 24h, excluded
	mkJob(models.JobStatusSuccess, 0)

	now := time.Now()

	mkCheckJob := func(modify func(cj *models.CheckJob)) {
		check := newCheck(ctx, r, dbSvc, org.UID, "", "")
		cj := models.NewCheckJob(org.UID, check.UID, timeutils.Duration(time.Minute))
		cj.Type = "http"
		modify(cj)
		r.NoError(dbSvc.CreateCheckJob(ctx, cj))
	}

	// due now (scheduled in past, no lease)
	mkCheckJob(func(cj *models.CheckJob) { cj.ScheduledAt = ptrTime(now.Add(-time.Minute)) })
	// in-flight
	mkCheckJob(func(cj *models.CheckJob) {
		cj.ScheduledAt = ptrTime(now.Add(-time.Minute))
		cj.LeaseExpiresAt = ptrTime(now.Add(time.Minute))
	})
	// stalled
	mkCheckJob(func(cj *models.CheckJob) {
		cj.ScheduledAt = ptrTime(now.Add(-time.Minute))
		cj.LeaseExpiresAt = ptrTime(now.Add(-time.Minute))
	})
	// crash-looping
	mkCheckJob(func(cj *models.CheckJob) { cj.LeaseStarts = 12 })

	stats, err := svc.Stats(ctx, org.UID)
	r.NoError(err)

	r.Equal(2, stats.Jobs.Pending)
	r.Equal(1, stats.Jobs.Running)
	r.Equal(1, stats.Jobs.Failed24h)

	r.Equal(4, stats.Checks.Total)
	r.Equal(1, stats.Checks.DueNow)
	r.Equal(1, stats.Checks.InFlight)
	r.Equal(1, stats.Checks.Stalled)
	r.Equal(1, stats.Checks.CrashLooping)
}
