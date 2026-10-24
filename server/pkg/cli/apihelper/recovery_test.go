package apihelper

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/pkg/cli/config"
)

// jwtExpiring builds an unsigned JWT whose exp claim is at.
func jwtExpiring(t *testing.T, at time.Time) string {
	t.Helper()

	enc := base64.RawURLEncoding
	payload, err := json.Marshal(map[string]any{"exp": at.Unix(), "sub": "alice"})
	require.NoError(t, err)

	return enc.EncodeToString([]byte(`{"alg":"none","typ":"JWT"}`)) + "." + enc.EncodeToString(payload) + ".sig"
}

// authServer answers login and refresh with the given access token; a nil
// token makes both answer 200 with an empty body (no access token).
func authServer(t *testing.T, access *string, status int) *httptest.Server {
	t.Helper()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)

		if access == nil {
			_, _ = w.Write([]byte(`{}`))

			return
		}

		_, _ = w.Write([]byte(`{"accessToken":"` + *access + `","expiresIn":3600}`))
	}))
	t.Cleanup(srv.Close)

	return srv
}

func writeTokens(t *testing.T, helper *Helper, data *TokenData) {
	t.Helper()
	require.NoError(t, helper.saveTokenFile(data))
}

func TestParseJWTExpirationRejectsMalformedTokens(t *testing.T) {
	t.Parallel()

	enc := base64.RawURLEncoding
	header := enc.EncodeToString([]byte(`{"alg":"none"}`))

	for name, token := range map[string]string{
		"not a jwt":       "garbage",
		"no exp claim":    header + "." + enc.EncodeToString([]byte(`{"sub":"a"}`)) + ".sig",
		"exp is a string": header + "." + enc.EncodeToString([]byte(`{"exp":"soon"}`)) + ".sig",
	} {
		_, err := parseJWTExpiration(token)
		require.Error(t, err, name)
	}
}

func TestResolveTokenRefreshesAnExpiredAccessToken(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	fresh := jwtExpiring(t, time.Now().Add(time.Hour))
	refresh := jwtExpiring(t, time.Now().Add(2*time.Hour))
	srv := authServer(t, &fresh, http.StatusOK)

	helper := NewHelper(&config.Config{URL: srv.URL, Org: "acme"}, filepath.Join(t.TempDir(), "token.json"), false)
	writeTokens(t, helper, &TokenData{
		AccessToken:           "stale",
		AccessTokenExpiresAt:  time.Now().Add(-time.Hour),
		RefreshToken:          refresh,
		RefreshTokenExpiresAt: time.Now().Add(time.Hour),
	})

	token, err := helper.resolveToken(t.Context())
	r.NoError(err)
	r.Equal(fresh, token)

	saved, err := helper.readTokenFile()
	r.NoError(err)
	r.Equal(fresh, saved.AccessToken)
	r.Equal(refresh, saved.RefreshToken, "the refresh grant is kept")

	// Recovery takes the same road: the stored refresh grant is still valid.
	recovered, err := helper.TryAuthRecovery(t.Context())
	r.NoError(err)
	r.Equal(fresh, recovered)
}

func TestRefreshAccessTokenFailures(t *testing.T) {
	t.Parallel()

	valid := &TokenData{RefreshToken: "rt", RefreshTokenExpiresAt: time.Now().Add(time.Hour)}

	t.Run("expired refresh token", func(t *testing.T) {
		t.Parallel()

		helper := NewHelper(&config.Config{URL: "http://127.0.0.1:1"}, filepath.Join(t.TempDir(), "t.json"), false)
		_, err := helper.refreshAccessToken(t.Context(), &TokenData{RefreshToken: "rt"})
		require.ErrorIs(t, err, ErrRefreshTokenInvalid)
	})

	t.Run("server refuses", func(t *testing.T) {
		t.Parallel()

		srv := authServer(t, nil, http.StatusUnauthorized)
		helper := NewHelper(&config.Config{URL: srv.URL}, filepath.Join(t.TempDir(), "t.json"), false)
		_, err := helper.refreshAccessToken(t.Context(), valid)
		require.Error(t, err)
	})

	t.Run("answer without an access token", func(t *testing.T) {
		t.Parallel()

		srv := authServer(t, nil, http.StatusOK)
		helper := NewHelper(&config.Config{URL: srv.URL}, filepath.Join(t.TempDir(), "t.json"), false)
		_, err := helper.refreshAccessToken(t.Context(), valid)
		require.Error(t, err)
	})

	t.Run("answer with an unparsable token", func(t *testing.T) {
		t.Parallel()

		bad := "not-a-jwt"
		srv := authServer(t, &bad, http.StatusOK)
		helper := NewHelper(&config.Config{URL: srv.URL}, filepath.Join(t.TempDir(), "t.json"), false)
		_, err := helper.refreshAccessToken(t.Context(), valid)
		require.Error(t, err)
	})

	t.Run("token file cannot be written", func(t *testing.T) {
		t.Parallel()

		fresh := jwtExpiring(t, time.Now().Add(time.Hour))
		srv := authServer(t, &fresh, http.StatusOK)
		blocker := filepath.Join(t.TempDir(), "file")
		require.NoError(t, os.WriteFile(blocker, nil, 0o600))

		helper := NewHelper(&config.Config{URL: srv.URL}, filepath.Join(blocker, "sub", "t.json"), false)
		_, err := helper.refreshAccessToken(t.Context(), valid)
		require.Error(t, err)
	})
}

