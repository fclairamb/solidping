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
	"github.com/fclairamb/solidping/server/internal/utils/passwords"
)

const subRouteTestPassword = "sub-route-test-pass"

// subRouteEnv is a fully wired server used to prove that the two status-page
// subscription entry points BOTH exist on the production router.
//
// This is a router-level test on purpose. The service-level tests in
// handlers/statussubscribers call CreateEndpointSubscriber directly and
// therefore cannot see the failure mode this pins: chi's node.setEndpoint
// silently OVERWRITES a duplicate method+pattern instead of panicking, so
// registering the operator-side creator and the public email subscriber on the
// same `POST …/subscribers` pattern left the operator one unreachable while
// every service test stayed green.
type subRouteEnv struct {
	t       *testing.T
	ts      *httptest.Server
	client  *http.Client
	server  *Server
	orgSlug string
	pageUID string
	jwt     string
}

func newSubRouteEnv(t *testing.T) *subRouteEnv {
	t.Helper()
	r := require.New(t)
	ctx := context.Background()

	cfg := &config.Config{}
	cfg.Database.Type = dbTypeSQLiteMemory
	cfg.Auth.JWTSecret = "status-subscriber-routes-secret"
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

	org := models.NewOrganization("sub-route-org", "Sub Route Org")
	r.NoError(server.dbService.CreateOrganization(ctx, org))

	page := models.NewStatusPage(org.UID, "Sub Route Status", "sub-route-status")
	r.NoError(server.dbService.CreateStatusPage(ctx, page))

	hash, err := passwords.Hash(subRouteTestPassword)
	r.NoError(err)
	now := time.Now()

	user := models.NewUser("admin@sub-route.example")
	user.PasswordHash = &hash
	user.EmailVerifiedAt = &now
	r.NoError(server.dbService.CreateUser(ctx, user))

	member := models.NewOrganizationMember(org.UID, user.UID, models.MemberRoleAdmin)
	member.JoinedAt = &now
	r.NoError(server.dbService.CreateOrganizationMember(ctx, member))

	env := &subRouteEnv{
		t: t, ts: ts, server: server, orgSlug: org.Slug, pageUID: page.UID,
		client: &http.Client{
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
	}
	env.jwt = env.login("admin@sub-route.example")

	return env
}

func (e *subRouteEnv) login(email string) string {
	e.t.Helper()
	r := require.New(e.t)

	body, err := json.Marshal(map[string]string{
		"org": e.orgSlug, "email": email, "password": subRouteTestPassword,
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

	return payload.AccessToken
}

// post sends a JSON POST, optionally authenticated.
func (e *subRouteEnv) post(path, body string, authed bool) (int, string) {
	e.t.Helper()
	r := require.New(e.t)

	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost,
		e.ts.URL+path, bytes.NewReader([]byte(body)))
	r.NoError(err)
	req.Header.Set("Content-Type", "application/json")

	if authed {
		req.Header.Set("Authorization", "Bearer "+e.jwt)
	}

	resp, err := e.client.Do(req)
	r.NoError(err)

	defer func() { _ = resp.Body.Close() }()

	raw, err := io.ReadAll(resp.Body)
	r.NoError(err)

	return resp.StatusCode, string(raw)
}

func (e *subRouteEnv) subscribersPath() string {
	return "/api/v1/orgs/" + e.orgSlug + "/status-pages/" + e.pageUID + "/subscribers"
}

// TestStatusSubscriberRoutesDoNotCollide is the regression test for the chi
// overwrite. Both entry points must WORK — not merely be registered — through
// the production router.
func TestStatusSubscriberRoutesDoNotCollide(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	env := newSubRouteEnv(t)

	// 1. Operator-side: registering a webhook must succeed and hand back the
	//    signing secret exactly once.
	status, body := env.post(env.subscribersPath()+"/endpoints",
		`{"channel":"webhook","url":"https://hooks.example.com/services/abcdef123456"}`, true)
	r.Equal(http.StatusCreated, status, "operator endpoint registration must reach its own handler; body=%s", body)

	var created struct {
		Data struct {
			UID           string `json:"uid"`
			Channel       string `json:"channel"`
			Endpoint      string `json:"endpoint"`
			SigningSecret string `json:"signingSecret"`
		} `json:"data"`
	}

	r.NoError(json.Unmarshal([]byte(body), &created))
	r.Equal("webhook", created.Data.Channel)
	r.NotEmpty(created.Data.SigningSecret, "the secret must be shown once, or the HMAC is unverifiable")
	r.NotContains(created.Data.Endpoint, "abcdef123456", "the list/echo form of the URL must stay masked")

	// 2. Public: the email path still works on its own pattern...
	status, body = env.post(env.subscribersPath(), `{"email":"visitor@example.com"}`, false)
	r.Equal(http.StatusAccepted, status, "public email subscribe must still reach the public handler; body=%s", body)

	// 3. ...and still refuses a non-email channel. This is the assertion that
	//    used to pass for the WRONG reason: before the split, the public
	//    handler answered the operator's request too.
	status, body = env.post(env.subscribersPath(),
		`{"email":"visitor@example.com","channel":"webhook"}`, false)
	// 422 is this codebase's VALIDATION_ERROR status (base.WriteValidationError).
	r.Equal(http.StatusUnprocessableEntity, status)
	r.Contains(body, "email-only")

	// 4. The operator route is genuinely authenticated — it is not a second
	//    public way in.
	status, _ = env.post(env.subscribersPath()+"/endpoints",
		`{"channel":"webhook","url":"https://hooks.example.com/services/zzz"}`, false)
	r.Equal(http.StatusUnauthorized, status)
}

// TestStatusSubscriberRoutePatternsAreUnique walks the REAL route table and
// fails on any duplicate method+pattern under the status-page subscriber
// subtree.
//
// The test above proves the two handlers currently coexist; this one explains
// WHY a future duplicate would be invisible, and catches it at the table level
// rather than through a behavioral symptom. chi does not panic on a duplicate
// registration, so nothing else in the build would.
func TestStatusSubscriberRoutePatternsAreUnique(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	env := newSubRouteEnv(t)

	seen := make(map[string]int)
	r.NoError(env.server.router.Walk(func(method, pattern string) error {
		seen[method+" "+pattern]++

		return nil
	}))

	for key, count := range seen {
		r.Equal(1, count, "duplicate route registration %q — chi silently keeps only the last one", key)
	}

	r.Equal(1, seen["POST /api/v1/orgs/{org}/status-pages/{statusPageUid}/subscribers"])
	r.Equal(1, seen["POST /api/v1/orgs/{org}/status-pages/{statusPageUid}/subscribers/endpoints"])
}
