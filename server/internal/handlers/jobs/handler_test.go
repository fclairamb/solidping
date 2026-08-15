package jobs_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/handlers/auth"
	"github.com/fclairamb/solidping/server/internal/handlers/jobs"
	"github.com/fclairamb/solidping/server/internal/httpx"
	"github.com/fclairamb/solidping/server/internal/jobs/jobdef"
	"github.com/fclairamb/solidping/server/internal/jobs/jobsvc"
	"github.com/fclairamb/solidping/server/internal/jobs/jobtypes"
	mw "github.com/fclairamb/solidping/server/internal/middleware"
	"github.com/fclairamb/solidping/server/internal/notifier"
)

// jobsFixture wires the jobs handler to a real jobsvc over an in-memory SQLite
// DB, behind the PRODUCTION middleware chain: routes are registered through
// jobs.Handler.RegisterRoutes with the same RouteMiddleware app/server.go
// supplies, and callers authenticate with real JWTs. That is deliberate — a
// bare-handler test would pass even with the admin gate missing entirely, which
// is exactly the regression this file exists to catch (spec 2026-08-15-04).
//
// Two organizations exist so cross-org isolation stays testable; the admin is a
// member of both, the viewer only of org A.
type jobsFixture struct {
	router  *httpx.Router
	jobSvc  jobsvc.Service
	dbSvc   db.Service
	orgA    *models.Organization
	orgB    *models.Organization
	authSvc *auth.Service
	admin   *models.User
	viewer  *models.User
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

	cfg := &config.Config{}
	cfg.Auth.JWTSecret = "jobs-handler-test-secret"
	cfg.Auth.AccessTokenExpiry = time.Hour
	cfg.Auth.RefreshTokenExpiry = 24 * time.Hour

	authSvc := auth.NewService(dbSvc, cfg.Auth, cfg, jobSvc, nil)
	authMW := mw.NewAuthMiddleware(authSvc, dbSvc, cfg)

	fixture := &jobsFixture{
		router: httpx.New(), jobSvc: jobSvc, dbSvc: dbSvc,
		orgA: orgA, orgB: orgB, authSvc: authSvc,
	}

	fixture.admin = fixture.newMember(t, "admin@jobs.example", models.MemberRoleAdmin, orgA, orgB)
	fixture.viewer = fixture.newMember(t, "viewer@jobs.example", models.MemberRoleViewer, orgA)

	// The exact wiring app/server.go uses (minus the slug-alias redirect, which
	// carries no authorization meaning): membership for every route, org admin
	// on top for POST.
	jobs.NewHandler(jobSvc).RegisterRoutes(fixture.router.NewGroup("/api/v1"), jobs.RouteMiddleware{
		Shared:     []httpx.Middleware{authMW.RequireAuth, authMW.RequireOrgAccess},
		CreateOnly: []httpx.Middleware{authMW.RequireOrgAdmin},
	})

	return fixture
}

// newMember creates a user and attaches it to each given org with the role.
func (f *jobsFixture) newMember(
	t *testing.T, email string, role models.MemberRole, orgs ...*models.Organization,
) *models.User {
	t.Helper()

	r := require.New(t)
	ctx := t.Context()
	now := time.Now()

	user := models.NewUser(email)
	user.EmailVerifiedAt = &now
	r.NoError(f.dbSvc.CreateUser(ctx, user))

	for _, org := range orgs {
		member := models.NewOrganizationMember(org.UID, user.UID, role)
		member.JoinedAt = &now
		r.NoError(f.dbSvc.CreateOrganizationMember(ctx, member))
	}

	return user
}

// token mints a real access token for the user, scoped to the given org (org
// scoping is part of the production chain: a token minted for org A is refused
// on org B's routes).
func (f *jobsFixture) token(t *testing.T, user *models.User, org *models.Organization, role string) string {
	t.Helper()

	tokens, err := f.authSvc.GenerateTokensForOAuth(t.Context(), user, org, role)
	require.NoError(t, err)

	return tokens.AccessToken
}

func (f *jobsFixture) adminToken(t *testing.T, org *models.Organization) string {
	t.Helper()

	return f.token(t, f.admin, org, string(models.MemberRoleAdmin))
}

func (f *jobsFixture) viewerToken(t *testing.T, org *models.Organization) string {
	t.Helper()

	return f.token(t, f.viewer, org, string(models.MemberRoleViewer))
}

