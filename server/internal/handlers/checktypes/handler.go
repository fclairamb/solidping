package checktypes

import (
	"net/http"

	"github.com/uptrace/bunrouter"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/handlers/base"
)

// Handler provides HTTP handlers for check type endpoints.
type Handler struct {
	base.HandlerBase
	svc *Service
}

// NewHandler creates a new check types handler.
func NewHandler(service *Service, cfg *config.Config) *Handler {
	return &Handler{
		HandlerBase: base.NewHandlerBase(cfg),
		svc:         service,
	}
}

// ListServerCheckTypes handles GET /api/v1/check-types (public, shows server-level status).
func (h *Handler) ListServerCheckTypes(writer http.ResponseWriter, _ bunrouter.Request) error {
	response := h.svc.ListServerCheckTypes()

	return h.WriteJSON(writer, http.StatusOK, response)
}

// ListOrgCheckTypes handles GET /api/v1/orgs/:org/check-types (auth required, org-resolved).
func (h *Handler) ListOrgCheckTypes(writer http.ResponseWriter, req bunrouter.Request) error {
	// For now, no per-org disabled types are loaded from DB.
	// This will be wired to the parameters table in a follow-up.
	_ = req.Param("org")

	response := h.svc.ListOrgCheckTypes(nil)

	return h.WriteJSON(writer, http.StatusOK, response)
}
