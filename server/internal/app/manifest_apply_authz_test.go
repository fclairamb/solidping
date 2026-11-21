package app

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

// manifestApplyBody is a manifest with only non-check kinds (spec
// 2026-11-21-01): valid, so a rejection is never about the document.
const manifestApplyBody = `version: 2
organization: viewerguard
statusPages:
  - slug: public-status
    name: Public status
`

func manifestRequest(t *testing.T, env *viewerEnv, method, path, token, body string) (int, []byte) {
	t.Helper()

	req, err := http.NewRequestWithContext(context.Background(), method,
		env.ts.URL+"/api/v1/orgs/"+env.org.Slug+path, bytes.NewReader([]byte(body)))
	require.NoError(t, err)

	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	req.Header.Set("Content-Type", "application/yaml")

	resp, err := env.ts.Client().Do(req)
	require.NoError(t, err)

	defer func() { _ = resp.Body.Close() }()

	raw, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	return resp.StatusCode, raw
}

// TestManifestApplyRoutesAreAdminOnly sweeps the new kind-neutral routes next
// to the historical one: the same admin gate, because apply can delete by
// absence on every kind.
func TestManifestApplyRoutesAreAdminOnly(t *testing.T) {
	t.Parallel()

	env := newViewerEnv(t)
	admin := adminToken(t, env)

	routes := []struct{ method, path string }{
		{http.MethodPost, "/apply?dryRun=true"},
		{http.MethodPost, "/checks/apply?dryRun=true"},
		{http.MethodGet, "/export"},
	}

	for _, route := range routes {
		t.Run(route.method+" "+route.path, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)

			status, _ := manifestRequest(t, env, route.method, route.path, "", manifestApplyBody)
			r.Equal(http.StatusUnauthorized, status, "anonymous")

			status, _ = manifestRequest(t, env, route.method, route.path, env.viewerToken, manifestApplyBody)
			r.Equal(http.StatusForbidden, status, "viewer")

			status, _ = manifestRequest(t, env, route.method, route.path, env.userToken, manifestApplyBody)
			r.Equal(http.StatusForbidden, status, "regular member")

			status, body := manifestRequest(t, env, route.method, route.path, admin, manifestApplyBody)
			r.Equalf(http.StatusOK, status, "admin: %s", body)
		})
	}
}

func TestManifestApplyEndToEnd(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	env := newViewerEnv(t)
	admin := adminToken(t, env)

	status, body := manifestRequest(t, env, http.MethodPost, "/apply", admin, manifestApplyBody)
	r.Equalf(http.StatusOK, status, "%s", body)

	var res struct {
		Created int `json:"created"`
		Plan    []struct {
			Kind   string `json:"kind"`
			Slug   string `json:"slug"`
			Action string `json:"action"`
		} `json:"plan"`
	}
	r.NoError(json.Unmarshal(body, &res))
	r.Equal(1, res.Created)
	r.Len(res.Plan, 1)
	r.Equal("statusPage", res.Plan[0].Kind)

	// The export now carries the page back.
	status, body = manifestRequest(t, env, http.MethodGet, "/export", admin, "")
	r.Equal(http.StatusOK, status)
	r.Contains(string(body), "public-status")

	// An empty manifest is a 400.
	status, _ = manifestRequest(t, env, http.MethodPost, "/apply", admin, "version: 2\norganization: x\n")
	r.Equal(http.StatusBadRequest, status)
}