func TestAutoLoginFailures(t *testing.T) {
	t.Parallel()

	creds := config.Auth{Email: "alice@acme.com", Password: "pw"}

	t.Run("server refuses", func(t *testing.T) {
		t.Parallel()

		srv := authServer(t, nil, http.StatusUnauthorized)
		helper := NewHelper(&config.Config{URL: srv.URL, Org: "acme", Auth: creds},
			filepath.Join(t.TempDir(), "t.json"), false)
		_, err := helper.resolveToken(t.Context())
		require.Error(t, err)

		_, err = helper.RenewToken(t.Context(), "x")
		require.Error(t, err)
	})

	t.Run("no access token", func(t *testing.T) {
		t.Parallel()

		srv := authServer(t, nil, http.StatusOK)
		helper := NewHelper(&config.Config{URL: srv.URL, Org: "acme", Auth: creds},
			filepath.Join(t.TempDir(), "t.json"), false)
		_, err := helper.autoLogin(t.Context())
		require.Error(t, err)
	})

	t.Run("unparsable token", func(t *testing.T) {
		t.Parallel()

		bad := "not-a-jwt"
		srv := authServer(t, &bad, http.StatusOK)
		helper := NewHelper(&config.Config{URL: srv.URL, Org: "acme", Auth: creds},
			filepath.Join(t.TempDir(), "t.json"), false)
		_, err := helper.autoLogin(t.Context())
		require.Error(t, err)
	})

	t.Run("token file cannot be written", func(t *testing.T) {
		t.Parallel()

		fresh := jwtExpiring(t, time.Now().Add(time.Hour))
		srv := authServer(t, &fresh, http.StatusOK)
		blocker := filepath.Join(t.TempDir(), "file")
		require.NoError(t, os.WriteFile(blocker, nil, 0o600))

		helper := NewHelper(&config.Config{URL: srv.URL, Org: "acme", Auth: creds},
			filepath.Join(blocker, "sub", "t.json"), false)
		_, err := helper.autoLogin(t.Context())
		require.Error(t, err)
	})

	t.Run("recovery logs in again when the refresh grant is gone", func(t *testing.T) {
		t.Parallel()

		fresh := jwtExpiring(t, time.Now().Add(time.Hour))
		srv := authServer(t, &fresh, http.StatusOK)
		helper := NewHelper(&config.Config{URL: srv.URL, Org: "acme", Auth: creds},
			filepath.Join(t.TempDir(), "t.json"), false)

		token, err := helper.TryAuthRecovery(t.Context())
		require.NoError(t, err)
		require.Equal(t, fresh, token)
	})
}

func TestResolveTokenFallsThroughAFailedRefreshToThePAT(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	srv := authServer(t, nil, http.StatusUnauthorized)
	helper := NewHelper(&config.Config{URL: srv.URL, Auth: config.Auth{PAT: "pat_cfg"}},
		filepath.Join(t.TempDir(), "token.json"), false)
	writeTokens(t, helper, &TokenData{
		AccessTokenExpiresAt:  time.Now().Add(-time.Hour),
		RefreshToken:          "rt",
		RefreshTokenExpiresAt: time.Now().Add(time.Hour),
	})

	token, err := helper.resolveToken(t.Context())
	r.NoError(err)
	r.Equal("pat_cfg", token)

	// The same dead refresh grant also fails the renewal path.
	_, err = helper.RenewToken(t.Context(), "pat_cfg")
	r.ErrorIs(err, ErrNoAuthentication)
}

func TestRenewTokenHandsBackAValidAccessToken(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	helper := NewHelper(&config.Config{URL: "http://127.0.0.1:1"}, filepath.Join(t.TempDir(), "token.json"), false)
	writeTokens(t, helper, &TokenData{AccessToken: "newer", AccessTokenExpiresAt: time.Now().Add(time.Hour)})

	token, err := helper.RenewToken(t.Context(), "older")
	r.NoError(err)
	r.Equal("newer", token)
}
