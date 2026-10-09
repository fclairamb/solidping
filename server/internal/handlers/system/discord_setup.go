package system

import (
	"net/http"

	"github.com/fclairamb/solidping/server/internal/config"
)

// DiscordSetupResponse lists the values an operator pastes into the Discord
// developer portal, computed from the same helpers the login and install flows
// use to build their redirect URIs.
type DiscordSetupResponse struct {
	BaseURL            string `json:"baseUrl"`
	BaseURLIsDefault   bool   `json:"baseUrlIsDefault"`
	LoginRedirectURI   string `json:"loginRedirectUri"`
	InstallRedirectURI string `json:"installRedirectUri"`
	InteractionsURL    string `json:"interactionsUrl"`
}

// DiscordSetupInfo builds the response for a base URL.
func DiscordSetupInfo(baseURL string) DiscordSetupResponse {
	return DiscordSetupResponse{
		BaseURL:            baseURL,
		BaseURLIsDefault:   baseURL == config.DefaultBaseURL,
		LoginRedirectURI:   config.DiscordLoginRedirectURI(baseURL),
		InstallRedirectURI: config.DiscordInstallRedirectURI(baseURL),
		InteractionsURL:    config.DiscordInteractionsURL(baseURL),
	}
}

// DiscordSetup handles GET /api/v1/system/discord-setup. Super-admin only.
func (h *Handler) DiscordSetup(writer http.ResponseWriter, _ *http.Request) error {
	return h.WriteJSON(writer, http.StatusOK, DiscordSetupInfo(h.cfg.Server.BaseURL))
}
