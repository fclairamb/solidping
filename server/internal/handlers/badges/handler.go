// Package badges provides HTTP handlers for serving badge images.
package badges

import (
	"errors"
	"net/http"
	"strconv"

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
func (h *Handler) GetBadge(writer http.ResponseWriter, req bunrouter.Request) error {
	orgSlug := req.Param("org")
	checkIdentifier := req.Param("check")
	components := req.Param("components")

	opts := BadgeOptions{
		Period: req.URL.Query().Get("period"),
		Label:  req.URL.Query().Get("label"),
		Style:  req.URL.Query().Get("style"),
	}

	if minWidth, ok := parseIntParam(req.URL.Query().Get("minWidth"), 0, 800); ok {
		opts.MinWidth = minWidth
	}

	svg, err := h.svc.GenerateBadge(req.Context(), orgSlug, checkIdentifier, components, opts)
	if err != nil {
		return h.handleError(writer, err)
	}

	writer.Header().Set("Content-Type", "image/svg+xml")
	writer.Header().Set("Cache-Control", "public, max-age=60")
	writer.WriteHeader(http.StatusOK)
	_, _ = writer.Write([]byte(svg))

	return nil
}

// GetUptimeBar handles GET requests for uptime bar SVG images.
func (h *Handler) GetUptimeBar(writer http.ResponseWriter, req bunrouter.Request) error {
	orgSlug := req.Param("org")
	checkIdentifier := req.Param("check")

	opts := UptimeBarOptions{
		Period: req.URL.Query().Get("period"),
		Style:  req.URL.Query().Get("style"),
		Width:  300,
		Height: 20,
	}

	if barWidth, ok := parseIntParam(req.URL.Query().Get("width"), 60, 800); ok {
		opts.Width = barWidth
	}

	if barHeight, ok := parseIntParam(req.URL.Query().Get("height"), 10, 40); ok {
		opts.Height = barHeight
	}

	svg, err := h.svc.GenerateUptimeBar(req.Context(), orgSlug, checkIdentifier, opts)
	if err != nil {
		return h.handleError(writer, err)
	}

	writer.Header().Set("Content-Type", "image/svg+xml")
	writer.Header().Set("Cache-Control", "public, max-age=300")
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
