package app

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/utils/passwords"
)

const lockRoutePassword = "correct-horse-battery"

// lockRouteEnv is a fully wired server carrying one password-protected status
// page, driven through the REAL router with a real cookie jar.
//
// Service-level tests can only assert that ViewStatusPage returns ErrLocked;
// they cannot prove that every PUBLIC ROUTE is wired through the gate, that the
// unlock endpoint's Set-Cookie is actually accepted by a browser-shaped client,
// or that the Atom feed — whose handler checks the cookie itself rather than
// going through the service — agrees with the rest. That is what this covers.
type lockRouteEnv struct {
	t       *testing.T
	ts      *httptest.Server
	client  *http.Client
	server  *Server
	orgSlug string
	pageUID string
	slug    string
}

func newLockRouteEnv(t *testing.T) *lockRouteEnv {
	t.Helper()
	r := require.New(t)
	ctx := context.Background()

	cfg := &config.Config{}
	cfg.Database.Type = dbTypeSQLiteMemory
	cfg.Auth.JWTSecret = "status-page-lock-routes-secret"
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

	org := models.NewOrganization("lock-route-org", "Lock Route Org")
	r.NoError(server.dbService.CreateOrganization(ctx, org))

	page := models.NewStatusPage(org.UID, "Locked Status", "locked-status")
	page.IsDefault = true
	r.NoError(server.dbService.CreateStatusPage(ctx, page))

	// A real cookie jar: the unlock cookie is host-only and HttpOnly, so the
	// only honest way to test it is to let a client store and replay it exactly
	// as a browser would.
	jar, err := cookiejar.New(nil)
	r.NoError(err)

	return &lockRouteEnv{
		t: t, ts: ts, server: server,
		orgSlug: org.Slug, pageUID: page.UID, slug: page.Slug,
		client: &http.Client{
			Jar:           jar,
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
	}
}

// lockPage flips the page to `password` visibility with a real hash.
func (e *lockRouteEnv) lockPage() {
	e.t.Helper()
	r := require.New(e.t)

	hash, err := passwords.Hash(lockRoutePassword)
	r.NoError(err)

	visibility := models.StatusPageVisibilityPassword
	r.NoError(e.server.dbService.UpdateStatusPage(context.Background(), e.pageUID,
		&models.StatusPageUpdate{Visibility: &visibility, PasswordHash: &hash}))
}

func (e *lockRouteEnv) do(method, path, body string) (int, string) {
	e.t.Helper()
	r := require.New(e.t)

	req, err := http.NewRequestWithContext(context.Background(), method, e.ts.URL+path,
		bytes.NewReader([]byte(body)))
	r.NoError(err)
	req.Header.Set("Content-Type", "application/json")

	resp, err := e.client.Do(req)
	r.NoError(err)

	defer func() { _ = resp.Body.Close() }()

	raw, err := io.ReadAll(resp.Body)
	r.NoError(err)

	return resp.StatusCode, string(raw)
}

// gatedSurfaces is every public read a password page must gate. Naming them in
// one list is the point: a new public status-page route that forgets the gate
// shows up here as an obvious omission rather than as a leak nobody looked for.
func (e *lockRouteEnv) gatedSurfaces() []struct {
	name   string
	method string
	path   string
	body   string
} {
	base := "/api/v1/status-pages/" + e.orgSlug

	return []struct {
		name   string
		method string
		path   string
		body   string
	}{
		{"page view", http.MethodGet, base + "/" + e.slug, ""},
		{"default page view", http.MethodGet, base, ""},
		{"summary", http.MethodGet, base + "/" + e.slug + "/summary", ""},
		{"badge", http.MethodGet, base + "/" + e.slug + "/badge", ""},
		{"incident history", http.MethodGet, base + "/" + e.slug + "/incidents", ""},
		{"atom feed", http.MethodGet, base + "/" + e.slug + "/feed.xml", ""},
		{
			"public subscribe", http.MethodPost,
			"/api/v1/orgs/" + e.orgSlug + "/status-pages/" + e.pageUID + "/subscribers",
			`{"email":"visitor@example.com"}`,
		},
	}
}

// TestPasswordPageGatesEveryPublicSurface walks all seven public reads on the
// production router: every one 401s while locked, and every one succeeds after
// a single unlock — the positive control that keeps the 401s from being a
// broken fixture.
func TestPasswordPageGatesEveryPublicSurface(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	env := newLockRouteEnv(t)

	// Positive control BEFORE locking: everything answers while public.
	for _, surface := range env.gatedSurfaces() {
		status, body := env.do(surface.method, surface.path, surface.body)
		r.Less(status, http.StatusBadRequest,
			"%s must answer while the page is public; got %d %s", surface.name, status, body)
	}

	env.lockPage()

	for _, surface := range env.gatedSurfaces() {
		status, body := env.do(surface.method, surface.path, surface.body)
		r.Equal(http.StatusUnauthorized, status, "%s must be gated; body=%s", surface.name, body)
		r.Contains(body, "STATUS_PAGE_LOCKED", "%s must use the unlock-me code, not a generic 401", surface.name)
	}

	// Unlock once, through the real endpoint, and the cookie jar carries it.
	status, body := env.do(http.MethodPost,
		"/api/v1/status-pages/"+env.orgSlug+"/"+env.slug+"/unlock",
		`{"password":"`+lockRoutePassword+`"}`)
	r.Equal(http.StatusNoContent, status, "unlock failed: %s", body)

	for _, surface := range env.gatedSurfaces() {
		status, body := env.do(surface.method, surface.path, surface.body)
		r.Less(status, http.StatusBadRequest,
			"%s must answer once unlocked; got %d %s", surface.name, status, body)
	}
}

// TestUnlockRejectsTheWrongPassword pins the failure branch on the real route,
// and that a failed attempt grants nothing.
func TestUnlockRejectsTheWrongPassword(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	env := newLockRouteEnv(t)
	env.lockPage()

	status, body := env.do(http.MethodPost,
		"/api/v1/status-pages/"+env.orgSlug+"/"+env.slug+"/unlock",
		`{"password":"not-the-password"}`)
	r.Equal(http.StatusUnauthorized, status)
	r.Contains(body, "STATUS_PAGE_LOCKED")

	status, _ = env.do(http.MethodGet, "/api/v1/status-pages/"+env.orgSlug+"/"+env.slug, "")
	r.Equal(http.StatusUnauthorized, status, "a failed unlock must not grant access")
}

// TestPrivatePageStays404OnTheRouter is the counterpart distinction, proven at
// the HTTP layer: `private` must never become the knowable-but-locked 401.
func TestPrivatePageStays404OnTheRouter(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	env := newLockRouteEnv(t)

	visibility := models.StatusPageVisibilityPrivate
	r.NoError(env.server.dbService.UpdateStatusPage(context.Background(), env.pageUID,
		&models.StatusPageUpdate{Visibility: &visibility}))

	status, body := env.do(http.MethodGet, "/api/v1/status-pages/"+env.orgSlug+"/"+env.slug, "")
	r.Equal(http.StatusNotFound, status)
	r.NotContains(body, "STATUS_PAGE_LOCKED")

	// And there is nothing to unlock — the unlock endpoint must not confirm the
	// page exists either.
	status, _ = env.do(http.MethodPost,
		"/api/v1/status-pages/"+env.orgSlug+"/"+env.slug+"/unlock",
		`{"password":"`+lockRoutePassword+`"}`)
	r.Equal(http.StatusNotFound, status)
}
