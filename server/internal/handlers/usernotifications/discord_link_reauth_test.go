package usernotifications

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/audit"
	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/handlers/auth"
	"github.com/fclairamb/solidping/server/internal/handlers/base"
	"github.com/fclairamb/solidping/server/internal/utils/passwords"
	"github.com/fclairamb/solidping/server/internal/webpush"
)

// Spec 2026-11-01-03: a linked Discord identity signs in, so link-start asks
// the same re-authentication as a new TOTP or passkey.

const linkTestPassword = "correct-horse-battery"

// linkReauthEnv is a contact env whose service re-authenticates through the
// real auth service.
func linkReauthEnv(t *testing.T, withPassword bool) (context.Context, *contactEnv, *Handler) {
	t.Helper()

	ctx, env := setupContactEnv(t, WithServerBaseURL("https://solidping.example"))

	if withPassword {
		hash, err := passwords.Hash(linkTestPassword)
		require.NoError(t, err)
		require.NoError(t, env.db.UpdateUser(ctx, env.user.UID, &models.UserUpdate{PasswordHash: &hash}))

		env.user.PasswordHash = &hash
	}

	cfg := &config.Config{Auth: config.AuthConfig{JWTSecret: "test-secret-1234567890"}}
	env.svc.reauth = auth.NewService(env.db, cfg.Auth, cfg, nil, nil)

	return ctx, env, NewHandler(env.svc, cfg, nil, nil, webpush.Options{})
}

// sessionCtx is the context RequireAuth builds for a session whose primary
// sign-in happened at signedInAt.
func sessionCtx(ctx context.Context, user *models.User, signedInAt time.Time) context.Context {
	ctx = context.WithValue(ctx, base.ContextKeyUser, user)

	return context.WithValue(ctx, base.ContextKeyClaims,
		&auth.Claims{UserUID: user.UID, OrgSlug: "acme", AuthTime: signedInAt.Unix()})
}

// postLinkStart drives the real link-start handler.
//
//nolint:revive // ctx-second matches the existing test helpers
func postLinkStart(
	t *testing.T, ctx context.Context, handler *Handler, body string,
) *httptest.ResponseRecorder {
	t.Helper()

	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("org", "acme")

	req := httptest.NewRequestWithContext(context.WithValue(ctx, chi.RouteCtxKey, rctx), http.MethodPost,
		"/api/v1/orgs/acme/users/me/discord/link-start", strings.NewReader(body))
	rec := httptest.NewRecorder()

	require.NoError(t, handler.CreateDiscordLink(rec, req))

	return rec
}

func errorCode(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()

	var body struct {
		Code string `json:"code"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body), rec.Body.String())

	return body.Code
}

// linkTokenCount is the number of live link tokens.
//
//nolint:revive // ctx-second matches the existing test helpers
func linkTokenCount(t *testing.T, ctx context.Context, env *contactEnv) int {
	t.Helper()

	entries, err := env.db.ListStateEntries(ctx, nil, "discord_link:")
	require.NoError(t, err)

	return len(entries)
}

func TestDiscordLinkStartRequiresCurrentPassword(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		body string
	}{
		{"missing", `{"redirectUri":"/d/orgs/acme/account/notifications"}`},
		{"empty body", ``},
		{"wrong", `{"redirectUri":"/d/orgs/acme/account/notifications","currentPassword":"nope"}`},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)
			ctx, env, handler := linkReauthEnv(t, true)

			// A fresh session is not enough for an account with a password.
			rec := postLinkStart(t, sessionCtx(ctx, env.user, time.Now()), handler, tc.body)
			r.Equal(http.StatusUnauthorized, rec.Code, rec.Body.String())
			r.Equal(string(base.ErrorCodeInvalidCurrentPassword), errorCode(t, rec))
			r.Zero(linkTokenCount(t, ctx, env), "a refused link-start mints no link token")
		})
	}
}

func TestDiscordLinkStartWithCorrectPassword(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx, env, handler := linkReauthEnv(t, true)

	// An old sign-in does not matter once the password is proven.
	rec := postLinkStart(t, sessionCtx(ctx, env.user, time.Now().Add(-24*time.Hour)), handler,
		`{"redirectUri":"/d/orgs/acme/account/notifications","currentPassword":"`+linkTestPassword+`"}`)
	r.Equal(http.StatusCreated, rec.Code, rec.Body.String())

	var resp DiscordLinkResponse
	r.NoError(json.Unmarshal(rec.Body.Bytes(), &resp))
	r.Contains(resp.URL, "state=link")
	r.Equal(1, linkTokenCount(t, ctx, env))
}

func TestDiscordLinkStartPasswordlessNeedsRecentSignIn(t *testing.T) {
	t.Parallel()

	t.Run("stale sign-in", func(t *testing.T) {
		t.Parallel()
		r := require.New(t)
		ctx, env, handler := linkReauthEnv(t, false)

		rec := postLinkStart(t, sessionCtx(ctx, env.user, time.Now().Add(-time.Hour)), handler, `{}`)
		r.Equal(http.StatusForbidden, rec.Code, rec.Body.String())
		r.Equal(string(base.ErrorCodeReauthenticationRequired), errorCode(t, rec))
		r.Zero(linkTokenCount(t, ctx, env))
	})

	t.Run("recent sign-in", func(t *testing.T) {
		t.Parallel()
		r := require.New(t)
		ctx, env, handler := linkReauthEnv(t, false)

		rec := postLinkStart(t, sessionCtx(ctx, env.user, time.Now()), handler, `{}`)
		r.Equal(http.StatusCreated, rec.Code, rec.Body.String())
		r.Equal(1, linkTokenCount(t, ctx, env))
	})
}

// TestDiscordLinkStartRefusesImpersonationBeforePassword: the impersonation
// refusal still holds, and it comes first, so an impersonating admin who knows
// the password gets no link either.
func TestDiscordLinkStartRefusesImpersonationBeforePassword(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx, env, _ := linkReauthEnv(t, true)

	_, err := env.svc.CreateDiscordLink(
		audit.WithImpersonator(sessionCtx(ctx, env.user, time.Now()), "admin-uid"),
		env.org.Slug, env.user, "", new(linkTestPassword))
	r.ErrorIs(err, ErrImpersonationForbidden)
	r.Zero(linkTokenCount(t, ctx, env))
}

// TestDiscordLinkStartFailsClosedWithoutReauthenticator: a service wired
// without the re-authenticator links nothing.
func TestDiscordLinkStartFailsClosedWithoutReauthenticator(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx, env := setupContactEnv(t, WithServerBaseURL("https://solidping.example"), WithFactorReauthenticator(nil))

	_, err := env.svc.CreateDiscordLink(ctx, env.org.Slug, env.user, "", nil)
	r.ErrorIs(err, ErrReauthUnavailable)
	r.Zero(linkTokenCount(t, ctx, env))
}
