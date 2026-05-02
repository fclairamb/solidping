package auth

import (
	"net/http"

	"github.com/uptrace/bunrouter"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/handlers/base"
)

// ProvidersHandler handles the available auth providers endpoint.
type ProvidersHandler struct {
	base.HandlerBase
	cfg *config.Config
}

// NewProvidersHandler creates a new providers handler.
func NewProvidersHandler(cfg *config.Config) *ProvidersHandler {
	return &ProvidersHandler{
		HandlerBase: base.NewHandlerBase(cfg),
		cfg:         cfg,
	}
}

// ProviderInfo represents an available auth provider.
type ProviderInfo struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

// ProvidersResponse is the response for the providers endpoint.
type ProvidersResponse struct {
	Data                []ProviderInfo `json:"data"`
	RegistrationEnabled bool           `json:"registrationEnabled"`
}

// ListProviders returns which auth providers are configured.
//
//nolint:cyclop // Linear sequence of provider checks with the same shape; flatter than splitting up.
func (h *ProvidersHandler) ListProviders(writer http.ResponseWriter, _ bunrouter.Request) error {
	providers := make([]ProviderInfo, 0)

	if h.cfg.Slack.Enabled && h.cfg.Slack.ClientID != "" && h.cfg.Slack.ClientSecret != "" {
		providers = append(providers, ProviderInfo{
			Name: "Slack",
			Type: "slack",
		})
	}

	if h.cfg.Google.Enabled && h.cfg.Google.ClientID != "" && h.cfg.Google.ClientSecret != "" {
		providers = append(providers, ProviderInfo{
			Name: "Google",
			Type: "google",
		})
	}

	if h.cfg.GitHub.Enabled && h.cfg.GitHub.ClientID != "" && h.cfg.GitHub.ClientSecret != "" {
		providers = append(providers, ProviderInfo{
			Name: "GitHub",
			Type: "github",
		})
	}

	if h.cfg.Microsoft.Enabled && h.cfg.Microsoft.ClientID != "" && h.cfg.Microsoft.ClientSecret != "" {
		providers = append(providers, ProviderInfo{
			Name: "Microsoft",
			Type: "microsoft",
		})
	}

	if h.cfg.GitLab.Enabled && h.cfg.GitLab.ClientID != "" && h.cfg.GitLab.ClientSecret != "" {
		providers = append(providers, ProviderInfo{
			Name: "GitLab",
			Type: "gitlab",
		})
	}

	if h.cfg.Discord.Enabled && h.cfg.Discord.ClientID != "" && h.cfg.Discord.ClientSecret != "" {
		providers = append(providers, ProviderInfo{
			Name: "Discord",
			Type: "discord",
		})
	}

	return h.WriteJSON(writer, http.StatusOK, ProvidersResponse{
		Data:                providers,
		RegistrationEnabled: h.cfg.Auth.RegistrationEmailPattern != "",
	})
}
