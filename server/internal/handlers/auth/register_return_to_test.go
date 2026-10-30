package auth

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
)

const testAuthorizeReturnTo = "/api/v1/oauth/authorize?client_id=acme-client&state=s1"

func TestPendingFlowReturnTo(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		raw  string
		want string
	}{
		{testAuthorizeReturnTo, testAuthorizeReturnTo},
		{"/api/v1/oauth/authorize", "/api/v1/oauth/authorize"},
		{config.DashboardBasePath + "/device?user_code=ABCD-EFGH", config.DashboardBasePath + "/device?user_code=ABCD-EFGH"},
		{"", ""},
		{"https://evil.example/api/v1/oauth/authorize?client_id=x", ""},
		{"//evil.example/api/v1/oauth/authorize", ""},
		{"/\\evil.example/api/v1/oauth/authorize", ""},
		{"/api/v1/oauth/authorize/extra", ""},
		{"/api/v1/oauth/authorizex", ""},
		{"/api/v1/oauth/authorize#frag", ""},
		{config.DashboardBasePath + "/orgs/acme/checks", ""},
		{config.DashboardBasePath + "/devices", ""},
		{"/api/v1/oauth/authorize?x=\n", ""},
	} {
		require.Equal(t, tc.want, pendingFlowReturnTo(tc.raw), "raw=%q", tc.raw)
	}
}

// A sign-up started by an MCP client keeps its pending authorize request
// across the email confirmation (fclairamb/solidping-business#35): the link
// opens in a new tab, so the value travels server-side and comes back from
// confirm-registration. Anything outside the allowlist is dropped.
func TestRegisterKeepsPendingFlowReturnTo(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name     string
		email    string
		returnTo string
		want     string
	}{
		{"authorize", "alice@acme.com", testAuthorizeReturnTo, testAuthorizeReturnTo},
		{"absolute", "bob@acme.com", "https://evil.example/api/v1/oauth/authorize", ""},
		{"protocol-relative", "carol@acme.com", "//evil.example/api/v1/oauth/authorize", ""},
		{"other path", "dave@acme.com", config.DashboardBasePath + "/orgs/acme", ""},
		{"none", "erin@acme.com", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)
			f, ctx := newLoginAuditFixture(t)
			f.svc.fullCfg.Auth.RegistrationEmailPattern = ".*"

			_, err := f.svc.Register(ctx, RegisterRequest{
				Email: tc.email, Password: "supersecret123", ReturnTo: tc.returnTo,
			})
			r.NoError(err)

			entry, err := f.db.GetStateEntry(ctx, nil, registrationKeyPrefix+tc.email)
			r.NoError(err)
			r.NotNil(entry)

			stored, hasKey := (*entry.Value)[keyReturnTo]
			if tc.want == "" {
				r.False(hasKey, "a refused returnTo is not stored: %v", stored)
			} else {
				r.Equal(tc.want, stored)
			}

			token := extractRegistrationConfirmToken(t, ctx, f.db, tc.email)
			resp, err := f.svc.ConfirmRegistration(ctx, token)
			r.NoError(err)
			r.NotEmpty(resp.AccessToken)
			r.Equal(tc.want, resp.ReturnTo)
		})
	}
}
