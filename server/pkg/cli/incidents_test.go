package cli

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/pkg/client"
)

// TestGetIncidentWithResponseNilParams pins the exact call shape used by
// `sp incidents get` (pkg/cli/incidents.go): the generated
// GetIncidentWithResponse gained a *GetIncidentParams argument when the
// "with" query parameter was added to the getIncident operation, and this
// call site passes nil. That's only correct behavior as long as nil sends
// no query string (server-side "with" defaults to neither check nor
// members) — if a future codegen or client change ever made nil imply a
// default query, this call site would silently start requesting extra data.
func TestGetIncidentWithResponseNilParams(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	incidentUID := uuid.New()

	var gotPath, gotQuery string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		gotPath = req.URL.Path
		gotQuery = req.URL.RawQuery

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{
			"uid": "` + incidentUID.String() + `",
			"state": "active",
			"checkSlug": "google",
			"checkName": "Google"
		}`))
	}))
	defer srv.Close()

	apiClient, err := client.NewClientWithResponses(srv.URL)
	r.NoError(err)

	resp, err := apiClient.GetIncidentWithResponse(t.Context(), "default", incidentUID, nil)
	r.NoError(err)
	r.Equal(http.StatusOK, resp.StatusCode())
	r.NotNil(resp.JSON200)

	r.Equal("/api/v1/orgs/default/incidents/"+incidentUID.String(), gotPath)
	r.Empty(gotQuery, "nil params must not add a query string (preserves pre-existing CLI behavior)")

	r.NotNil(resp.JSON200.CheckSlug)
	r.Equal("google", *resp.JSON200.CheckSlug)
	r.NotNil(resp.JSON200.CheckName)
	r.Equal("Google", *resp.JSON200.CheckName)
	// The opt-in "check" and members fields are never requested by this
	// call site — confirming they stay unset guards against a future
	// change silently starting to ask for them via a non-nil params value.
	r.Nil(resp.JSON200.Check)
}
