package auth

import (
	"context"
	"net/http"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/handlers/base"
	"github.com/fclairamb/solidping/server/internal/systemconfig"
)

// ProvidersHandler handles the available auth providers endpoint.
type ProvidersHandler struct {
	base.HandlerBase
	cfg            *config.Config
	passkeyEnabled func() bool
	localLogin     func(ctx context.Context) bool
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
		localLogin: func(ctx context.Context) bool {
			return systemconfig.ResolveLocalLogin(ctx, nil, cfg).Enabled
		},
	}
}

// SetLocalLoginResolver replaces the env-and-config-only local-login lookup
// with one that also reads the DB parameter (Service.LocalLoginEnabled).
func (h *ProvidersHandler) SetLocalLoginResolver(resolve func(ctx context.Context) bool) {
	if resolve != nil {
		h.localLogin = resolve
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
	// LocalLogin is false while local password login is switched off: the
	// login page then hides the email/password form, the forgot-password
	// and the sign-up links.
	LocalLogin bool `json:"localLogin"`
}

// ListProviders returns which auth providers are configured, and whether the
// email/password form is offered (localLogin, spec 2026-11-21-02).
func (h *ProvidersHandler) ListProviders(writer http.ResponseWriter, req *http.Request) error {
	sso := h.cfg.SSOProviders()
	providers := make([]ProviderInfo, 0, len(sso))

	for i := range sso {
		providers = append(providers, ProviderInfo{Name: sso[i].Name, Type: sso[i].Type})
	}

	localLogin := h.localLogin(req.Context())

	return h.WriteJSON(writer, http.StatusOK, ProvidersResponse{
		Data: providers,
		// Self-registration creates a password account: off with local login.
		RegistrationEnabled: localLogin && h.cfg.Auth.RegistrationEmailPattern != "",
		PasskeysEnabled:     h.passkeyEnabled(),
		LocalLogin:          localLogin,
	})
}
