package app

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/audit"
	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/handlers/auth"
	"github.com/fclairamb/solidping/server/internal/handlers/base"
)

// impersonationEnv is the real server (NewServer + SetupRoutes over in-memory
// SQLite) with two orgs, a super admin, a member of org A only, and one check
// in each org.
type impersonationEnv struct {
	t           *testing.T
	server      *Server
	ts          *httptest.Server
	orgA, orgB  *models.Organization
	admin       *models.User
	member      *models.User
	adminToken  string
	memberToken string
}

func newImpersonationEnv(t *testing.T, enabled bool) *impersonationEnv {
	t.Helper()
	r := require.New(t)
	ctx := context.Background()

	cfg := &config.Config{}
	cfg.Database.Type = dbTypeSQLiteMemory
	cfg.Auth.JWTSecret = "impersonation-secret"
	cfg.Auth.AccessTokenExpiry = time.Hour
	cfg.Auth.RefreshTokenExpiry = 24 * time.Hour
	cfg.Auth.ImpersonationEnabled = enabled

	server, err := NewServer(ctx, cfg)
	r.NoError(err)
	t.Cleanup(func() { _ = server.dbService.Close() })

	r.NoError(server.Initialize(ctx))
	r.NoError(server.InitializeSystemConfig(ctx, cfg))
	server.SetupRoutes(ctx)

	ts := httptest.NewServer(server.Handler())
	t.Cleanup(ts.Close)

	env := &impersonationEnv{t: t, server: server, ts: ts}

	env.orgA = models.NewOrganization("impa", "Acme A")
	r.NoError(server.dbService.CreateOrganization(ctx, env.orgA))
	env.orgB = models.NewOrganization("impb", "Acme B")
	r.NoError(server.dbService.CreateOrganization(ctx, env.orgB))

	r.NoError(server.dbService.CreateCheck(ctx, models.NewCheck(env.orgA.UID, "a-check", "http")))
	r.NoError(server.dbService.CreateCheck(ctx, models.NewCheck(env.orgB.UID, "b-check", "http")))

	now := time.Now()

	env.admin = models.NewUser("root@acme.com")
	env.admin.SuperAdmin = true
	r.NoError(server.dbService.CreateUser(ctx, env.admin))

	env.member = models.NewUser("alice@acme.com")
	r.NoError(server.dbService.CreateUser(ctx, env.member))

	member := models.NewOrganizationMember(env.orgA.UID, env.member.UID, models.MemberRoleAdmin)
	member.JoinedAt = &now
	r.NoError(server.dbService.CreateOrganizationMember(ctx, member))

	env.adminToken = mintTestToken(t, server, env.admin.UID, env.orgA.Slug, auth.RoleSuperAdmin, false)
	env.memberToken = mintTestToken(t, server, env.member.UID, env.orgA.Slug, string(models.MemberRoleAdmin), false)

	return env
}

func (e *impersonationEnv) do(method, path, token, body string) (int, map[string]any) {
	e.t.Helper()

	req, err := http.NewRequestWithContext(context.Background(), method, e.ts.URL+path, strings.NewReader(body))
	require.NoError(e.t, err)

	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	req.Header.Set("Content-Type", "application/json")

	resp, err := e.ts.Client().Do(req)
	require.NoError(e.t, err)

	defer func() { _ = resp.Body.Close() }()

	raw, err := io.ReadAll(resp.Body)
	require.NoError(e.t, err)

	out := map[string]any{}
	_ = json.Unmarshal(raw, &out)

	return resp.StatusCode, out
}

func (e *impersonationEnv) impersonatePath() string {
	return "/api/v1/system/users/" + e.member.UID + "/impersonate"
}

