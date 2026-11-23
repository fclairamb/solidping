package app

import (
	"context"
	"net/http"

	"github.com/fclairamb/solidping/server/internal/agentregistry"
	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/httpx"
	"github.com/fclairamb/solidping/server/internal/middleware"
	"github.com/fclairamb/solidping/server/internal/version"
)

// agentRegistryRequestsPerMinute is the per-IP budget of the agent image
// registry. One pull is a handful of requests (base, manifest index, one
// manifest per platform, a redirect per layer); a fleet behind one NAT pulls
// a few times per upgrade.
const agentRegistryRequestsPerMinute = 300

// agentRegistryRateLimitConfig derives the registry limiter from the server's
// own, keeping its trusted-proxy setting.
func agentRegistryRateLimitConfig(base config.RateLimitConfig) config.RateLimitConfig {
	return config.RateLimitConfig{
		RequestsPerMinute: agentRegistryRequestsPerMinute,
		Burst:             agentRegistryRequestsPerMinute / 5,
		MaxConcurrent:     10,
		TrustedProxies:    base.TrustedProxies,
	}
}

// registerAgentRegistry mounts the read-only agent image registry at the host
// root (spec 2026-11-22-04): `<server-host>/solidping:current` resolves to the
// image of this server's own version. Registries cannot live under a path
// prefix, so the routes are /v2 and /v2/*. Custom status-page domains never
// reach them (isCustomHostForbidden).
func (s *Server) registerAgentRegistry(ctx context.Context, mainGroup *httpx.Group) {
	registry := agentregistry.New(agentregistry.Config{
		Version: func() string { return version.Get().Version },
	})

	limiter := middleware.NewRateLimiter(agentRegistryRateLimitConfig(s.config.Server.RateLimiting), ctx)
	group := mainGroup.Use(limiter.RateLimitRoute)

	serve := func(writer http.ResponseWriter, req *http.Request) error {
		registry.ServeHTTP(writer, req)

		return nil
	}

	group.GET(agentregistry.PathPrefix, serve)
	group.GET(agentregistry.PathPrefix+"/", serve)
	group.GET(agentregistry.PathPrefix+"/*path", serve)
}
