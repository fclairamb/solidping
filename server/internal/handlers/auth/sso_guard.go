package auth

import (
	"net/http"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/handlers/base"
	"github.com/fclairamb/solidping/server/internal/httpx"
)

// RequireSSOProvider guards the always-mounted /auth/<provider> routes (spec
// 2026-11-25-02): each request reads the live SSO snapshot and answers 404
// while the provider is not configured, so a provider saved on Server
// Settings > Authentication works without a restart, and one switched off
// stops at once.
func RequireSSOProvider(cfg *config.Config, provider string) httpx.Middleware {
	handlerBase := base.NewHandlerBase(cfg)

	return func(next httpx.HandlerFunc) httpx.HandlerFunc {
		return func(writer http.ResponseWriter, req *http.Request) error {
			if !cfg.SSO().RoutesActive(provider) {
				return handlerBase.WriteError(writer, http.StatusNotFound, base.ErrorCodeNotFound,
					"Sign-in provider "+provider+" is not configured")
			}

			return next(writer, req)
		}
	}
}