// request issues an authenticated HTTP request through the full router.
func (f *jobsFixture) request(
	t *testing.T, token, method, path string, body io.Reader,
) *httptest.ResponseRecorder {
	t.Helper()

	req := httptest.NewRequestWithContext(t.Context(), method, path, body)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	rec := httptest.NewRecorder()
	f.router.ServeHTTP(rec, req)

	return rec
}

// do issues a request as the admin of the given org.
func (f *jobsFixture) do(
	t *testing.T, org *models.Organization, method, path string,
) *httptest.ResponseRecorder {
	t.Helper()

	return f.request(t, f.adminToken(t, org), method, path, nil)
}

// post sends a job-creation request with the given raw JSON payload.
func (f *jobsFixture) post(
	t *testing.T, token string, org *models.Organization, payload string,
) *httptest.ResponseRecorder {
	t.Helper()

	return f.request(t, token, http.MethodPost, "/api/v1/orgs/"+org.Slug+"/jobs", strings.NewReader(payload))
}

// createJob enqueues a pending job for the given org UID ("" = global).
func (f *jobsFixture) createJob(t *testing.T, orgUID string) *models.Job {
	t.Helper()

	r := require.New(t)
	job, err := f.jobSvc.CreateJob(t.Context(), orgUID, "sleep", json.RawMessage(`{"seconds":60}`), nil)
	r.NoError(err)

	return job
}

// jobRowCount counts every job row in the DB, soft-deleted ones included — the
// ground truth for "nothing was enqueued", independent of the HTTP status.
func (f *jobsFixture) jobRowCount(t *testing.T) int {
	t.Helper()

	count, err := f.dbSvc.DB().NewSelect().Model((*models.Job)(nil)).Count(t.Context())
	require.NoError(t, err)

	return count
}

func (f *jobsFixture) jobPath(org *models.Organization, uid string) string {
	return "/api/v1/orgs/" + org.Slug + "/jobs/" + uid
}

// errorCode digs the machine-readable code out of a response body. Middleware
// denials use the flat {title, code, detail} shape while the jobs handler nests
// it under "error"; both are accepted so the assertions state what they mean
// rather than which layer answered.
func errorCode(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()

	var payload struct {
		Code  string `json:"code"`
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}

	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))

	if payload.Code != "" {
		return payload.Code
	}

	return payload.Error.Code
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

// TestCreateJobBlockedTypesAsAdmin is the core negative: even an org ADMIN —
// past the role gate — cannot enqueue a non-allowlisted job type, and nothing
// is written. The email payload is the phishing primitive (raw HTML through the
// deployment's own sender) and the webhook payload the SSRF one (link-local
// metadata endpoint); both must be refused by the allowlist, not by luck.
func TestCreateJobBlockedTypesAsAdmin(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		payload string
	}{
		{
			name: "email with raw html",
			payload: `{"type":"email","config":{"to":["victim@example.com"],` +
				`"subject":"Password reset","html":"<a href='http://evil'>click</a>"}}`,
		},
		{
			name:    "webhook to the cloud metadata endpoint",
			payload: `{"type":"webhook","config":{"url":"http://169.254.169.254/","method":"GET"}}`,
		},
		{
			name:    "notification",
			payload: `{"type":"notification","config":{}}`,
		},
		{
			name:    "network discovery",
			payload: `{"type":"network_discovery","config":{"ranges":["10.0.0.0/8"]}}`,
		},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			r := require.New(t)
			f := newJobsFixture(t)

			rec := f.post(t, f.adminToken(t, f.orgA), f.orgA, testCase.payload)
			r.Equal(http.StatusForbidden, rec.Code, "body: %s", rec.Body.String())
			r.Equal("FORBIDDEN", errorCode(t, rec))
			r.Equal(0, f.jobRowCount(t), "a refused create must not enqueue anything")
		})
	}
}

// TestCreateJobRefusedForViewer proves the role gate rejects independently of
// the allowlist: a viewer is refused BOTH a blocked type and the allowlisted
// one, and neither leaves a row.
func TestCreateJobRefusedForViewer(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		payload string
	}{
		{name: "blocked type", payload: `{"type":"email","config":{"to":["v@example.com"],` +
			`"subject":"hi","html":"<p>hi</p>"}}`},
		{name: "allowlisted type", payload: `{"type":"sleep","config":{"seconds":1}}`},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			r := require.New(t)
			f := newJobsFixture(t)

			rec := f.post(t, f.viewerToken(t, f.orgA), f.orgA, testCase.payload)
			r.Equal(http.StatusForbidden, rec.Code, "body: %s", rec.Body.String())
			r.Equal("FORBIDDEN", errorCode(t, rec))
			r.Equal(0, f.jobRowCount(t), "a viewer must never enqueue a job")
		})
	}
}

