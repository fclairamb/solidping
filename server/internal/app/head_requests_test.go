package app

import (
	"context"
	"io"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestHeadRequestsAnswerLikeGet verifies external uptime monitors probing with
// HEAD get the same status as GET (never 405) and an empty body.
func TestHeadRequestsAnswerLikeGet(t *testing.T) {
	t.Parallel()

	env := newCompressionEnv(t, true)

	do := func(method, path string) (int, []byte) {
		req, err := http.NewRequestWithContext(context.Background(), method, env.ts.URL+path, nil)
		require.NoError(t, err)
		resp, err := env.client.Do(req)
		require.NoError(t, err)
		defer func() { _ = resp.Body.Close() }()
		body, err := io.ReadAll(resp.Body)
		require.NoError(t, err)

		return resp.StatusCode, body
	}

	for _, path := range []string{"/", "/d/", "/api/mgmt/version", "/api/v1/orgs/test/checks", env.statusPagePath()} {
		getStatus, getBody := do(http.MethodGet, path)
		headStatus, headBody := do(http.MethodHead, path)

		require.NotEqual(t, http.StatusMethodNotAllowed, headStatus, path)
		require.Equal(t, getStatus, headStatus, path)
		require.Empty(t, headBody, path)
		require.NotEmpty(t, getBody, path)
	}

	status, _ := do(http.MethodHead, "/api/v1/orgs/test/checks")
	require.Equal(t, http.StatusUnauthorized, status)

	// A POST-only route has no GET handler of its own, so HEAD answers exactly
	// like GET (the root catch-all), never differently.
	getLogin, _ := do(http.MethodGet, "/api/v1/auth/login")
	headLogin, _ := do(http.MethodHead, "/api/v1/auth/login")
	require.Equal(t, getLogin, headLogin)
}
