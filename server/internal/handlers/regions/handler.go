package regions

import (
	"net/http"

	"github.com/uptrace/bunrouter"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/handlers/base"
)

// Handler provides HTTP handlers for region endpoints.
type Handler struct {
	base.HandlerBase
	svc *Service
}

// NewHandler creates a new regions handler.
func NewHandler(service *Service, cfg *config.Config) *Handler {
	return &Handler{
		HandlerBase: base.NewHandlerBase(cfg),
		svc:         service,
	}
}

// ListGlobalRegions handles GET /api/v1/regions (public, no auth required).
func (h *Handler) ListGlobalRegions(writer http.ResponseWriter, req bunrouter.Request) error {
	response, err := h.svc.ListGlobalRegions(req.Context())
	if err != nil {
		return h.WriteInternalError(writer, err)
	}

	return h.WriteJSON(writer, http.StatusOK, response)
}

// ListOrgRegions handles GET /api/v1/orgs/:org/regions (auth required).
func (h *Handler) ListOrgRegions(writer http.ResponseWriter, req bunrouter.Request) error {
	orgSlug := req.Param("org")

	response, err := h.svc.ListOrgRegions(req.Context(), orgSlug)
	if err != nil {
		return h.WriteErrorErr(
			writer, http.StatusNotFound, base.ErrorCodeOrganizationNotFound, "Organization not found", err)
	}

	return h.WriteJSON(writer, http.StatusOK, response)
}
