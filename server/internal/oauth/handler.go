package oauth

import (
	"net/http"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/handlers/base"
)

// Handler exposes the OAuth 2.1 authorization-server HTTP surface: discovery
// documents, the authorization and token endpoints, and dynamic client
// registration. It is constructed once and mounted by the app server.
type Handler struct {
	base.HandlerBase
	svc *Service
	cfg *config.Config
}

// NewHandler builds the OAuth handler.
func NewHandler(svc *Service, cfg *config.Config) *Handler {
	return &Handler{
		HandlerBase: base.NewHandlerBase(cfg),
		svc:         svc,
		cfg:         cfg,
	}
}

// metadata resolves the issuer-derived URLs from the current config. Reading it
// per request keeps a runtime base-URL override (system-parameters overlay)
// authoritative.
func (h *Handler) metadata() Metadata {
	return NewMetadata(h.cfg.Server.BaseURL)
}

// writeNoStoreJSON writes a JSON body with caching disabled. OAuth metadata and
// token responses must not be cached by intermediaries (RFC 6749 §5.1).
func (h *Handler) writeNoStoreJSON(w http.ResponseWriter, status int, data any) error {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")

	return h.WriteJSON(w, status, data)
}

// ProtectedResourceMetadata serves the RFC 9728 document. Public, no auth.
func (h *Handler) ProtectedResourceMetadata(w http.ResponseWriter, _ *http.Request) error {
	return h.WriteJSON(w, http.StatusOK, h.metadata().BuildProtectedResourceMetadata())
}

// AuthorizationServerMetadata serves the RFC 8414 document (and its OIDC alias).
// Public, no auth.
func (h *Handler) AuthorizationServerMetadata(w http.ResponseWriter, _ *http.Request) error {
	return h.WriteJSON(w, http.StatusOK, h.metadata().BuildAuthorizationServerMetadata())
}

// JWKS serves the (empty, symmetric-key) JSON Web Key Set. Public, no auth.
func (h *Handler) JWKS(w http.ResponseWriter, _ *http.Request) error {
	return h.WriteJSON(w, http.StatusOK, BuildJWKS())
}
