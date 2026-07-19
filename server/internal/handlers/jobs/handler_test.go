package jobs_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/handlers/auth"
	"github.com/fclairamb/solidping/server/internal/handlers/base"
	"github.com/fclairamb/solidping/server/internal/handlers/jobs"
	"github.com/fclairamb/solidping/server/internal/httpx"
	"github.com/fclairamb/solidping/server/internal/jobs/jobsvc"
	"github.com/fclairamb/solidping/server/internal/notifier"
)

// jobsFixture wires the jobs handler to a real jobsvc over an in-memory
// SQLite DB, with two organizations so cross-org isolation is testable.
type jobsFixture struct {
	router *httpx.Router
	jobSvc jobsvc.Service
	dbSvc  db.Service
	orgA   *models.Organization
	orgB   *models.Organization
}

func newJobsFixture(t *testing.T) *jobsFixture {
	t.Helper()

	ctx := t.Context()
	r := require.New(t)

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	orgA := models.NewOrganization("org-a", "Org A")
	r.NoError(dbSvc.CreateOrganization(ctx, orgA))
	orgB := models.NewOrganization("org-b", "Org B")
	r.NoError(dbSvc.CreateOrganization(ctx, orgB))

	jobSvc := jobsvc.NewService(dbSvc.DB(), dbSvc, notifier.NewLocalEventNotifier(), nil)

	router := httpx.New()
	jobs.NewHandler(jobSvc).RegisterRoutes(router.NewGroup("/api/v1"))

	return &jobsFixture{router: router, jobSvc: jobSvc, dbSvc: dbSvc, orgA: orgA, orgB: orgB}
}

// do issues a request with admin claims and the given org resolved in the
// request context, the way the org middleware would.
func (f *jobsFixture) do(
	t *testing.T, org *models.Organization, method, path string,
) *httptest.ResponseRecorder {
	t.Helper()

	ctx := context.WithValue(t.Context(), base.ContextKeyClaims, &auth.Claims{
		UserUID: "test-user", OrgSlug: org.Slug, Role: "admin",
	})
	ctx = context.WithValue(ctx, base.ContextKeyOrganization, org)

	req := httptest.NewRequestWithContext(ctx, method, path, nil)
	rec := httptest.NewRecorder()
	f.router.ServeHTTP(rec, req)

	return rec
}

// createJob enqueues a pending job for the given org UID ("" = global).
func (f *jobsFixture) createJob(t *testing.T, orgUID string) *models.Job {
	t.Helper()

	r := require.New(t)
	job, err := f.jobSvc.CreateJob(t.Context(), orgUID, "sleep", json.RawMessage(`{"seconds":60}`), nil)
	r.NoError(err)

	return job
}

func (f *jobsFixture) jobPath(org *models.Organization, uid string) string {
	return "/api/v1/orgs/" + org.Slug + "/jobs/" + uid
}

// TestGetJobScopedToOrg: a job is readable through its own org's route and
// invisible (404, not 403 — existence must not leak) through another org's.
func TestGetJobScopedToOrg(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	f := newJobsFixture(t)
	job := f.createJob(t, f.orgA.UID)

	rec := f.do(t, f.orgA, http.MethodGet, f.jobPath(f.orgA, job.UID))
	r.Equal(http.StatusOK, rec.Code)

	var body struct {
		Data models.Job `json:"data"`
	}
	r.NoError(json.Unmarshal(rec.Body.Bytes(), &body))
	r.Equal(job.UID, body.Data.UID)

	// Same UID through org B's route: not found.
	rec = f.do(t, f.orgB, http.MethodGet, f.jobPath(f.orgB, job.UID))
	r.Equal(http.StatusNotFound, rec.Code)
}

// TestGetGlobalJobNotExposedOnOrgRoute: global jobs (nil org — reaper,
// startup, …) are not reachable through any org-scoped route.
func TestGetGlobalJobNotExposedOnOrgRoute(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	f := newJobsFixture(t)
	global := f.createJob(t, "")

	rec := f.do(t, f.orgA, http.MethodGet, f.jobPath(f.orgA, global.UID))
	r.Equal(http.StatusNotFound, rec.Code)
}

// TestCancelJobScopedToOrg: canceling through another org's route 404s AND
// leaves the job live; canceling through the owning org soft-deletes it.
func TestCancelJobScopedToOrg(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	f := newJobsFixture(t)
	ctx := t.Context()
	job := f.createJob(t, f.orgA.UID)

	// Cross-org cancel: 404, and the row must remain untouched.
	rec := f.do(t, f.orgB, http.MethodDelete, f.jobPath(f.orgB, job.UID))
	r.Equal(http.StatusNotFound, rec.Code)

	var row models.Job
	r.NoError(f.dbSvc.DB().NewSelect().Model(&row).Where("uid = ?", job.UID).Scan(ctx))
	r.Nil(row.DeletedAt, "a cross-org cancel attempt must not touch the job")

	// Owning org cancels fine.
	rec = f.do(t, f.orgA, http.MethodDelete, f.jobPath(f.orgA, job.UID))
	r.Equal(http.StatusNoContent, rec.Code)

	r.NoError(f.dbSvc.DB().NewSelect().Model(&row).Where("uid = ?", job.UID).Scan(ctx))
	r.NotNil(row.DeletedAt, "the owning org's cancel soft-deletes the job")
}

// TestCancelGlobalJobNotExposedOnOrgRoute: global jobs cannot be canceled
// through an org-scoped route.
func TestCancelGlobalJobNotExposedOnOrgRoute(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	f := newJobsFixture(t)
	ctx := t.Context()
	global := f.createJob(t, "")

	rec := f.do(t, f.orgA, http.MethodDelete, f.jobPath(f.orgA, global.UID))
	r.Equal(http.StatusNotFound, rec.Code)

	var row models.Job
	r.NoError(f.dbSvc.DB().NewSelect().Model(&row).Where("uid = ?", global.UID).Scan(ctx))
	r.Nil(row.DeletedAt, "a global job must survive an org-scoped cancel attempt")
}

// TestListJobsScopedToOrg guards the earlier slug-vs-UID fix: the list is
// resolved through the org middleware and only returns the org's own jobs.
func TestListJobsScopedToOrg(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	f := newJobsFixture(t)
	mine := f.createJob(t, f.orgA.UID)
	f.createJob(t, f.orgB.UID)

	rec := f.do(t, f.orgA, http.MethodGet, "/api/v1/orgs/"+f.orgA.Slug+"/jobs")
	r.Equal(http.StatusOK, rec.Code)

	var body struct {
		Data []models.Job `json:"data"`
	}
	r.NoError(json.Unmarshal(rec.Body.Bytes(), &body))
	r.Len(body.Data, 1, "only org A's jobs are listed")
	r.Equal(mine.UID, body.Data[0].UID)
}
