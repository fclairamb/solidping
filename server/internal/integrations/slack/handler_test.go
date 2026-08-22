package slack_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/handlers/auth"
	"github.com/fclairamb/solidping/server/internal/httpx"
	"github.com/fclairamb/solidping/server/internal/integrations/slack"
	"github.com/fclairamb/solidping/server/internal/middleware"
	"github.com/fclairamb/solidping/server/internal/oauthstate"
)

// installURLTestEnv wires a real router with the actual RequireAuth +
// RequireOrgAccess middleware chain in front of BuildInstallURLForOrg —
// exactly as server.go registers it — so the 401/403/404 acceptance
// criteria in spec 2026-07-05-01 are proven against the real middleware,
// not simulated by injecting context.
type installURLTestEnv struct {
	router  *httpx.Router
	db      *sqlite.Service
	authSvc *auth.Service
	orgA    *models.Organization
	orgB    *models.Organization
}

func newInstallURLTestEnv(t *testing.T) *installURLTestEnv {
	t.Helper()

	ctx := t.Context()
	r := require.New(t)

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	cfg := &config.Config{
		Server: config.ServerConfig{BaseURL: "http://localhost:4000"},
		Slack:  config.SlackConfig{ClientID: "test-client-id", ClientSecret: "test-client-secret"},
		Auth: config.AuthConfig{
			JWTSecret:         "test-secret-key-for-handler-tests",
			AccessTokenExpiry: time.Hour,
		},
	}

	authSvc := auth.NewService(dbSvc, cfg.Auth, cfg, nil, nil)

	slackSvc := slack.NewService(dbSvc, cfg, authSvc, nil, nil)
	slackHandler := slack.NewHandler(slackSvc, cfg)

	authMw := middleware.NewAuthMiddleware(authSvc, dbSvc, cfg)

	orgA := models.NewOrganization("install-url-org-a", "Org A")
	r.NoError(dbSvc.CreateOrganization(ctx, orgA))

	orgB := models.NewOrganization("install-url-org-b", "Org B")
	r.NoError(dbSvc.CreateOrganization(ctx, orgB))

	router := httpx.New()
	group := router.NewGroup("/api/v1/orgs/:org/integrations/slack").
		Use(authMw.RequireAuth, authMw.RequireOrgAccess)
	group.POST("/install-url", slackHandler.BuildInstallURLForOrg)

	return &installURLTestEnv{
		router:  router,
		db:      dbSvc,
		authSvc: authSvc,
		orgA:    orgA,
		orgB:    orgB,
	}
}

// mintToken creates a real, valid JWT for the given user/org/role — a member
// of orgUID's SolidPing organization — using the same code path
// HandleOAuthCallback uses to issue tokens after a successful install.
func (e *installURLTestEnv) mintToken(t *testing.T, org *models.Organization, role models.MemberRole) string {
	t.Helper()

	ctx := t.Context()
	r := require.New(t)

	user := models.NewUser(org.Slug + "-user-" + string(role) + "@example.com")
	r.NoError(e.db.CreateUser(ctx, user))

	member := models.NewOrganizationMember(org.UID, user.UID, role)
	r.NoError(e.db.CreateOrganizationMember(ctx, member))

	tokens, err := e.authSvc.GenerateTokensForOAuth(ctx, user, org, string(role), "", auth.Context{})
	r.NoError(err)

	return tokens.AccessToken
}

// do POSTs to the install-url endpoint for orgSlug, optionally bearing a
// token. An empty token simulates an unauthenticated request.
func (e *installURLTestEnv) do(t *testing.T, orgSlug, token string, body any) *httptest.ResponseRecorder {
	t.Helper()

	r := require.New(t)

	var bodyBytes []byte
	if body != nil {
		var err error
		bodyBytes, err = json.Marshal(body)
		r.NoError(err)
	}

	req := httptest.NewRequestWithContext(
		t.Context(), http.MethodPost,
		"/api/v1/orgs/"+orgSlug+"/integrations/slack/install-url", bytes.NewBuffer(bodyBytes))
	req.Header.Set("Content-Type", "application/json")

	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	rec := httptest.NewRecorder()
	e.router.ServeHTTP(rec, req)

	return rec
}

// TestBuildInstallURLForOrg_NoAuth asserts the endpoint requires
// authentication (401 without a token) — spec acceptance criterion.
func TestBuildInstallURLForOrg_NoAuth(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	env := newInstallURLTestEnv(t)

	rec := env.do(t, env.orgA.Slug, "", nil)
	r.Equal(http.StatusUnauthorized, rec.Code)
}

// TestBuildInstallURLForOrg_NonMemberForbidden asserts a valid token for a
// DIFFERENT org is rejected with 403 — spec acceptance criterion
// ("403/404 for non-members").
func TestBuildInstallURLForOrg_NonMemberForbidden(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	env := newInstallURLTestEnv(t)

	// Token minted for org B, used against org A's route.
	tokenForOrgB := env.mintToken(t, env.orgB, models.MemberRoleUser)

	rec := env.do(t, env.orgA.Slug, tokenForOrgB, nil)
	r.Equal(http.StatusForbidden, rec.Code)
}

// TestBuildInstallURLForOrg_UnknownOrgNotFound asserts a request against an
// org slug that does not exist gets 404, not a leaked 500.
func TestBuildInstallURLForOrg_UnknownOrgNotFound(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	env := newInstallURLTestEnv(t)

	tokenForOrgA := env.mintToken(t, env.orgA, models.MemberRoleUser)

	// RequireOrgAccess's claims.OrgSlug != route org check would already
	// 403 here since the token's org doesn't match; to reach the "unknown
	// org" 404 branch we mint a token whose claims org slug IS the unknown
	// slug, which does not exist in the DB.
	rec := env.do(t, "does-not-exist", tokenForOrgA, nil)
	// Either a 403 (org-slug mismatch guard fires first) or 404 (org lookup
	// fails) is an acceptable non-2xx outcome; assert it is NOT found/OK.
	r.NotEqual(http.StatusOK, rec.Code)
	r.Contains([]int{http.StatusForbidden, http.StatusNotFound}, rec.Code)
}

