package cli

import (
	"encoding/json"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// fakeAuthServer answers the login, device-authorization and /auth/me
// endpoints the way a real server does on success. orgless drops the
// organization from /auth/me.
func fakeAuthServer(t *testing.T, orgless bool) http.Handler {
	t.Helper()

	access, err := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"exp": time.Now().Add(time.Hour).Unix(),
	}).SignedString([]byte("test-secret"))
	if err != nil {
		t.Fatal(err)
	}

	var polls atomic.Int32

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		reply := func(status int, v any) {
			w.WriteHeader(status)
			_ = json.NewEncoder(w).Encode(v)
		}

		switch r.URL.Path {
		case "/api/v1/auth/login":
			reply(http.StatusOK, map[string]any{
				"accessToken": access, "refreshToken": "refresh", "expiresIn": 3600, "tokenType": "Bearer",
				"user": map[string]any{"uid": smokeUID, "email": "alice@acme.com"},
			})
		case devicePathAuthorize:
			reply(http.StatusOK, map[string]any{
				"device_code": "dev", "user_code": "ABCD-EFGH", "verification_uri": "http://example.invalid/device",
				"verification_uri_complete": "http://example.invalid/device?code=ABCD-EFGH",
				"expires_in": 30, "interval": 1,
			})
		case devicePathToken:
			if polls.Add(1) == 1 {
				reply(http.StatusBadRequest, map[string]any{"error": "authorization_pending"})

				return
			}
			reply(http.StatusOK, map[string]any{"access_token": "sp_pat_minted", "token_type": "Bearer"})
		case "/api/v1/auth/me":
			me := map[string]any{"user": map[string]any{"uid": smokeUID, "email": "alice@acme.com"}}
			if !orgless {
				me["organization"] = map[string]any{"uid": smokeUID, "slug": "acme", "name": "Acme"}
			}
			reply(http.StatusOK, me)
		default:
			reply(http.StatusOK, map[string]any{"data": []any{}})
		}
	})
}

// TestAuthLoginFlowsCover runs the password and device login flows end to
// end against a fake server, in both output formats, plus the device flow
// with an org-less session. PATH is emptied so the device flow's best-effort
// browser launch fails instead of opening a real browser.
//
//nolint:paralleltest // process-global stdio, HOME and PATH
func TestAuthLoginFlowsCover(t *testing.T) {
	logins := [][]string{
		{"auth", "login", "--email", "alice@acme.com", "--password", "secret"},
		{"auth", "login"},
		{"auth", "me"},
		{"auth", "logout"},
	}

	for _, format := range []string{"text", "json"} {
		//nolint:paralleltest // process-global stdio, HOME and PATH
		t.Run(format, func(t *testing.T) {
			t.Setenv("PATH", t.TempDir())
			runInvocations(t, format, fakeAuthServer(t, false), logins)
		})
	}

	//nolint:paralleltest // process-global stdio, HOME and PATH
	t.Run("orgless", func(t *testing.T) {
		t.Setenv("PATH", t.TempDir())
		runInvocations(t, "text", fakeAuthServer(t, true), [][]string{{"auth", "login"}})
	})
}