// TestCreateJobUnauthenticated: no token, no job — the route is not open.
func TestCreateJobUnauthenticated(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	f := newJobsFixture(t)

	rec := f.post(t, "", f.orgA, `{"type":"sleep","config":{"seconds":1}}`)
	r.Equal(http.StatusUnauthorized, rec.Code)
	r.Equal(0, f.jobRowCount(t))
}

// TestCreateJobSleepAsAdmin is the positive control: without it the whole
// negative suite above would still pass with CreateJob simply broken.
func TestCreateJobSleepAsAdmin(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	f := newJobsFixture(t)

	rec := f.post(t, f.adminToken(t, f.orgA), f.orgA, `{"type":"sleep","config":{"seconds":1}}`)
	r.Equal(http.StatusCreated, rec.Code, "body: %s", rec.Body.String())

	var body struct {
		Data models.Job `json:"data"`
	}
	r.NoError(json.Unmarshal(rec.Body.Bytes(), &body))
	r.Equal(string(jobdef.JobTypeSleep), body.Data.Type)

	r.Equal(1, f.jobRowCount(t), "the allowlisted create must really enqueue")

	var row models.Job
	r.NoError(f.dbSvc.DB().NewSelect().Model(&row).Where("uid = ?", body.Data.UID).Scan(t.Context()))
	r.NotNil(row.OrganizationUID)
	r.Equal(f.orgA.UID, *row.OrganizationUID)
}

// TestCreateJobClientErrorsAreNot500 pins the status mapping: a bad request is
// the caller's fault (4xx), never a masked INTERNAL_ERROR.
func TestCreateJobClientErrorsAreNot500(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		payload    string
		wantStatus int
		wantCode   string
	}{
		{
			name:       "unknown job type",
			payload:    `{"type":"nonexistent","config":{}}`,
			wantStatus: http.StatusBadRequest,
			wantCode:   "VALIDATION_ERROR",
		},
		{
			name:       "allowlisted type with a malformed config",
			payload:    `{"type":"sleep","config":{"seconds":"not-a-number"}}`,
			wantStatus: http.StatusBadRequest,
			wantCode:   "VALIDATION_ERROR",
		},
		{
			name:       "allowlisted type with a non-object config",
			payload:    `{"type":"sleep","config":["nope"]}`,
			wantStatus: http.StatusBadRequest,
			wantCode:   "VALIDATION_ERROR",
		},
		{
			name:       "unparseable body",
			payload:    `{"type":`,
			wantStatus: http.StatusBadRequest,
			wantCode:   "VALIDATION_ERROR",
		},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			r := require.New(t)
			f := newJobsFixture(t)

			rec := f.post(t, f.adminToken(t, f.orgA), f.orgA, testCase.payload)
			r.Equal(testCase.wantStatus, rec.Code, "body: %s", rec.Body.String())
			r.Less(rec.Code, http.StatusInternalServerError, "a client error must never surface as a 5xx")
			r.Equal(testCase.wantCode, errorCode(t, rec))
			r.Equal(0, f.jobRowCount(t))
		})
	}
}

// TestInternalRawContentEmailStillEnqueues is the boundary assertion for
// proposal §5: the gate is HTTP-only. Raw-content email — refused over the API
// by TestCreateJobBlockedTypesAsAdmin above — must keep working for in-process
// callers (auth's transactional mail, testapi, the schedulers), both as a
// queued row and as a validated runner.
func TestInternalRawContentEmailStillEnqueues(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	f := newJobsFixture(t)

	rawContent := json.RawMessage(
		`{"to":["ops@example.com"],"subject":"Internal notice","html":"<p>raw</p>","text":"raw"}`,
	)

	job, err := f.jobSvc.CreateJob(t.Context(), f.orgA.UID, string(jobdef.JobTypeEmail), rawContent, nil)
	r.NoError(err, "the allowlist must not reach the service layer")
	r.NotEmpty(job.UID)
	r.Equal(1, f.jobRowCount(t))

	definition, ok := jobtypes.GetJobDefinition(jobdef.JobTypeEmail)
	r.True(ok)

	runner, err := definition.CreateJobRun(rawContent)
	r.NoError(err, "raw-content email must still validate internally")
	r.NotNil(runner)
}
