// Package webpush provides HTTP handlers for the Web Push VAPID public key endpoint.
package webpush

import (
	"net/http"

	"github.com/uptrace/bunrouter"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/handlers/base"
)

// Handler exposes the VAPID public key for Web Push subscription.
type Handler struct {
	base.HandlerBase
	cfg *config.Config
}

// NewHandler creates a new webpush handler.
func NewHandler(cfg *config.Config) *Handler {
	return &Handler{
		HandlerBase: base.NewHandlerBase(cfg),
		cfg:         cfg,
	}
}

// vapidPublicKeyResponse wraps the VAPID public key in the standard data envelope.
type vapidPublicKeyResponse struct {
	Data vapidPublicKeyData `json:"data"`
}

type vapidPublicKeyData struct {
	PublicKey string `json:"publicKey"`
}

// GetVAPIDPublicKey returns the server's VAPID public key.
// GET /api/v1/orgs/:org/webpush/vapid-public-key
func (h *Handler) GetVAPIDPublicKey(w http.ResponseWriter, _ bunrouter.Request) error {
	if h.cfg.WebPush.VAPIDPublicKey == "" {
		return h.WriteError(w, http.StatusNotFound, base.ErrorCodeNotFound, "Web Push not configured")
	}

	return h.WriteJSON(w, http.StatusOK, vapidPublicKeyResponse{
		Data: vapidPublicKeyData{
			PublicKey: h.cfg.WebPush.VAPIDPublicKey,
		},
	})
}
