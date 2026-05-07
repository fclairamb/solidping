package integration

import (
	"encoding/json"
	"io"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestChannelsAliasMatchesConnections pins the spec 2026-05-07-03 PR-1
// invariant: the new `/channels` paths must respond identically to the
// legacy `/connections` paths so the dashboard / MCP / CLI can switch
// over without an external client breakage.
func TestChannelsAliasMatchesConnections(t *testing.T) {
	t.Parallel()
	ts := NewTestServer(t)
	t.Cleanup(ts.Close)

	cases := []struct {
		name       string
		method     string
		legacyPath string
		aliasPath  string
	}{
		{
			name:       "list",
			method:     http.MethodGet,
			legacyPath: "/api/v1/orgs/" + TestOrgSlug + "/connections",
			aliasPath:  "/api/v1/orgs/" + TestOrgSlug + "/channels",
		},
		{
			name:       "get-missing",
			method:     http.MethodGet,
			legacyPath: "/api/v1/orgs/" + TestOrgSlug + "/connections/00000000-0000-0000-0000-000000000000",
			aliasPath:  "/api/v1/orgs/" + TestOrgSlug + "/channels/00000000-0000-0000-0000-000000000000",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)

			legacyStatus, legacyBody := authedRequest(t, ts, tc.method, tc.legacyPath)
			aliasStatus, aliasBody := authedRequest(t, ts, tc.method, tc.aliasPath)

			r.Equal(legacyStatus, aliasStatus,
				"alias status must match legacy: %d vs %d (path=%s)",
				legacyStatus, aliasStatus, tc.aliasPath)

			// The bodies are JSON; compare as decoded interfaces so timestamp /
			// ordering quirks don't bite. For empty list & missing-uid both are
			// stable shapes that reduce to the same value.
			var l, a any
			r.NoError(json.Unmarshal(legacyBody, &l), "legacy body parse")
			r.NoError(json.Unmarshal(aliasBody, &a), "alias body parse")
			r.Equal(l, a, "alias body must match legacy for %s", tc.aliasPath)
		})
	}
}

// authedRequest issues a request with the test PAT and returns the status
// and body. Errors fail the test.
func authedRequest(t *testing.T, ts *TestServer, method, path string) (int, []byte) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), method, ts.HTTPServer.URL+path, nil)
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer pat_test")

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return resp.StatusCode, body
}
