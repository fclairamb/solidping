package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/handlers/system"
	"github.com/fclairamb/solidping/server/internal/systemconfig"
)

// The tests below are not parallel: NewServer writes package-level resolver
// hooks, which -race reports when two servers are built at once.

// ssoLiveEnv is a fully wired server with no SSO provider at boot, plus the
// system service the dashboard's Server Settings > Authentication page saves
// through.
type ssoLiveEnv struct {
	server *Server
	ts     *httptest.Server
	sys    *system.Service
	issuer string
}

func newSSOLiveEnv(t *testing.T) *ssoLiveEnv {
	t.Helper()
	r := require.New(t)
	ctx := context.Background()

	// A discovery-only IdP: enough for /auth/oidc/login to build its redirect.
	var issuer string

	idp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path != "/.well-known/openid-configuration" {
			http.NotFound(w, req)

			return
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issuer":                 issuer,
			"authorization_endpoint": issuer + "/authorize",
			"token_endpoint":         issuer + "/token",
			"jwks_uri":               issuer + "/jwks",
		})
	}))
	t.Cleanup(idp.Close)
	issuer = idp.URL

	cfg := &config.Config{}
	cfg.Database.Type = dbTypeSQLiteMemory
	cfg.Auth.JWTSecret = "sso-live-secret-sso-live-secret-0123"
	cfg.Auth.AccessTokenExpiry = time.Hour
	cfg.Auth.RefreshTokenExpiry = 24 * time.Hour
	cfg.Auth.LocalLoginEnabled = true

	server, err := NewServer(ctx, cfg)
	r.NoError(err)
	t.Cleanup(func() { _ = server.dbService.Close() })

	r.NoError(server.Initialize(ctx))
	r.NoError(server.InitializeSystemConfig(ctx, cfg))
	server.SetupRoutes(ctx)

	ts := httptest.NewServer(server.Handler())
	t.Cleanup(ts.Close)

	r.NoError(server.dbService.CreateOrganization(ctx, models.NewOrganization("acme", "Acme")))

	sys := system.NewService(server.dbService)
	sys.SetAppConfig(cfg)

	return &ssoLiveEnv{server: server, ts: ts, sys: sys, issuer: issuer}
}

