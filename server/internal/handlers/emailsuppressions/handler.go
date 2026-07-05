package emailsuppressions

import (
	"errors"
	"net/http"

	"github.com/uptrace/bunrouter"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/handlers/base"
)

// Handler exposes the org-scoped suppression list/delete API.
type Handler struct {
	base.HandlerBase
	svc *Service
}

// NewHandler builds a Handler.
func NewHandler(svc *Service, cfg *config.Config) *Handler {
	return &Handler{
		HandlerBase: base.NewHandlerBase(cfg),
		svc:         svc,
	}
}

// ListSuppressions handles GET /api/v1/orgs/:org/email-suppressions.
func (h *Handler) ListSuppressions(writer http.ResponseWriter, req bunrouter.Request) error {
	orgSlug := req.Param("org")

	rows, err := h.svc.List(req.Context(), orgSlug)
	if err != nil {
		return h.handleError(writer, err)
	}

	return h.WriteJSON(writer, http.StatusOK, map[string]any{"data": rows})
}

// DeleteSuppression handles DELETE /api/v1/orgs/:org/email-suppressions/:uid
// — the dashboard's re-subscribe action.
func (h *Handler) DeleteSuppression(writer http.ResponseWriter, req bunrouter.Request) error {
	orgSlug := req.Param("org")
	uid := req.Param("uid")

	if err := h.svc.Delete(req.Context(), orgSlug, uid); err != nil {
		return h.handleError(writer, err)
	}

	writer.WriteHeader(http.StatusNoContent)

	return nil
}

func (h *Handler) handleError(writer http.ResponseWriter, err error) error {
	switch {
	case errors.Is(err, ErrOrganizationNotFound):
		return h.WriteErrorErr(
			writer, http.StatusNotFound, base.ErrorCodeOrganizationNotFound,
			"Organization not found", err)
	case errors.Is(err, ErrSuppressionNotFound):
		return h.WriteErrorErr(
			writer, http.StatusNotFound, base.ErrorCodeNotFound,
			"Email suppression not found", err)
	default:
		return h.WriteInternalError(writer, err)
	}
}