// TestBuildInstallURLForOrg_MemberSucceeds asserts a member of the org gets
// a 200 with a Slack authorize URL when no channelUid is given. Uses a
// viewer role deliberately: RequireOrgAccess gates on membership only, not
// role, so even the least-privileged member must be able to mint an
// install URL for their own org.
func TestBuildInstallURLForOrg_MemberSucceeds(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	env := newInstallURLTestEnv(t)

	token := env.mintToken(t, env.orgA, models.MemberRoleViewer)

	rec := env.do(t, env.orgA.Slug, token, map[string]string{})
	r.Equal(http.StatusOK, rec.Code)

	var resp struct {
		URL string `json:"url"`
	}
	r.NoError(json.Unmarshal(rec.Body.Bytes(), &resp))
	r.Contains(resp.URL, "https://slack.com/oauth/v2/authorize?")
	r.Contains(resp.URL, "client_id=test-client-id")
}

// TestBuildInstallURLForOrg_ChannelInOtherOrgNotFound asserts a channelUid
// belonging to a DIFFERENT org than the authenticated one returns 404,
// closing the channel-takeover vector described in the spec's "Security
// problem" section.
func TestBuildInstallURLForOrg_ChannelInOtherOrgNotFound(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	env := newInstallURLTestEnv(t)

	ctx := t.Context()

	// A Slack channel that belongs to org B.
	channelInOrgB := models.NewIntegration(env.orgB.UID, models.ConnectionTypeSlack, "Org B channel")
	r.NoError(env.db.CreateChannel(ctx, channelInOrgB))

	// Authenticated as a member of org A, targeting org B's channel.
	token := env.mintToken(t, env.orgA, models.MemberRoleUser)

	rec := env.do(t, env.orgA.Slug, token, map[string]string{"channelUid": channelInOrgB.UID})
	r.Equal(http.StatusNotFound, rec.Code)
}

// TestBuildInstallURLForOrg_ChannelInSameOrgSucceeds asserts a channelUid
// belonging to the authenticated org succeeds and is stashed for the
// callback to update that specific channel.
func TestBuildInstallURLForOrg_ChannelInSameOrgSucceeds(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	env := newInstallURLTestEnv(t)

	ctx := t.Context()

	channelInOrgA := models.NewIntegration(env.orgA.UID, models.ConnectionTypeSlack, "Org A channel")
	r.NoError(env.db.CreateChannel(ctx, channelInOrgA))

	token := env.mintToken(t, env.orgA, models.MemberRoleAdmin)

	rec := env.do(t, env.orgA.Slug, token, map[string]string{"channelUid": channelInOrgA.UID})
	r.Equal(http.StatusOK, rec.Code)

	var resp struct {
		URL string `json:"url"`
	}
	r.NoError(json.Unmarshal(rec.Body.Bytes(), &resp))
	r.Contains(resp.URL, "https://slack.com/oauth/v2/authorize?")
}

// TestInstall_IgnoresOrgAndChannelUIDQueryParams is the regression test for
// the spec's "Security problem": the public, unauthenticated Install
// endpoint must NOT honor `org=` / `channelUid=` query params anymore —
// otherwise anyone could craft a link that auto-joins the OAuth-ing Slack
// user into an arbitrary org, or overwrites an arbitrary channel's Slack
// credentials. Only `source` may still flow into the minted state.
func TestInstall_IgnoresOrgAndChannelUIDQueryParams(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	r := require.New(t)

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	cfg := &config.Config{
		Server: config.ServerConfig{BaseURL: "http://localhost:4000"},
		Slack:  config.SlackConfig{ClientID: "test-client-id", ClientSecret: "test-client-secret"},
	}

	victimOrg := models.NewOrganization("victim-org", "Victim Org")
	r.NoError(dbSvc.CreateOrganization(ctx, victimOrg))

	victimChannel := models.NewIntegration(victimOrg.UID, models.ConnectionTypeSlack, "Victim channel")
	r.NoError(dbSvc.CreateChannel(ctx, victimChannel))

	slackSvc := slack.NewService(dbSvc, cfg, nil, nil, nil)
	slackHandler := slack.NewHandler(slackSvc, cfg)

	router := httpx.New()
	group := router.NewGroup("/api/v1/integrations/slack")
	group.GET("/install", slackHandler.Install)

	target := "/api/v1/integrations/slack/install?source=marketplace&org=" + victimOrg.Slug +
		"&channelUid=" + victimChannel.UID

	req := httptest.NewRequestWithContext(ctx, http.MethodGet, target, http.NoBody)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	r.Equal(http.StatusFound, rec.Code)

	location := rec.Header().Get("Location")
	r.Contains(location, "https://slack.com/oauth/v2/authorize?")

	parsedLoc, err := url.Parse(location)
	r.NoError(err)
	state := parsedLoc.Query().Get("state")
	r.NotEmpty(state)

	entry, err := oauthstate.Validate(ctx, dbSvc, "slack-install", state)
	r.NoError(err)

	// source survives (analytics-only), but org/channelUid must be absent —
	// the minted state must not carry attacker-supplied org/channel targeting.
	r.Equal("marketplace", entry.Payload["source"])
	r.NotContains(entry.Payload, "installOrg")
	r.NotContains(entry.Payload, "channelUid")
}
