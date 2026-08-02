// Package publicconfig serves GET /api/v1/config: the unauthenticated,
// browser-safe configuration blob the dashboard reads at boot, before any
// login has happened.
//
// It is deliberately a *general-purpose* document rather than a
// PostHog-specific endpoint — future public feature flags join the same JSON
// object instead of minting an endpoint each.
//
// Hard rule: nothing marked Secret in the system-parameter registry may ever
// appear here, and nothing that is off may leak its configuration. In
// particular posthog.personal_api_key is never read by this package, and when
// PostHog is inactive the projectApiKey/host fields are OMITTED entirely
// (json omitempty on pointer/empty values), not emitted as empty strings — a
// self-hosted operator must be able to see at a glance that the feature is
// wholly unconfigured.
package publicconfig

import (
	"net/http"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/handlers/base"
)

// PostHogPublicConfig is the browser-safe view of the PostHog settings.
//
// Enabled is the resolved enablement rule (config.PostHogConfig.Active), NOT
// the raw posthog.enabled kill switch: a deployment with the switch on but no
// project key reports enabled=false, because nothing will in fact be captured.
// ProjectAPIKey and Host are present if and only if Enabled is true.
type PostHogPublicConfig struct {
	Enabled bool `json:"enabled"`
	// ProjectAPIKey is the public phc_… browser key. Absent when disabled.
	ProjectAPIKey string `json:"projectApiKey,omitempty"`
	// Host is the ingestion endpoint. Absent when disabled.
	Host string `json:"host,omitempty"`
}

// Response is the public config document. Fields are added here as new public
// flags appear; every one of them must be non-secret and browser-safe.
type Response struct {
	PostHog PostHogPublicConfig `json:"posthog"`
}

// Handler serves the public config document.
type Handler struct {
	base.HandlerBase
	cfg *config.Config
}

// NewHandler creates a new public config handler.
func NewHandler(cfg *config.Config) *Handler {
	return &Handler{
		HandlerBase: base.NewHandlerBase(cfg),
		cfg:         cfg,
	}
}

// Build assembles the public config document from the (already overlaid)
// server config. Exported so tests can assert the shape without an HTTP round
// trip, and so future callers can embed the same document elsewhere.
func Build(cfg *config.Config) Response {
	resp := Response{}

	if cfg != nil && cfg.PostHog.Active() {
		resp.PostHog = PostHogPublicConfig{
			Enabled:       true,
			ProjectAPIKey: cfg.PostHog.ProjectAPIKey,
			Host:          cfg.PostHog.ResolvedHost(),
		}
	}

	return resp
}

// GetConfig handles GET /api/v1/config (public, no authentication).
func (h *Handler) GetConfig(writer http.ResponseWriter, _ *http.Request) error {
	return h.WriteJSON(writer, http.StatusOK, Build(h.cfg))
}
