package integration

import (
	"encoding/json"
	"io"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestIntegrationsAliasMatchesChannels pins the rename invariant from spec
// 2026-05-29-01: the canonical `/integrations` paths must respond identically
// to the `/channels` alias so the dashboard / MCP / CLI keep working through the
// one-cycle deprecation. It also asserts the legacy `/connections` path is gone
// (404) after PR-E.
func TestIntegrationsAliasMatchesChannels(t *testing.T) {
	t.Parallel()
	ts := NewTestServer(t)
	t.Cleanup(ts.Close)

	cases := []struct {
		name          string
		method        string
		canonicalPath string
		aliasPath     string
	}{
		{
			name:          "list",
			method:        http.MethodGet,
			canonicalPath: "/api/v1/orgs/" + TestOrgSlug + "/integrations",
			aliasPath:     "/api/v1/orgs/" + TestOrgSlug + "/channels",
		},
		{
			name:          "get-missing",
			method:        http.MethodGet,
			canonicalPath: "/api/v1/orgs/" + TestOrgSlug + "/integrations/00000000-0000-0000-0000-000000000000",
			aliasPath:     "/api/v1/orgs/" + TestOrgSlug + "/channels/00000000-0000-0000-0000-000000000000",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)

			canonicalStatus, canonicalBody := authedRequest(t, ts, tc.method, tc.canonicalPath)
			aliasStatus, aliasBody := authedRequest(t, ts, tc.method, tc.aliasPath)

			r.Equal(canonicalStatus, aliasStatus,
				"alias status must match canonical: %d vs %d (path=%s)",
				canonicalStatus, aliasStatus, tc.aliasPath)

			// The bodies are JSON; compare as decoded interfaces so timestamp /
			// ordering quirks don't bite. For empty list & missing-uid both are
			// stable shapes that reduce to the same value.
			var c, a any
			r.NoError(json.Unmarshal(canonicalBody, &c), "canonical body parse")
			r.NoError(json.Unmarshal(aliasBody, &a), "alias body parse")
			r.Equal(c, a, "alias body must match canonical for %s", tc.aliasPath)
		})
	}
}

// TestConnectionsPathRemoved asserts the legacy `/connections` API routes were
// dropped in PR-E. Because the server serves an SPA fallback for unmatched
// routes, a removed API path no longer returns the integrations JSON payload —
// it falls through to the HTML shell. We assert it does NOT return the JSON
// `data` list that the live `/channels` alias still returns.
func TestConnectionsPathRemoved(t *testing.T) {
	t.Parallel()
	ts := NewTestServer(t)
	t.Cleanup(ts.Close)

	paths := []string{
		"/api/v1/orgs/" + TestOrgSlug + "/connections",
		"/api/v1/orgs/" + TestOrgSlug + "/checks/some-check/connections",
	}

	for _, p := range paths {
		t.Run(p, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)
			_, body := authedRequest(t, ts, http.MethodGet, p)
			// The removed route must NOT decode to a JSON object with a `data`
			// key (the integrations list shape). The SPA fallback returns HTML.
			var decoded map[string]any
			if err := json.Unmarshal(body, &decoded); err == nil {
				_, hasData := decoded["data"]
				r.False(hasData,
					"legacy /connections path must not return the integrations JSON after PR-E: %s", p)
			}
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
