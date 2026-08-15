package app

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/handlers/base"
	"github.com/fclairamb/solidping/server/internal/utils/passwords"
)

const jobsRouteTestPassword = "jobs-route-test-pass"

// jobsRouteEnv is a fully wired server used to prove the guards on
// POST /api/v1/orgs/:org/jobs are what production actually registers — not what
// a hand-built router in a handler test says. The handler test covers the same
// rules through the same RegisterRoutes call; this one covers the argument
// server.go passes to it, which is the half that could silently drift
// (spec 2026-08-15-04).
type jobsRouteEnv struct {
	t       *testing.T
	ts      *httptest.Server
	client  *http.Client
	orgSlug string
	server  *Server
	jwts    map[models.MemberRole]string
}

func newJobsRouteEnv(t *testing.T) *jobsRouteEnv {
	t.Helper()
	r := require.New(t)
	ctx := context.Background()

	cfg := &config.Config{}
	cfg.Database.Type = dbTypeSQLiteMemory
	cfg.Auth.JWTSecret = "jobs-create-routes-secret"
	cfg.Auth.AccessTokenExpiry = time.Hour
	cfg.Auth.RefreshTokenExpiry = 24 * time.Hour
	cfg.FileStorage.Type = "local"
	cfg.FileStorage.LocalRoot = t.TempDir()

	server, err := NewServer(ctx, cfg)
	r.NoError(err)
	t.Cleanup(func() { _ = server.dbService.Close() })

	r.NoError(server.Initialize(ctx))
	r.NoError(server.InitializeSystemConfig(ctx, cfg))
	server.SetupRoutes(ctx)

	ts := httptest.NewServer(server.Handler())
	t.Cleanup(ts.Close)

	org := models.NewOrganization("jobs-route-org", "Jobs Route Org")
	r.NoError(server.dbService.CreateOrganization(ctx, org))

	env := &jobsRouteEnv{
		t: t, ts: ts, orgSlug: org.Slug, server: server,
		client: &http.Client{
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
		jwts: make(map[models.MemberRole]string),
	}

	hash, err := passwords.Hash(jobsRouteTestPassword)
	r.NoError(err)
	now := time.Now()

	for role, email := range map[models.MemberRole]string{
		models.MemberRoleAdmin:  "admin@jobs-route.example",
		models.MemberRoleViewer: "viewer@jobs-route.example",
	} {
		user := models.NewUser(email)
		user.PasswordHash = &hash
		user.EmailVerifiedAt = &now
		r.NoError(server.dbService.CreateUser(ctx, user))

		member := models.NewOrganizationMember(org.UID, user.UID, role)
		member.JoinedAt = &now
		r.NoError(server.dbService.CreateOrganizationMember(ctx, member))

		env.jwts[role] = env.login(email)
	}

	return env
}

func (e *jobsRouteEnv) login(email string) string {
	e.t.Helper()
	r := require.New(e.t)

	body, err := json.Marshal(map[string]string{
		"org": e.orgSlug, "email": email, "password": jobsRouteTestPassword,
	})
	r.NoError(err)

	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost,
		e.ts.URL+"/api/v1/auth/login", bytes.NewReader(body))
	r.NoError(err)
	req.Header.Set("Content-Type", "application/json")

	resp, err := e.client.Do(req)
	r.NoError(err)

	defer func() { _ = resp.Body.Close() }()

	r.Equal(http.StatusOK, resp.StatusCode)

	var payload struct {
		AccessToken string `json:"accessToken"`
	}

	r.NoError(json.NewDecoder(resp.Body).Decode(&payload))
	r.NotEmpty(payload.AccessToken)

	return payload.AccessToken
}

// post sends a job-creation request as the given role.
func (e *jobsRouteEnv) post(role models.MemberRole, payload string) *http.Response {
	e.t.Helper()

	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost,
		e.ts.URL+"/api/v1/orgs/"+e.orgSlug+"/jobs", bytes.NewReader([]byte(payload)))
	require.NoError(e.t, err)
	req.Header.Set("Authorization", "Bearer "+e.jwts[role])
	req.Header.Set("Content-Type", "application/json")

	resp, err := e.client.Do(req)
	require.NoError(e.t, err)

	return resp
}

// countJobs counts the queued jobs of a given type (the server enqueues its own
// startup/global jobs, so an absolute row count would not be meaningful).
func (e *jobsRouteEnv) countJobs(jobType string) int {
	e.t.Helper()

	count, err := e.server.dbService.DB().NewSelect().
		Model((*models.Job)(nil)).Where("type = ?", jobType).Count(context.Background())
	require.NoError(e.t, err)

	return count
}

// TestCreateJobRouteIsAdminAndAllowlisted walks the production route: a viewer
// cannot create anything, an admin cannot create a non-allowlisted type, and
// neither refusal leaves a row — while the allowlisted type still works.
func TestCreateJobRouteIsAdminAndAllowlisted(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	env := newJobsRouteEnv(t)

	emailPayload := `{"type":"email","config":{"to":["victim@example.com"],` +
		`"subject":"hi","html":"<p>phish</p>"}}`
	sleepPayload := `{"type":"sleep","config":{"seconds":1}}`
	webhookPayload := `{"type":"webhook","config":{"url":"http://169.254.169.254/","method":"GET"}}`

	// --- Viewer: refused on every payload, allowlisted or not. ---
	for _, payload := range []string{emailPayload, sleepPayload, webhookPayload} {
		resp := env.post(models.MemberRoleViewer, payload)
		defer func() { _ = resp.Body.Close() }()
		r.Equal(http.StatusForbidden, resp.StatusCode, "a viewer must never create a job")
		r.Equal(base.ErrorCodeForbidden, decodeErrorCode(t, resp))
	}

	// --- Admin: refused on the abusable types. ---
	for _, payload := range []string{emailPayload, webhookPayload} {
		resp := env.post(models.MemberRoleAdmin, payload)
		defer func() { _ = resp.Body.Close() }()
		r.Equal(http.StatusForbidden, resp.StatusCode, "an admin must not bypass the allowlist")
	}

	r.Equal(0, env.countJobs("email"), "no email job may have been enqueued")
	r.Equal(0, env.countJobs("webhook"), "no webhook job may have been enqueued")

	// --- Positive control: the allowlisted type still works for an admin. ---
	before := env.countJobs("sleep")
	resp := env.post(models.MemberRoleAdmin, sleepPayload)
	defer func() { _ = resp.Body.Close() }()
	r.Equal(http.StatusCreated, resp.StatusCode)
	r.Equal(before+1, env.countJobs("sleep"))
}
