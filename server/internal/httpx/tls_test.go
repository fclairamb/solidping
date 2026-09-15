package httpx_test

import (
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/httpx"
)

func TestIsTLS(t *testing.T) {
	t.Parallel()

	t.Run("nil request", func(t *testing.T) {
		t.Parallel()
		require.False(t, httpx.IsTLS(nil))
	})

	t.Run("plain http", func(t *testing.T) {
		t.Parallel()

		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", http.NoBody)
		require.False(t, httpx.IsTLS(req))
	})

	t.Run("direct TLS", func(t *testing.T) {
		t.Parallel()

		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", http.NoBody)
		req.TLS = &tls.ConnectionState{}
		require.True(t, httpx.IsTLS(req))
	})

	t.Run("forwarded proto https", func(t *testing.T) {
		t.Parallel()

		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", http.NoBody)
		req.Header.Set("X-Forwarded-Proto", "HTTPS")
		require.True(t, httpx.IsTLS(req), "the header comparison must be case-insensitive")
	})

	t.Run("forwarded proto http", func(t *testing.T) {
		t.Parallel()

		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", http.NoBody)
		req.Header.Set("X-Forwarded-Proto", "http")
		require.False(t, httpx.IsTLS(req))
	})
}
