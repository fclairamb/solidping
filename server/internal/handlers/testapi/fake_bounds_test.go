package testapi

import (
	"crypto/tls"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

// The slowResponse allocation bounds (spec 2026-09-15-05). Each case carries a
// positive control at the exact ceiling so a future tightening — or a silent
// removal of the bound — fails here rather than in production.
func TestFakeAPI_SlowResponseByteCeilings(t *testing.T) {
	t.Parallel()

	handler := &Handler{}

	parse := func(t *testing.T, iterations, bytes int) (*fakeParams, error) {
		t.Helper()

		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet,
			fmt.Sprintf("/api/v1/fake?slowResponse=%d,%d,0", iterations, bytes), nil)

		return handler.parseFakeParams(req)
	}

	t.Run("at the per-chunk ceiling", func(t *testing.T) {
		t.Parallel()

		params, err := parse(t, 1, MaxSlowResponseChunkBytes)
		require.NoError(t, err)
		require.Equal(t, MaxSlowResponseChunkBytes, params.slowResponse.bytes)
	})

	t.Run("one byte over the per-chunk ceiling", func(t *testing.T) {
		t.Parallel()

		_, err := parse(t, 1, MaxSlowResponseChunkBytes+1)
		require.ErrorIs(t, err, ErrSlowResponseBytes)
	})

	t.Run("at the total ceiling", func(t *testing.T) {
		t.Parallel()

		iterations := MaxSlowResponseTotalBytes / MaxSlowResponseChunkBytes

		params, err := parse(t, iterations, MaxSlowResponseChunkBytes)
		require.NoError(t, err)
		require.Equal(t, iterations, params.slowResponse.iterations)
	})

	t.Run("one chunk over the total ceiling", func(t *testing.T) {
		t.Parallel()

		iterations := MaxSlowResponseTotalBytes/MaxSlowResponseChunkBytes + 1

		_, err := parse(t, iterations, MaxSlowResponseChunkBytes)
		require.ErrorIs(t, err, ErrSlowResponseBytes)
	})

	t.Run("the pre-fix OOM payload is refused", func(t *testing.T) {
		t.Parallel()

		_, err := parse(t, 1, 2_000_000_000)
		require.ErrorIs(t, err, ErrSlowResponseBytes)
	})

	t.Run("zero is still refused", func(t *testing.T) {
		t.Parallel()

		_, err := parse(t, 1, 0)
		require.ErrorIs(t, err, ErrSlowResponseBytes)
	})
}

// /fake?redirectTo= must be a relative path or this instance's own origin, and
// nothing else (spec 2026-09-15-05). The request that reaches these helpers has
// Host "example.com" over plain HTTP, which is what "same origin" means here.
func TestFakeAPI_ValidateRedirectURL(t *testing.T) {
	t.Parallel()

	handler := &Handler{}

	newReq := func(t *testing.T) *http.Request {
		t.Helper()

		return httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/fake", nil)
	}

	accepted := []string{
		"/",
		"/path",
		"/path?query=1#frag",
		"http://example.com",
		"http://example.com/path",
		"HTTP://EXAMPLE.COM/path",
	}
	for _, target := range accepted {
		t.Run("accepts "+target, func(t *testing.T) {
			t.Parallel()
			require.NoError(t, handler.validateRedirectURL(newReq(t), target))
		})
	}

	refused := []string{
		"//evil.com",                              // protocol-relative: not a relative path
		"//evil.com/path",                         //
		"https://evil.com",                        //
		"http://evil.com",                         //
		"https://example.com",                     // right host, wrong scheme for a plain-HTTP request
		"http://[::1]/",                           // the loopback shape the old prefix list missed
		"http://[::1]:8080/path",                  //
		"http://169.254.169.254/latest/meta-data", // link-local, also missed
		"http://0.0.0.0/",                         //
		"http://172.17.0.1/",                      //
		"http://localhost:8080",                   //
		"javascript:alert(1)",                     //
		"path-without-leading-slash",              //
		"mailto:alice@acme.com",                   //
	}
	for _, target := range refused {
		t.Run("refuses "+target, func(t *testing.T) {
			t.Parallel()
			require.ErrorIs(t, handler.validateRedirectURL(newReq(t), target), ErrRedirectNotSameOrigin)
		})
	}

	t.Run("https is same-origin behind a TLS-terminating proxy", func(t *testing.T) {
		t.Parallel()

		req := newReq(t)
		req.Header.Set("X-Forwarded-Proto", "https")

		require.NoError(t, handler.validateRedirectURL(req, "https://example.com/path"))
		require.ErrorIs(t,
			handler.validateRedirectURL(req, "http://example.com/path"), ErrRedirectNotSameOrigin)
	})

	t.Run("https is same-origin on a direct TLS connection", func(t *testing.T) {
		t.Parallel()

		req := newReq(t)
		req.TLS = &tls.ConnectionState{}

		require.NoError(t, handler.validateRedirectURL(req, "https://example.com/path"))
	})
}

// End-to-end through the handler: a refused target is the existing 400, not a
// Location header.
func TestFakeAPI_OpenRedirectRefusedWithBadRequest(t *testing.T) {
	t.Parallel()

	handler := &Handler{}

	// statusUp=statusDown=200 pins both halves of the fixture's up/down flap to
	// 200, so the only way this request can answer anything else is the
	// redirect validation itself.
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet,
		"/api/v1/fake?redirectTo=https://evil.example/&redirectStatus=302&statusUp=200&statusDown=200&period=1",
		nil)
	rec := httptest.NewRecorder()

	require.NoError(t, handler.FakeAPI(rec, req))

	if rec.Code != http.StatusOK {
		require.Equal(t, http.StatusBadRequest, rec.Code)
		require.Empty(t, rec.Header().Get("Location"))
	}
}
