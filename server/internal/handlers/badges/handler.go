// Package badges provides HTTP handlers for serving badge images.
package badges

import (
	"errors"
	"net/http"

	"github.com/uptrace/bunrouter"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/handlers/base"
)

// Handler handles badge HTTP requests.
type Handler struct {
	base.HandlerBase
	svc *Service
}

// NewHandler creates a new badge handler.
func NewHandler(service *Service, cfg *config.Config) *Handler {
	return &Handler{
		HandlerBase: base.NewHandlerBase(cfg),
		svc:         service,
	}
}

// GetBadge handles GET requests for badge images.
func (h *Handler) GetBadge(writer http.ResponseWriter, req bunrouter.Request) error {
	orgSlug := req.Param("org")
	checkIdentifier := req.Param("check")
	format := req.Param("format")

	opts := BadgeOptions{
		Period: req.URL.Query().Get("period"),
		Label:  req.URL.Query().Get("label"),
		Style:  req.URL.Query().Get("style"),
	}

	svg, err := h.svc.GenerateBadge(req.Context(), orgSlug, checkIdentifier, format, opts)
	if err != nil {
		return h.handleError(writer, err)
	}

	writer.Header().Set("Content-Type", "image/svg+xml")
	writer.Header().Set("Cache-Control", "public, max-age=60")
	writer.WriteHeader(http.StatusOK)
	_, _ = writer.Write([]byte(svg))

	return nil
}

func (h *Handler) handleError(writer http.ResponseWriter, err error) error {
	switch {
	case errors.Is(err, ErrCheckNotFound):
		return h.WriteError(writer, http.StatusNotFound, base.ErrorCodeCheckNotFound, "Check not found")
	case errors.Is(err, ErrInvalidFormat):
		return h.WriteError(writer, http.StatusBadRequest, base.ErrorCodeValidationError, "Invalid badge format")
	case errors.Is(err, ErrOrganizationNotFound):
		return h.WriteError(writer, http.StatusNotFound, base.ErrorCodeOrganizationNotFound, "Organization not found")
	default:
		return h.WriteInternalError(writer, err)
	}
}
