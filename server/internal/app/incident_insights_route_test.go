package app

import (
	"context"
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
}
