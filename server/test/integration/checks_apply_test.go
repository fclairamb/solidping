package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

// loginToken logs in against the given org and returns the access token. The
// shared test user is an admin in TestOrgSlug and a plain user in TestOrg2Slug.
func loginToken(t *testing.T, ts *TestServer, org string) string {
	t.Helper()
	r := require.New(t)
	ctx := t.Context()

	apiClient := ts.NewClient()
	resp, err := apiClient.Login(ctx, org, TestUserEmail, TestUserPassword)
	r.NoError(err)
	r.NotNil(resp.AccessToken)

	return *resp.AccessToken
}

// rawRequest issues a raw HTTP request to the test server and returns the
// status code and body. Used for endpoints not covered by the generated client.
func rawRequest(t *testing.T, ts *TestServer, method, path, token, contentType string, body []byte) (int, []byte) {
	t.Helper()
	r := require.New(t)

	var rdr io.Reader
	if body != nil {
		rdr = bytes.NewReader(body)
	}

	req, err := http.NewRequestWithContext(context.Background(), method, ts.HTTPServer.URL+path, rdr)
	r.NoError(err)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}

	resp, err := http.DefaultClient.Do(req)
	r.NoError(err)
	defer func() { _ = resp.Body.Close() }()

	respBody, err := io.ReadAll(resp.Body)
	r.NoError(err)

	return resp.StatusCode, respBody
}

// TestChecksConfigAsCodeAdminGating verifies the export/import/apply endpoints
// are admin-only: an admin (TestOrgSlug) is allowed, a non-admin member
// (TestOrg2Slug) gets 403. This covers the retro-gate of export/import and the
// admin gate on apply in one matrix.
func TestChecksConfigAsCodeAdminGating(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ts := NewTestServer(t)

	adminToken := loginToken(t, ts, TestOrgSlug) // admin in test-org
	userToken := loginToken(t, ts, TestOrg2Slug) // plain user in test-org-2

	manifest := []byte(`{"version":1,"organization":"test-org","checks":[` +
		`{"name":"Apply Web","slug":"apply-web","type":"http","config":{"url":"https://example.com"},"enabled":true}]}`)

	// Non-admin must be rejected (403) on all three.
	for _, tc := range []struct {
		name, method, path string
		body               []byte
	}{
		{"export", http.MethodGet, "/api/v1/orgs/" + TestOrg2Slug + "/checks/export", nil},
		{"import", http.MethodPost, "/api/v1/orgs/" + TestOrg2Slug + "/checks/import", manifest},
		{"apply", http.MethodPost, "/api/v1/orgs/" + TestOrg2Slug + "/checks/apply", manifest},
	} {
		t.Run("forbidden_"+tc.name, func(t *testing.T) {
			t.Parallel()
			code, _ := rawRequest(t, ts, tc.method, tc.path, userToken, "application/json", tc.body)
			require.Equal(t, http.StatusForbidden, code, "non-admin must be forbidden on %s", tc.name)
		})
	}

	// Admin export succeeds.
	code, _ := rawRequest(t, ts, http.MethodGet, "/api/v1/orgs/"+TestOrgSlug+"/checks/export", adminToken, "", nil)
	r.Equal(http.StatusOK, code, "admin export should succeed")

	// Admin apply (dry-run) succeeds and returns a plan creating the check.
	code, body := rawRequest(t, ts, http.MethodPost,
		"/api/v1/orgs/"+TestOrgSlug+"/checks/apply?dryRun=true", adminToken, "application/json", manifest)
	r.Equal(http.StatusOK, code, "admin apply should succeed: %s", string(body))

	var applyResp struct {
		DryRun  bool `json:"dryRun"`
		Created int  `json:"created"`
		Plan    []struct {
			Slug   string `json:"slug"`
			Action string `json:"action"`
		} `json:"plan"`
	}
	r.NoError(json.Unmarshal(body, &applyResp))
	r.True(applyResp.DryRun)
	r.Equal(1, applyResp.Created)
	r.Len(applyResp.Plan, 1)
	r.Equal("apply-web", applyResp.Plan[0].Slug)
	r.Equal("create", applyResp.Plan[0].Action)
}

// TestChecksApplyAcceptsYAML verifies the apply endpoint parses a hand-authored
// YAML manifest (not just the JSON export shape) into the same plan.
func TestChecksApplyAcceptsYAML(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ts := NewTestServer(t)
	adminToken := loginToken(t, ts, TestOrgSlug)

	yamlManifest := []byte(`
version: 1
organization: test-org
checks:
  - name: Apply YAML
    slug: apply-yaml
    type: http
    config:
      url: https://example.com
    enabled: true
`)

	code, body := rawRequest(t, ts, http.MethodPost,
		"/api/v1/orgs/"+TestOrgSlug+"/checks/apply?dryRun=true", adminToken, "application/yaml", yamlManifest)
	r.Equal(http.StatusOK, code, "YAML apply should succeed: %s", string(body))

	var applyResp struct {
		Created int `json:"created"`
		Plan    []struct {
			Slug   string `json:"slug"`
			Action string `json:"action"`
		} `json:"plan"`
	}
	r.NoError(json.Unmarshal(body, &applyResp))
	r.Equal(1, applyResp.Created)
	r.Equal("apply-yaml", applyResp.Plan[0].Slug)
	r.Equal("create", applyResp.Plan[0].Action)
}
