package app

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
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

const membersRouteTestPassword = "members-route-test-pass"

// membersRouteEnv is a fully wired server with one org holding one member per
// role, so the admin gate on the write routes can be probed from every side
// over real HTTP (spec 2026-08-09-03: the gate must run through the
// registered router, not just the service layer — a handler-only test would
// pass even with the middleware missing).
type membersRouteEnv struct {
	t       *testing.T
	ts      *httptest.Server
	client  *http.Client
	orgSlug string
	orgUID  string
	server  *Server
	jwts    map[models.MemberRole]string
	uids    map[models.MemberRole]string
	target  string // uid of the plain "user" member used as a PATCH/DELETE target
}

func newMembersRouteEnv(t *testing.T) *membersRouteEnv {
	t.Helper()
	r := require.New(t)
	ctx := context.Background()

	cfg := &config.Config{}
	cfg.Database.Type = dbTypeSQLiteMemory
	cfg.Auth.JWTSecret = "members-write-routes-secret"
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

	org := models.NewOrganization("members-route-org", "Members Route Org")
	r.NoError(server.dbService.CreateOrganization(ctx, org))

	env := &membersRouteEnv{
		t: t, ts: ts, orgSlug: org.Slug, orgUID: org.UID, server: server,
		client: &http.Client{
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
		jwts: make(map[models.MemberRole]string),
		uids: make(map[models.MemberRole]string),
	}

	hash, err := passwords.Hash(membersRouteTestPassword)
	r.NoError(err)
	now := time.Now()

	roleEmails := map[models.MemberRole]string{
		models.MemberRoleOwner:  "owner@members-route.example",
		models.MemberRoleAdmin:  "admin@members-route.example",
		models.MemberRoleUser:   "user@members-route.example",
		models.MemberRoleViewer: "viewer@members-route.example",
	}

	for role, email := range roleEmails {
		user := models.NewUser(email)
		user.PasswordHash = &hash
		user.EmailVerifiedAt = &now
		r.NoError(server.dbService.CreateUser(ctx, user))

		member := models.NewOrganizationMember(org.UID, user.UID, role)
		member.JoinedAt = &now
		r.NoError(server.dbService.CreateOrganizationMember(ctx, member))

		env.uids[role] = member.UID
		env.jwts[role] = env.login(email)
	}

	// The PATCH/DELETE target must be a member distinct from every caller
	// identity above: RequireOrgAdmin re-reads the LIVE membership row (not
	// the JWT claim), so promoting the "user" caller's own row via the
	// PATCH positive control would let their still-valid JWT pass the gate
	// on the next call — a test-setup bug, not a product one.
	targetUser := models.NewUser("target@members-route.example")
	targetUser.PasswordHash = &hash
	targetUser.EmailVerifiedAt = &now
	r.NoError(server.dbService.CreateUser(ctx, targetUser))

	targetMember := models.NewOrganizationMember(org.UID, targetUser.UID, models.MemberRoleUser)
	targetMember.JoinedAt = &now
	r.NoError(server.dbService.CreateOrganizationMember(ctx, targetMember))
	env.target = targetMember.UID

	return env
}

func (e *membersRouteEnv) login(email string) string {
	e.t.Helper()
	r := require.New(e.t)

	body, err := json.Marshal(map[string]string{
		"org": e.orgSlug, "email": email, "password": membersRouteTestPassword,
	})
	r.NoError(err)

	resp := e.do(http.MethodPost, "/api/v1/auth/login", "", "application/json", bytes.NewReader(body))
	defer func() { _ = resp.Body.Close() }()

	r.Equal(http.StatusOK, resp.StatusCode)

	var payload struct {
		AccessToken string `json:"accessToken"`
	}

	r.NoError(json.NewDecoder(resp.Body).Decode(&payload))
	r.NotEmpty(payload.AccessToken)

	return payload.AccessToken
}

func (e *membersRouteEnv) do(method, path, token, contentType string, body io.Reader) *http.Response {
	e.t.Helper()

	req, err := http.NewRequestWithContext(context.Background(), method, e.ts.URL+path, body)
	require.NoError(e.t, err)

	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}

	resp, err := e.client.Do(req)
	require.NoError(e.t, err)

	return resp
}

