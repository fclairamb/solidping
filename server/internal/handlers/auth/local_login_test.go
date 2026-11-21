package auth

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/handlers/base"
	"github.com/fclairamb/solidping/server/internal/systemconfig"
)

// localLoginWorld is an auth service whose config has an SSO provider (so
// local login may really be switched off) and local login set as asked.
func localLoginWorld(t *testing.T, localLogin, withSSO bool) (*Service, *Handler, db.Service, context.Context) {
	t.Helper()

	svc, dbSvc, ctx := setupAuthTestService(t)
	svc.fullCfg.Auth.LocalLoginEnabled = localLogin

	if withSSO {
		svc.fullCfg.Google = config.GoogleOAuthConfig{Enabled: true, ClientID: "id", ClientSecret: "secret"}
	}

	return svc, NewHandler(svc, svc.fullCfg), dbSvc, ctx
}

func postJSON(
	ctx context.Context, t *testing.T, path, body string, call func(http.ResponseWriter, *http.Request) error,
) *httptest.ResponseRecorder {
	t.Helper()

	req := httptest.NewRequestWithContext(ctx, http.MethodPost, path, strings.NewReader(body))
	rec := httptest.NewRecorder()
	require.NoError(t, call(rec, req))

	return rec
}

// Every local-credential endpoint answers 403 LOCAL_LOGIN_DISABLED while local
// login is off, before looking at the body.
func TestLocalLoginDisabled_EndpointsAnswer403(t *testing.T) {
	t.Parallel()

	_, handler, dbSvc, ctx := localLoginWorld(t, false, true)
	changePasswordFixture(t, ctx, dbSvc, "acme", "alice@acme.com")

	claims := &Claims{UserUID: "whoever"}

	endpoints := map[string]func(http.ResponseWriter, *http.Request) error{
		"/auth/login":                  handler.Login,
		"/auth/register":               handler.Register,
		"/auth/confirm-registration":   handler.ConfirmRegistration,
		"/auth/request-password-reset": handler.RequestPasswordReset,
		"/auth/reset-password":         handler.ResetPassword,
	}

	for path, call := range endpoints {
		rec := postJSON(ctx, t, path,
			`{"email":"alice@acme.com","password":"testpass1234","token":"x","org":"acme"}`, call)
		require.Equal(t, http.StatusForbidden, rec.Code, path)
		require.Equal(t, string(base.ErrorCodeLocalLoginDisabled), decodeErrorCode(t, rec), path)
	}

	rec := postChangePassword(t, ctx, handler, claims, `{"newPassword":"another-pass-1234"}`)
	require.Equal(t, http.StatusForbidden, rec.Code)
	require.Equal(t, string(base.ErrorCodeLocalLoginDisabled), decodeErrorCode(t, rec))
}

// The service refuses a password (or LDAP) login too, whatever the caller.
func TestLocalLoginDisabled_ServiceLoginRefused(t *testing.T) {
	t.Parallel()

	svc, _, dbSvc, ctx := localLoginWorld(t, false, true)
	changePasswordFixture(t, ctx, dbSvc, "acme", "alice@acme.com")

	_, err := svc.Login(ctx, "acme", "alice@acme.com", "testpass1234", Context{})
	require.ErrorIs(t, err, ErrLocalLoginDisabled)
}

func TestLocalLoginEnabled_LoginWorks(t *testing.T) {
	t.Parallel()

	_, handler, dbSvc, ctx := localLoginWorld(t, true, true)
	changePasswordFixture(t, ctx, dbSvc, "acme", "alice@acme.com")

	rec := postJSON(ctx, t, "/auth/login",
		`{"email":"alice@acme.com","password":"testpass1234","org":"acme"}`, handler.Login)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
}

// Lock-out guard: local login configured off with no SSO provider stays on.
func TestLocalLoginDisabled_NoSSOProviderFailsOpen(t *testing.T) {
	t.Parallel()

	svc, handler, dbSvc, ctx := localLoginWorld(t, false, false)
	changePasswordFixture(t, ctx, dbSvc, "acme", "alice@acme.com")

	require.True(t, svc.LocalLoginEnabled(ctx))

	rec := postJSON(ctx, t, "/auth/login",
		`{"email":"alice@acme.com","password":"testpass1234","org":"acme"}`, handler.Login)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
}

// A DB toggle takes effect on the next request, without a restart.
func TestLocalLogin_DBToggleNeedsNoRestart(t *testing.T) {
	t.Parallel()

	svc, handler, dbSvc, ctx := localLoginWorld(t, true, true)
	changePasswordFixture(t, ctx, dbSvc, "acme", "alice@acme.com")

	require.True(t, svc.LocalLoginEnabled(ctx))
	require.NoError(t, dbSvc.SetSystemParameter(ctx, string(systemconfig.KeyLocalLoginEnabled), false, false))
	require.False(t, svc.LocalLoginEnabled(ctx))

	rec := postJSON(ctx, t, "/auth/login",
		`{"email":"alice@acme.com","password":"testpass1234","org":"acme"}`, handler.Login)
	require.Equal(t, http.StatusForbidden, rec.Code)

	require.NoError(t, dbSvc.SetSystemParameter(ctx, string(systemconfig.KeyLocalLoginEnabled), true, false))
	require.True(t, svc.LocalLoginEnabled(ctx))
}

// GET /auth/providers tells the login page whether to show the password form.
func TestListProviders_LocalLoginFlag(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name       string
		localLogin bool
		withSSO    bool
		want       bool
	}{
		{"on", true, true, true},
		{"off with SSO", false, true, false},
		{"off without SSO fails open", false, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			svc, _, _, ctx := localLoginWorld(t, tc.localLogin, tc.withSSO)
			svc.fullCfg.Auth.RegistrationEmailPattern = ".*"

			handler := NewProvidersHandler(svc.fullCfg, nil)
			handler.SetLocalLoginResolver(svc.LocalLoginEnabled)

			req := httptest.NewRequestWithContext(ctx, http.MethodGet, "/auth/providers", nil)
			rec := httptest.NewRecorder()
			require.NoError(t, handler.ListProviders(rec, req))

			var resp ProvidersResponse
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
			require.Equal(t, tc.want, resp.LocalLogin)
			require.Equal(t, tc.want, resp.RegistrationEnabled, "sign-up follows local login")
		})
	}
}
