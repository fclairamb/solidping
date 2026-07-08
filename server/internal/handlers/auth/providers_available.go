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
	cfg            *config.Config
	passkeyEnabled func() bool
}

// NewProvidersHandler creates a new providers handler. When passkeyEnabled
// is non-nil it's queried each request — supports the case where WebAuthn
// is configured but disabled at runtime (e.g. base URL is not https).
func NewProvidersHandler(cfg *config.Config, passkeyEnabled func() bool) *ProvidersHandler {
	if passkeyEnabled == nil {
		passkeyEnabled = func() bool { return false }
	}

	return &ProvidersHandler{
		HandlerBase:    base.NewHandlerBase(cfg),
		cfg:            cfg,
		passkeyEnabled: passkeyEnabled,
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
	PasskeysEnabled     bool           `json:"passkeysEnabled"`
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

	if h.cfg.OIDC.Enabled && h.cfg.OIDC.IssuerURL != "" &&
		h.cfg.OIDC.ClientID != "" && h.cfg.OIDC.ClientSecret != "" {
		name := h.cfg.OIDC.DisplayName
		if name == "" {
			name = "SSO"
		}

		providers = append(providers, ProviderInfo{
			Name: name,
			Type: "oidc",
		})
	}

	return h.WriteJSON(writer, http.StatusOK, ProvidersResponse{
		Data:                providers,
		RegistrationEnabled: h.cfg.Auth.RegistrationEmailPattern != "",
		PasskeysEnabled:     h.passkeyEnabled(),
	})
}
