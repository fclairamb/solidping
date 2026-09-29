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

// TestDeploymentFieldsMovedToPublicConfig covers spec 2026-09-29-05:
// GET /api/mgmt/version carries build identity only, /api/v1/config carries
// runMode and deploymentMode, and the old /api/v1/features route is gone.
func TestDeploymentFieldsMovedToPublicConfig(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		deploymentMode string
	}{
		{"resolved self-hosted", config.DeploymentModeSelfHosted},
		{"explicit saas", config.DeploymentModeSaaS},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)

			ts := versionRouteServer(t, tt.deploymentMode)

			version := getJSON(t, ts.URL+"/api/mgmt/version")
			r.NotEmpty(version["version"])
			r.NotContains(version, "runMode")
			r.NotContains(version, "deploymentMode")

			cfgBody := getJSON(t, ts.URL+"/api/v1/config")
			r.Equal(tt.deploymentMode, cfgBody["deploymentMode"])
			r.Contains(cfgBody, "runMode")

			resp, err := http.Get(ts.URL + "/api/v1/features") //nolint:noctx // test-only call
			r.NoError(err)
			defer func() { _ = resp.Body.Close() }()
			r.Equal(http.StatusNotFound, resp.StatusCode)
		})
	}
}

func getJSON(t *testing.T, url string) map[string]any {
	t.Helper()

	resp, err := http.Get(url) //nolint:noctx // test-only call
	require.NoError(t, err)

	defer func() { _ = resp.Body.Close() }()

	require.Equal(t, http.StatusOK, resp.StatusCode)

	var body map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))

	return body
}
