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
	"github.com/fclairamb/solidping/server/internal/version"
)

// TestAgentRegistryRoutes drives the registry through the real router (spec
// 2026-11-22-04): /v2/ answers the registry handshake instead of the SPA
// catch-all, an unknown repository is a registry-shaped 404, and `current` on
// an untagged build is MANIFEST_UNKNOWN without any upstream call.
//
//nolint:paralleltest // pins the process-wide version.Version
func TestAgentRegistryRoutes(t *testing.T) {
	r := require.New(t)
	ctx := context.Background()

	previous := version.Version
	version.Version = "dev"
	t.Cleanup(func() { version.Version = previous })

	cfg := &config.Config{}
	cfg.Database.Type = dbTypeSQLiteMemory
	cfg.Auth.JWTSecret = "agent-registry-route-secret"
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

	get := func(path string) (*http.Response, string) {
		req, reqErr := http.NewRequestWithContext(ctx, http.MethodGet, ts.URL+path, nil)
		r.NoError(reqErr)

		resp, doErr := http.DefaultClient.Do(req)
		r.NoError(doErr)
		t.Cleanup(func() { _ = resp.Body.Close() })

		var payload struct {
			Errors []struct {
				Code string `json:"code"`
			} `json:"errors"`
		}

		_ = json.NewDecoder(resp.Body).Decode(&payload)

		code := ""
		if len(payload.Errors) > 0 {
			code = payload.Errors[0].Code
		}

		return resp, code
	}

	for _, path := range []string{"/v2/", "/v2"} {
		resp, _ := get(path)
		r.Equal(http.StatusOK, resp.StatusCode, path)
		r.Equal("registry/2.0", resp.Header.Get("Docker-Distribution-API-Version"), path)
	}

	resp, code := get("/v2/other/manifests/current")
	r.Equal(http.StatusNotFound, resp.StatusCode)
	r.Equal("NAME_UNKNOWN", code)

	resp, code = get("/v2/solidping/manifests/current")
	r.Equal(http.StatusNotFound, resp.StatusCode)
	r.Equal("MANIFEST_UNKNOWN", code)
}
