package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
)

// versionRouteServer boots a real server (real NewServer + real SetupRoutes)
// over an in-memory SQLite DB with the given deployment mode, mirroring
// testModeGateServer in testapi_routes_gate_test.go.
func versionRouteServer(t *testing.T, deploymentMode string) *httptest.Server {
	t.Helper()

	r := require.New(t)
	ctx := context.Background()

	cfg := &config.Config{}
	cfg.Database.Type = dbTypeSQLiteMemory
	cfg.Auth.JWTSecret = "version-route-secret"
	cfg.Deployment.Mode = deploymentMode

	server, err := NewServer(ctx, cfg)
	r.NoError(err)
	t.Cleanup(func() { _ = server.dbService.Close() })

	r.NoError(server.Initialize(ctx))
	r.NoError(server.InitializeSystemConfig(ctx, cfg))
	server.SetupRoutes(ctx)

	ts := httptest.NewServer(server.Handler())
	t.Cleanup(ts.Close)

	return ts
}

// TestGetVersionDeploymentMode covers spec 2026-09-06-01: GET /api/mgmt/version
// must surface the deployment mode so the dashboard can pick the right
// marketing UTM campaign. Config.Validate() (exercised separately in
// TestValidateDeploymentMode, internal/config) resolves an unset mode to
// "self-hosted" before the server ever sees it, so this test feeds the
// handler the two post-validation values it will actually see in practice.
func TestGetVersionDeploymentMode(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		deploymentMode string
		want           string
	}{
		{"resolved self-hosted", config.DeploymentModeSelfHosted, config.DeploymentModeSelfHosted},
		{"explicit saas", config.DeploymentModeSaaS, config.DeploymentModeSaaS},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)

			ts := versionRouteServer(t, tt.deploymentMode)

			resp, err := http.Get(ts.URL + "/api/mgmt/version") //nolint:noctx // test-only call
			r.NoError(err)
			defer func() { _ = resp.Body.Close() }()
			r.Equal(http.StatusOK, resp.StatusCode)

			var body struct {
				Version        string `json:"version"`
				DeploymentMode string `json:"deploymentMode"`
			}
			r.NoError(json.NewDecoder(resp.Body).Decode(&body))
			r.Equal(tt.want, body.DeploymentMode)
		})
	}
}