// TestImpersonateEndpoint drives POST /api/v1/system/users/:uid/impersonate
// through the real route table.
func TestImpersonateEndpoint(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	env := newImpersonationEnv(t, true)

	// Unauthenticated: 401.
	status, _ := env.do(http.MethodPost, env.impersonatePath(), "", `{}`)
	r.Equal(http.StatusUnauthorized, status)

	// A non-super-admin: 403.
	status, _ = env.do(http.MethodPost, env.impersonatePath(), env.memberToken, `{}`)
	r.Equal(http.StatusForbidden, status)

	// Self: refused.
	status, body := env.do(http.MethodPost,
		"/api/v1/system/users/"+env.admin.UID+"/impersonate", env.adminToken, `{}`)
	r.Equal(http.StatusForbidden, status)
	r.Equal(string(base.ErrorCodeImpersonationForbidden), body["code"])

	// Not a member of org B: 404.
	status, _ = env.do(http.MethodPost, env.impersonatePath(), env.adminToken, `{"orgSlug":"impb"}`)
	r.Equal(http.StatusNotFound, status)

	// Unknown user: 404.
	status, _ = env.do(http.MethodPost,
		"/api/v1/system/users/00000000-0000-0000-0000-000000000000/impersonate", env.adminToken, `{}`)
	r.Equal(http.StatusNotFound, status)

	// Success: an access token and no refresh token, no cookie.
	status, body = env.do(http.MethodPost, env.impersonatePath(), env.adminToken, `{"orgSlug":"impa"}`)
	r.Equal(http.StatusOK, status)
	r.NotEmpty(body["accessToken"])
	r.NotContains(body, "refreshToken")
	r.InDelta(float64(auth.ImpersonationTTL.Seconds()), body["expiresIn"], 0)

	token, ok := body["accessToken"].(string)
	r.True(ok)

	// The token reads the target's org...
	status, body = env.do(http.MethodGet, "/api/v1/orgs/impa/checks", token, "")
	r.Equal(http.StatusOK, status)

	data, ok := body["data"].([]any)
	r.True(ok)
	r.Len(data, 1)
	first, ok := data[0].(map[string]any)
	r.True(ok)
	r.Equal("a-check", first["slug"])

	// ...and nothing else: the admin's cross-org reach is not inherited.
	status, _ = env.do(http.MethodGet, "/api/v1/orgs/impb/checks", token, "")
	r.Equal(http.StatusForbidden, status)

	// /auth/me says who is behind it.
	status, body = env.do(http.MethodGet, "/api/v1/auth/me", token, "")
	r.Equal(http.StatusOK, status)
	imp, ok := body["impersonation"].(map[string]any)
	r.True(ok, "/auth/me carries the impersonation")
	r.Equal(env.admin.UID, imp["impersonatorUid"])
	r.Equal(env.admin.Email, imp["impersonatorEmail"])

	// Super-admin routes are closed to it.
	status, _ = env.do(http.MethodGet, "/api/v1/system/users", token, "")
	r.Equal(http.StatusForbidden, status)

	// No chaining.
	status, _ = env.do(http.MethodPost, env.impersonatePath(), token, `{}`)
	r.Equal(http.StatusForbidden, status)

	// Signing the target out everywhere is refused.
	status, body = env.do(http.MethodPost, "/api/v1/auth/logout", token, `{"deleteAllTokens":true}`)
	r.Equal(http.StatusForbidden, status)
	r.Equal(string(base.ErrorCodeImpersonationForbidden), body["code"])

	// A PAT cannot be minted in the target's name.
	status, body = env.do(http.MethodPost, "/api/v1/orgs/impa/tokens", token, `{"name":"x"}`)
	r.Equal(http.StatusForbidden, status)
	r.Equal(string(base.ErrorCodeImpersonationForbidden), body["code"])

	// No session can be minted: switch-org is refused.
	status, _ = env.do(http.MethodPost, "/api/v1/auth/switch-org", token, `{"org":"impa"}`)
	r.Equal(http.StatusForbidden, status)

	sessions, err := env.server.dbService.ListUserTokens(context.Background(), env.member.UID)
	r.NoError(err)
	r.Empty(sessions, "the target's session list is untouched")

	// auth.impersonation_started was recorded in the target's org, by the admin.
	events, err := env.server.dbService.ListEvents(context.Background(), &models.ListEventsFilter{
		OrganizationUID: env.orgA.UID,
		EventTypes:      []models.EventType{models.EventTypeAuthImpersonationStarted},
		Limit:           10,
	})
	r.NoError(err)
	r.Len(events, 1)
	r.Equal(env.admin.UID, *events[0].ActorUID)
	r.Equal(env.member.UID, events[0].Payload[audit.PayloadKeyTargetUID])
}

// TestImpersonationWriteIsAuditedWithTheAdmin: a real write done under the
// token lands in the trail as the target, with the admin named.
func TestImpersonationWriteIsAuditedWithTheAdmin(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	env := newImpersonationEnv(t, true)

	status, body := env.do(http.MethodPost, env.impersonatePath(), env.adminToken, `{}`)
	r.Equal(http.StatusOK, status)
	token, _ := body["accessToken"].(string)

	status, body = env.do(http.MethodPost, "/api/v1/orgs/impa/checks", token,
		`{"name":"Imp","slug":"imp-check","type":"http","config":{"url":"https://acme.com"}}`)
	r.Equalf(http.StatusCreated, status, "%v", body)

	events, err := env.server.dbService.ListEvents(context.Background(), &models.ListEventsFilter{
		OrganizationUID: env.orgA.UID,
		EventTypes:      []models.EventType{models.EventTypeCheckCreated},
		Limit:           10,
	})
	r.NoError(err)
	r.NotEmpty(events)

	var found bool

	for _, event := range events {
		if event.ActorUID != nil && *event.ActorUID == env.member.UID {
			r.Equal(env.admin.UID, event.Payload[audit.PayloadKeyImpersonatedBy])

			found = true
		}
	}

	r.True(found, "the check creation was recorded as the target")
}

// TestImpersonationKillSwitch: auth.impersonation_enabled=false makes the
// endpoint answer 404.
func TestImpersonationKillSwitch(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	env := newImpersonationEnv(t, false)

	status, _ := env.do(http.MethodPost, env.impersonatePath(), env.adminToken, `{}`)
	r.Equal(http.StatusNotFound, status)
}

// TestImpersonationDenylistNamesRealRoutes keeps the denylist honest: every
// entry must be a registered route, or a renamed endpoint would silently fall
// out of the guard.
func TestImpersonationDenylistNamesRealRoutes(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	env := newImpersonationEnv(t, true)

	registered := map[string]bool{}
	r.NoError(env.server.router.Walk(func(method, pattern string) error {
		registered[method+" "+pattern] = true

		return nil
	}))

	denylist := auth.ImpersonationForbiddenRoutes()
	r.NotEmpty(denylist)

	for _, entry := range denylist {
		r.Truef(registered[entry[0]+" "+entry[1]],
			"impersonation denylist entry %s %s is not a registered route", entry[0], entry[1])
	}
}
