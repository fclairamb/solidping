package app

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestIncidentInsightsRoute walks the real router: the insights route needs a
// token, and with one it is served by the insights handler rather than by
// the `/:uid` route (which would answer 404 INCIDENT_NOT_FOUND).
func TestIncidentInsightsRoute(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	env := newMCPTestEnv(t)
	path := "/api/v1/orgs/" + mcpE2EOrg + "/incidents/insights"

	anon := env.get(t, path, nil)
	r.Equal(http.StatusUnauthorized, anon.status, string(anon.body))

	authed := env.get(t, path+"?period=7d", map[string]string{"Authorization": "Bearer " + env.login(t)})
	r.Equal(http.StatusOK, authed.status, string(authed.body))
	r.Contains(string(authed.body), `"period":"7d"`)
}