func (e *ssoLiveEnv) providerTypes(t *testing.T) []string {
	t.Helper()

	resp, err := http.Get(e.ts.URL + "/api/v1/auth/providers") //nolint:noctx // test
	require.NoError(t, err)

	defer func() { _ = resp.Body.Close() }()

	require.Equal(t, http.StatusOK, resp.StatusCode)

	var body struct {
		Data []struct {
			Type string `json:"type"`
		} `json:"data"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))

	types := make([]string, 0, len(body.Data))
	for _, p := range body.Data {
		types = append(types, p.Type)
	}

	return types
}

// oidcLogin returns the status and Location of GET /auth/oidc/login.
func (e *ssoLiveEnv) oidcLogin(t *testing.T) (int, string) {
	t.Helper()

	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}

	resp, err := client.Get(e.ts.URL + "/api/v1/auth/oidc/login?org=acme") //nolint:noctx // test
	require.NoError(t, err)

	defer func() { _ = resp.Body.Close() }()

	return resp.StatusCode, resp.Header.Get("Location")
}

func (e *ssoLiveEnv) oidcLoginStatus(t *testing.T) int {
	t.Helper()

	status, _ := e.oidcLogin(t)

	return status
}

func (e *ssoLiveEnv) set(t *testing.T, key string, value any, secret bool) {
	t.Helper()

	_, err := e.sys.SetParameter(context.Background(), key, value, secret)
	require.NoError(t, err, key)
}

func (e *ssoLiveEnv) saveOIDC(t *testing.T) {
	t.Helper()

	// The same writes the OIDC card's Save button sends.
	e.set(t, string(systemconfig.KeyOIDCDisplayName), "Acme SSO", false)
	e.set(t, string(systemconfig.KeyOIDCIssuerURL), e.issuer, false)
	e.set(t, string(systemconfig.KeyOIDCClientID), "acme-client", false)
	e.set(t, string(systemconfig.KeyOIDCClientSecret), "acme-secret", true)
	e.set(t, string(systemconfig.KeyOIDCEnabled), true, false)
}

// A provider saved on Server Settings > Authentication is live without a
// restart: listed on the login page, its routes answer, and local login may be
// switched off (spec 2026-11-25-02). Switching it off reverts all three.
//
//nolint:paralleltest // NewServer writes package-level hooks, see above.
func TestSSOProviderSavedIsLiveWithoutRestart(t *testing.T) {
	r := require.New(t)
	ctx := context.Background()
	env := newSSOLiveEnv(t)
	localLogin := string(systemconfig.KeyLocalLoginEnabled)

	// Boot: nothing configured.
	r.NotContains(env.providerTypes(t), "oidc")
	r.Equal(http.StatusNotFound, env.oidcLoginStatus(t), "the route is mounted but gated")

	_, err := env.sys.SetParameter(ctx, localLogin, false, false)
	r.ErrorIs(err, systemconfig.ErrNoSSOProvider)

	env.saveOIDC(t)

	r.Contains(env.providerTypes(t), "oidc")

	status, location := env.oidcLogin(t)
	r.Equal(http.StatusFound, status)
	r.True(strings.HasPrefix(location, env.issuer+"/authorize?"), "redirects to the issuer, got %q", location)
	r.Contains(location, "client_id=acme-client")

	_, err = env.sys.SetParameter(ctx, localLogin, false, false)
	r.NoError(err, "local login may be switched off once OIDC is live")

	// Turn local login back on, then disable OIDC: everything reverts.
	env.set(t, localLogin, true, false)
	env.set(t, string(systemconfig.KeyOIDCEnabled), false, false)

	r.NotContains(env.providerTypes(t), "oidc")
	r.Equal(http.StatusNotFound, env.oidcLoginStatus(t))

	_, err = env.sys.SetParameter(ctx, localLogin, false, false)
	r.ErrorIs(err, systemconfig.ErrNoSSOProvider)
}

// The OIDC routes follow Configured(): without a client secret they stay 404,
// like the login page that does not list the provider.
//
//nolint:paralleltest // NewServer writes package-level hooks, see above.
func TestOIDCRoutesNeedTheClientSecret(t *testing.T) {
	r := require.New(t)
	env := newSSOLiveEnv(t)

	env.set(t, string(systemconfig.KeyOIDCIssuerURL), env.issuer, false)
	env.set(t, string(systemconfig.KeyOIDCClientID), "acme-client", false)
	env.set(t, string(systemconfig.KeyOIDCEnabled), true, false)

	r.NotContains(env.providerTypes(t), "oidc")
	r.Equal(http.StatusNotFound, env.oidcLoginStatus(t))

	env.set(t, string(systemconfig.KeyOIDCClientSecret), "acme-secret", true)

	r.Contains(env.providerTypes(t), "oidc")
	r.Equal(http.StatusFound, env.oidcLoginStatus(t))
}

// Deleting a provider's parameters falls back to the startup config.
//
//nolint:paralleltest // NewServer writes package-level hooks, see above.
func TestSSOParameterDeleteIsLive(t *testing.T) {
	r := require.New(t)
	ctx := context.Background()
	env := newSSOLiveEnv(t)

	env.saveOIDC(t)
	r.Contains(env.providerTypes(t), "oidc")

	r.NoError(env.sys.DeleteParameter(ctx, string(systemconfig.KeyOIDCEnabled)))
	r.NotContains(env.providerTypes(t), "oidc")
}

// Login requests read the SSO snapshot while a super admin rewrites it; run
// under -race this asserts the snapshot is published, not mutated in place.
//
//nolint:paralleltest // NewServer writes package-level hooks, see above.
func TestSSOSnapshotConcurrentSaveAndList(t *testing.T) {
	r := require.New(t)
	env := newSSOLiveEnv(t)
	env.saveOIDC(t)

	stop := make(chan struct{})

	var wg sync.WaitGroup

	for range 4 {
		wg.Add(1)

		go func() {
			defer wg.Done()

			for {
				select {
				case <-stop:
					return
				default:
				}

				resp, err := http.Get(env.ts.URL + "/api/v1/auth/providers") //nolint:noctx // test
				if err == nil {
					_ = resp.Body.Close()
				}

				_ = env.server.config.SSOProviders()
			}
		}()
	}

	for i := range 20 {
		env.set(t, string(systemconfig.KeyOIDCEnabled), i%2 == 0, false)
	}

	close(stop)
	wg.Wait()

	r.NotContains(env.providerTypes(t), "oidc", "the last save (i=19) disabled OIDC")
}
