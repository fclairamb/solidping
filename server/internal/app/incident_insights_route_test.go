package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db/models"
)

// TestIncidentInsightsRoute pins that GET …/incidents/insights is
// authenticated and is not swallowed by the /:uid route.
func TestIncidentInsightsRoute(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := context.Background()

	cfg := &config.Config{}
	cfg.Database.Type = dbTypeSQLiteMemory
	cfg.Auth.JWTSecret = "insights-route-secret"
	cfg.Auth.AccessTokenExpiry = time.Hour
	cfg.Auth.RefreshTokenExpiry = 24 * time.Hour
	cfg.FileStorage.Type = "local"
	cfg.FileStorage.LocalRoot = t.TempDir()

	server, err := NewServer(ctx, cfg)
	r.NoError(err)
	t.Cleanup(func() { _ = server.dbService.Close() })

	r.NoError(server.Initialize(ctx))
	r.NoError(server.InitializeSystemConfig(ctx, cfg))
	server.SetupRoutes(ctx)

	ts := httptest.NewServer(server.Handler())
	t.Cleanup(ts.Close)

	org := models.NewOrganization("insights-route-org", "Insights")
	r.NoError(server.dbService.CreateOrganization(ctx, org))

	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		ts.URL+"/api/v1/orgs/"+org.Slug+"/incidents/insights", nil)
	r.NoError(err)

	resp, err := http.DefaultClient.Do(req)
	r.NoError(err)
	_ = resp.Body.Close()
	r.Equal(http.StatusUnauthorized, resp.StatusCode)

	// Authenticated: the route reaches the insights handler (the /:uid route
	// would answer 404 or 400 for the literal "insights").
	now := time.Now()
	user := models.NewUser("insights-viewer@acme.com")
	user.EmailVerifiedAt = &now
	r.NoError(server.dbService.CreateUser(ctx, user))

	member := models.NewOrganizationMember(org.UID, user.UID, models.MemberRoleViewer)
	member.JoinedAt = &now
	r.NoError(server.dbService.CreateOrganizationMember(ctx, member))

	token := mintTestToken(t, server, user.UID, org.Slug, string(models.MemberRoleViewer), false)

	get := func(query string) (int, map[string]any) {
		authReq, reqErr := http.NewRequestWithContext(ctx, http.MethodGet,
			ts.URL+"/api/v1/orgs/"+org.Slug+"/incidents/insights"+query, nil)
		r.NoError(reqErr)
		authReq.Header.Set("Authorization", "Bearer "+token)

		authResp, doErr := http.DefaultClient.Do(authReq)
		r.NoError(doErr)

		defer func() { _ = authResp.Body.Close() }()

		var body map[string]any
		r.NoError(json.NewDecoder(authResp.Body).Decode(&body))

		return authResp.StatusCode, body
	}

	status, body := get("")
	r.Equal(http.StatusOK, status)
	r.Equal("30d", body["period"])
	r.Contains(body, "summary")
	r.Contains(body, "checks")

	status, body = get("?period=1y")
	r.Equal(http.StatusBadRequest, status)
	r.Equal("VALIDATION_ERROR", body["code"])
}
