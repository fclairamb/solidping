package availability

import (
	"errors"
	"net/http"
	"strings"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/handlers/base"
	"github.com/fclairamb/solidping/server/internal/httpx"
)

// Handler handles HTTP requests for the per-check availability endpoint.
type Handler struct {
	base.HandlerBase
	svc *Service
}

// NewHandler creates a new availability handler.
func NewHandler(service *Service, cfg *config.Config) *Handler {
	return &Handler{
		HandlerBase: base.NewHandlerBase(cfg),
		svc:         service,
	}
}

// GetAvailability handles GET /api/v1/orgs/:org/checks/:check/availability.
func (h *Handler) GetAvailability(writer http.ResponseWriter, req *http.Request) error {
	orgSlug := httpx.Param(req, "org")
	checkIdent := httpx.Param(req, "check")

	query := req.URL.Query()

	opts := &GetAvailabilityOptions{TZ: query.Get("tz")}
	if periodsParam := query.Get("periods"); periodsParam != "" {
		opts.Periods = strings.Split(periodsParam, ",")
	}

	resp, err := h.svc.GetAvailability(req.Context(), orgSlug, checkIdent, opts)
	if err != nil {
		switch {
		case errors.Is(err, ErrOrganizationNotFound):
			return h.WriteErrorErr(
				writer, req, http.StatusNotFound, base.ErrorCodeOrganizationNotFound, "Organization not found", err)
		case errors.Is(err, ErrCheckNotFound):
			return h.WriteErrorErr(
				writer, req, http.StatusNotFound, base.ErrorCodeCheckNotFound, "Check not found", err)
		case errors.Is(err, ErrInvalidPeriod):
			return h.WriteErrorErr(
				writer, req, http.StatusBadRequest, base.ErrorCodeValidationError, "Invalid period parameter", err)
		default:
			return h.WriteInternalError(writer, req, err)
		}
	}

	return h.WriteJSON(writer, http.StatusOK, resp)
}
