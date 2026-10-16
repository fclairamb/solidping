package cli

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/pkg/cli/apihelper"
	"github.com/fclairamb/solidping/server/pkg/cli/config"
	"github.com/fclairamb/solidping/server/pkg/cli/output"
	"github.com/fclairamb/solidping/server/pkg/client"
)

// acceptInviteTestServer answers accept-invite with the given status and
// error code, /auth/me with bob, and records the Authorization header it got.
func acceptInviteTestServer(t *testing.T, status int, code string, gotAuth *string) *httptest.Server {
	t.Helper()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		switch req.URL.Path {
		case "/api/v1/auth/accept-invite":
			*gotAuth = req.Header.Get("Authorization")
			w.WriteHeader(status)
			_, _ = w.Write([]byte(`{"title":"refused","code":"` + code + `"}`))
		case "/api/v1/auth/me":
			_, _ = w.Write([]byte(`{"user":{"uid":"00000000-0000-0000-0000-000000000001","email":"bob@acme.com"}}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)

	return srv
}

func acceptInviteTestContext(t *testing.T, url, pat string) (*Context, *bytes.Buffer) {
	t.Helper()

	cfg := &config.Config{URL: url, Org: "acme", Auth: config.Auth{PAT: pat}}
	out := &bytes.Buffer{}

	return &Context{
		Config:       cfg,
		APIHelper:    apihelper.NewHelper(cfg, filepath.Join(t.TempDir(), "token.json"), false),
		Outputter:    output.NewJSONOutputter(out),
		OutputFormat: output.FormatJSON,
	}, out
}

// TestAcceptInviteSendsTheStoredCredential: the CLI authenticates the call
// when it has a credential, and stays anonymous when it has none.
func TestAcceptInviteSendsTheStoredCredential(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		pat      string
		wantAuth string
	}{
		{name: "with a credential", pat: "sp_pat_test", wantAuth: "Bearer sp_pat_test"},
		{name: "without a credential", pat: "", wantAuth: ""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)

			var gotAuth string
			srv := acceptInviteTestServer(t, http.StatusUnauthorized, "INVITE_LOGIN_REQUIRED", &gotAuth)
			cliCtx, _ := acceptInviteTestContext(t, srv.URL, tc.pat)

			apiClient, err := acceptInviteClient(t.Context(), cliCtx)
			r.NoError(err)

			_, err = apiClient.AcceptInviteWithResponse(t.Context(), client.AcceptInviteJSONRequestBody{Token: "tok"})
			r.NoError(err)
			r.Equal(tc.wantAuth, gotAuth)
		})
	}
}

// TestAcceptInviteFailureMessages: the two refusals explain what to do.
func TestAcceptInviteFailureMessages(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		status int
		code   string
		want   []string
	}{
		{
			name: "login required", status: http.StatusUnauthorized, code: "INVITE_LOGIN_REQUIRED",
			want: []string{"existing account", "sp auth login"},
		},
		{
			name: "email mismatch", status: http.StatusForbidden, code: "INVITE_EMAIL_MISMATCH",
			want: []string{"another account", "signed in as bob@acme.com"},
		},
		{
			name: "other error", status: http.StatusNotFound, code: "INVITATION_NOT_FOUND",
			want: []string{"status: 404"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)

			var gotAuth string
			srv := acceptInviteTestServer(t, tc.status, tc.code, &gotAuth)
			cliCtx, out := acceptInviteTestContext(t, srv.URL, "sp_pat_test")

			apiClient, err := acceptInviteClient(t.Context(), cliCtx)
			r.NoError(err)

			resp, err := apiClient.AcceptInviteWithResponse(t.Context(), client.AcceptInviteJSONRequestBody{Token: "tok"})
			r.NoError(err)

			r.NoError(acceptInviteFailure(t.Context(), cliCtx, apiClient, resp))
			for _, want := range tc.want {
				r.Contains(out.String(), want)
			}
		})
	}
}
