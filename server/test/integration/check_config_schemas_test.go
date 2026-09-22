package integration

import (
	"encoding/json"
	"io"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestCheckConfigSchemaRoutes exercises the two schema routes through the real
// router, which is what the handler unit tests cannot do: they mount the handlers
// themselves, so a route registered on the wrong group, behind auth, or not at
// all would still leave them green. The whole value of these endpoints is that a
// third party can GET them without credentials.
func TestCheckConfigSchemaRoutes(t *testing.T) {
	t.Parallel()

	server := NewTestServer(t)

	t.Run("one type, unauthenticated", func(t *testing.T) {
		t.Parallel()
		r := require.New(t)

		resp := getNoAuth(t, server.HTTPServer.URL+"/api/v1/checks/schema/http")
		defer func() { _ = resp.Body.Close() }()

		r.Equal(http.StatusOK, resp.StatusCode, "the schema must be readable with no token")
		r.Equal("application/schema+json", resp.Header.Get("Content-Type"))

		body, err := io.ReadAll(resp.Body)
		r.NoError(err)

		var doc map[string]any
		r.NoError(json.Unmarshal(body, &doc))
		r.Equal("http", doc["x-solidping-check-type"])
		r.Contains(doc["description"], "DESCRIPTIVE ONLY")
	})

	t.Run("catalog, unauthenticated", func(t *testing.T) {
		t.Parallel()
		r := require.New(t)

		resp := getNoAuth(t, server.HTTPServer.URL+"/api/v1/checks/schema")
		defer func() { _ = resp.Body.Close() }()

		r.Equal(http.StatusOK, resp.StatusCode)

		var body struct {
			Data []struct {
				CheckType string `json:"checkType"`
				Ref       string `json:"ref"`
			} `json:"data"`
			Note string `json:"note"`
		}

		r.NoError(json.NewDecoder(resp.Body).Decode(&body))
		r.NotEmpty(body.Data)
		r.Contains(body.Note, "not the validator")
	})

	t.Run("unknown type is 404", func(t *testing.T) {
		t.Parallel()
		r := require.New(t)

		resp := getNoAuth(t, server.HTTPServer.URL+"/api/v1/checks/schema/not-a-check-type")
		defer func() { _ = resp.Body.Close() }()

		r.Equal(http.StatusNotFound, resp.StatusCode)
	})

	// The route must not have shadowed the org-scoped check routes it sits next
	// to: /checks/schema is a sibling of /orgs/{org}/checks, not a child.
	t.Run("org check routes still require auth", func(t *testing.T) {
		t.Parallel()
		r := require.New(t)

		resp := getNoAuth(t, server.HTTPServer.URL+"/api/v1/orgs/"+TestOrgSlug+"/checks")
		defer func() { _ = resp.Body.Close() }()

		r.Equal(http.StatusUnauthorized, resp.StatusCode)
	})
}

func getNoAuth(t *testing.T, url string) *http.Response {
	t.Helper()

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, url, nil)
	require.NoError(t, err)

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)

	return resp
}
