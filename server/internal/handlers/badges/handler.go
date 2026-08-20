// Package badges provides HTTP handlers for serving badge images.
package badges

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/handlers/base"
	"github.com/fclairamb/solidping/server/internal/httpx"
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

// parseIntParam parses an integer query parameter, clamping it within [min, max].
// Returns 0 if the parameter is empty or invalid.
func parseIntParam(raw string, minVal, maxVal int) (int, bool) {
	if raw == "" {
		return 0, false
	}

	parsed, err := strconv.Atoi(raw)
	if err != nil {
		return 0, false
	}

	if parsed < minVal {
		parsed = minVal
	}

	if parsed > maxVal {
		parsed = maxVal
	}

	return parsed, true
}

// GetBadge handles GET requests for badge images.
func (h *Handler) GetBadge(writer http.ResponseWriter, req *http.Request) error {
	orgSlug := httpx.Param(req, "org")
	checkIdentifier := httpx.Param(req, "check")
	components := httpx.Param(req, "components")

	opts := BadgeOptions{
		Period: req.URL.Query().Get("period"),
		Label:  req.URL.Query().Get("label"),
		Style:  req.URL.Query().Get("style"),
	}

	if minWidth, ok := parseIntParam(req.URL.Query().Get("minWidth"), 0, 800); ok {
		opts.MinWidth = minWidth
	}

	if width, ok := parseIntParam(req.URL.Query().Get("width"), 60, 800); ok {
		opts.Width = width
	}

	svg, err := h.svc.GenerateBadge(req.Context(), orgSlug, checkIdentifier, components, opts)
	if err != nil {
		return h.handleError(writer, req, err)
	}

	writer.Header().Set("Content-Type", "image/svg+xml")
	writer.Header().Set("Cache-Control", "public, max-age=60")
	writer.WriteHeader(http.StatusOK)
	_, _ = writer.Write([]byte(svg))

	return nil
}

func (h *Handler) handleError(writer http.ResponseWriter, request *http.Request, err error) error {
	switch {
	case errors.Is(err, ErrCheckNotFound):
		return h.WriteError(writer, http.StatusNotFound, base.ErrorCodeCheckNotFound, "Check not found")
	case errors.Is(err, ErrInvalidFormat):
		return h.WriteError(writer, http.StatusBadRequest, base.ErrorCodeValidationError, "Invalid badge format")
	case errors.Is(err, ErrOrganizationNotFound):
		return h.WriteError(writer, http.StatusNotFound, base.ErrorCodeOrganizationNotFound, "Organization not found")
	default:
		return h.WriteInternalError(writer, request, err)
	}
}
