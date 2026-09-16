package auth

import (
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

// readSetCookie returns the single cookie the recorder captured.
func readSetCookie(t *testing.T, rec *httptest.ResponseRecorder) *http.Cookie {
	t.Helper()

	cookies := (&http.Response{Header: rec.Header()}).Cookies()
	require.Len(t, cookies, 1)

	return cookies[0]
}

// The session cookie must be HttpOnly and SameSite=Lax unconditionally, and
// Secure only when the request actually arrived over TLS — a Secure cookie on
// a plain-HTTP self-hosted instance is silently dropped by the browser.
func TestSetAccessTokenCookieAttributes(t *testing.T) {
	t.Parallel()

	t.Run("plain http", func(t *testing.T) {
		t.Parallel()

		rec := httptest.NewRecorder()
		req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/auth/login", http.NoBody)

		setAccessTokenCookie(rec, req, "tok", 3600)

		cookie := readSetCookie(t, rec)
		require.Equal(t, CookieAuthToken, cookie.Name)
		require.Equal(t, "tok", cookie.Value)
		require.Equal(t, "/", cookie.Path)
		require.Equal(t, 3600, cookie.MaxAge)
		require.True(t, cookie.HttpOnly)
		require.Equal(t, http.SameSiteLaxMode, cookie.SameSite)
		require.False(t, cookie.Secure)
	})

	t.Run("behind a TLS-terminating proxy", func(t *testing.T) {
		t.Parallel()

		rec := httptest.NewRecorder()
		req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/auth/login", http.NoBody)
		req.Header.Set("X-Forwarded-Proto", "https")

		setAccessTokenCookie(rec, req, "tok", 3600)

		require.True(t, readSetCookie(t, rec).Secure)
	})

	t.Run("direct TLS", func(t *testing.T) {
		t.Parallel()

		rec := httptest.NewRecorder()
		req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/auth/login", http.NoBody)
		req.TLS = &tls.ConnectionState{}

		setAccessTokenCookie(rec, req, "tok", 3600)

		require.True(t, readSetCookie(t, rec).Secure)
	})
}

// The clearing cookie must carry the same attributes, or the browser keeps it
// as a distinct cookie and the session one survives the logout.
func TestClearAuthCookieMatchesSetAttributes(t *testing.T) {
	t.Parallel()

	rec := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/auth/logout", http.NoBody)
	req.Header.Set("X-Forwarded-Proto", "https")

	handler := &Handler{}
	handler.clearAuthCookie(rec, req)

	cookie := readSetCookie(t, rec)
	require.Equal(t, CookieAuthToken, cookie.Name)
	require.Empty(t, cookie.Value)
	require.Equal(t, "/", cookie.Path)
	require.Equal(t, -1, cookie.MaxAge)
	require.True(t, cookie.HttpOnly)
	require.True(t, cookie.Secure)
	require.Equal(t, http.SameSiteLaxMode, cookie.SameSite)
}