// TestMembersWriteRoutesAreAdminOnly walks the caller-role x verb matrix from
// the spec: viewer/user are refused with a standard 403 FORBIDDEN body on
// every write verb, admin/owner succeed, and GET stays open to everyone.
func TestMembersWriteRoutesAreAdminOnly(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	env := newMembersRouteEnv(t)
	membersPath := "/api/v1/orgs/" + env.orgSlug + "/members"

	// --- GET stays open to every role, including viewer. ---
	for role, token := range env.jwts {
		resp := env.do(http.MethodGet, membersPath, token, "", nil)
		r.Equal(http.StatusOK, resp.StatusCode, "GET must stay open for role %s", role)
		_ = resp.Body.Close()
	}

	// --- POST (add a new member): viewer/user refused, admin succeeds. ---
	newcomerEmail := func(role models.MemberRole) string {
		return "newcomer-" + string(role) + "@members-route.example"
	}

	for _, role := range []models.MemberRole{models.MemberRoleViewer, models.MemberRoleUser} {
		body, err := json.Marshal(map[string]string{"email": newcomerEmail(role), "role": "user"})
		r.NoError(err)

		resp := env.do(http.MethodPost, membersPath, env.jwts[role], "application/json", bytes.NewReader(body))
		defer func() { _ = resp.Body.Close() }()
		r.Equal(http.StatusForbidden, resp.StatusCode, "POST must be refused for role %s", role)
		r.Equal(base.ErrorCodeForbidden, decodeErrorCode(t, resp))

		// The refused add must not have landed.
		_, getErr := env.server.dbService.GetUserByEmail(context.Background(), newcomerEmail(role))
		r.Error(getErr, "the refused newcomer must not exist")
	}

	// AddMember only attaches an *existing* user to the org, so the positive
	// controls below need a real user row first (unlike the refused attempts
	// above, which never reach the handler).
	registerUser := func(email string) {
		newUser := models.NewUser(email)
		r.NoError(env.server.dbService.CreateUser(context.Background(), newUser))
	}

	// Positive control: the same POST as an admin succeeds.
	registerUser(newcomerEmail(models.MemberRoleAdmin))
	adminBody, err := json.Marshal(map[string]string{"email": newcomerEmail(models.MemberRoleAdmin), "role": "user"})
	r.NoError(err)
	adminResp := env.do(http.MethodPost, membersPath, env.jwts[models.MemberRoleAdmin],
		"application/json", bytes.NewReader(adminBody))
	defer func() { _ = adminResp.Body.Close() }()
	r.Equal(http.StatusCreated, adminResp.StatusCode)

	// Owner must also pass the admin gate (hierarchical, not equality).
	registerUser(newcomerEmail(models.MemberRoleOwner))
	ownerBody, err := json.Marshal(map[string]string{"email": newcomerEmail(models.MemberRoleOwner), "role": "user"})
	r.NoError(err)
	ownerResp := env.do(http.MethodPost, membersPath, env.jwts[models.MemberRoleOwner],
		"application/json", bytes.NewReader(ownerBody))
	defer func() { _ = ownerResp.Body.Close() }()
	r.Equal(http.StatusCreated, ownerResp.StatusCode)

	// --- PATCH (update a member's role): viewer/user refused; DB row unchanged. ---
	patchPath := membersPath + "/" + env.target

	for _, role := range []models.MemberRole{models.MemberRoleViewer, models.MemberRoleUser} {
		body, marshalErr := json.Marshal(map[string]string{"role": "admin"})
		r.NoError(marshalErr)

		resp := env.do(http.MethodPatch, patchPath, env.jwts[role], "application/json", bytes.NewReader(body))
		defer func() { _ = resp.Body.Close() }()
		r.Equal(http.StatusForbidden, resp.StatusCode, "PATCH must be refused for role %s", role)
		r.Equal(base.ErrorCodeForbidden, decodeErrorCode(t, resp))

		// Negative control: the target's role must be untouched.
		unchanged, getErr := env.server.dbService.GetOrganizationMember(context.Background(), env.target)
		r.NoError(getErr)
		r.Equal(models.MemberRoleUser, unchanged.Role, "a refused PATCH must not mutate the row")
	}

	// Positive control: the same PATCH as an admin succeeds and changes the row.
	patchBody, err := json.Marshal(map[string]string{"role": "admin"})
	r.NoError(err)
	patchResp := env.do(http.MethodPatch, patchPath, env.jwts[models.MemberRoleAdmin],
		"application/json", bytes.NewReader(patchBody))
	defer func() { _ = patchResp.Body.Close() }()
	r.Equal(http.StatusOK, patchResp.StatusCode)

	changed, getErr := env.server.dbService.GetOrganizationMember(context.Background(), env.target)
	r.NoError(getErr)
	r.Equal(models.MemberRoleAdmin, changed.Role, "the admin's PATCH must have landed")

	// --- DELETE: viewer/user refused, member still present; admin succeeds. ---
	// env.target is now "admin" role after the PATCH above — still a fine
	// removal target since we only assert presence/absence, not role.
	for _, role := range []models.MemberRole{models.MemberRoleViewer, models.MemberRoleUser} {
		resp := env.do(http.MethodDelete, patchPath, env.jwts[role], "", nil)
		defer func() { _ = resp.Body.Close() }()
		r.Equal(http.StatusForbidden, resp.StatusCode, "DELETE must be refused for role %s", role)
		r.Equal(base.ErrorCodeForbidden, decodeErrorCode(t, resp))

		still, getErr := env.server.dbService.GetOrganizationMember(context.Background(), env.target)
		r.NoError(getErr)
		r.Nil(still.DeletedAt, "a refused DELETE must not remove the member")
	}

	deleteResp := env.do(http.MethodDelete, patchPath, env.jwts[models.MemberRoleAdmin], "", nil)
	defer func() { _ = deleteResp.Body.Close() }()
	r.Equal(http.StatusNoContent, deleteResp.StatusCode)
}

// decodeErrorCode reads the standard {title, code, detail} error body's code
// field.
func decodeErrorCode(t *testing.T, resp *http.Response) base.ErrorCode {
	t.Helper()

	var payload struct {
		Title  string         `json:"title"`
		Code   base.ErrorCode `json:"code"`
		Detail string         `json:"detail"`
	}

	require.NoError(t, json.NewDecoder(resp.Body).Decode(&payload))
	require.NotEmpty(t, payload.Title, "the 403 body must carry the standard error shape")

	return payload.Code
}
