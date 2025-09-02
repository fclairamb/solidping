// Package events provides event listing HTTP handlers.
package events

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/uptrace/bunrouter"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/handlers/base"
)

// Handler handles HTTP requests for events.
type Handler struct {
	base.HandlerBase
	svc *Service
}

// NewHandler creates a new events handler.
func NewHandler(service *Service, cfg *config.Config) *Handler {
	return &Handler{
		HandlerBase: base.NewHandlerBase(cfg),
		svc:         service,
	}
}

// ListEvents handles GET /api/v1/orgs/:org/events.
func (h *Handler) ListEvents(writer http.ResponseWriter, req bunrouter.Request) error {
	orgSlug := req.Param("org")

	query := req.URL.Query()

	// Parse query parameters
	opts := ListEventsOptions{
		Size: 20, // Default size
	}

	// Parse eventType filter (comma-separated)
	if typeParam := query.Get("eventType"); typeParam != "" {
		opts.EventTypes = strings.Split(typeParam, ",")
	}

	// Parse checkUid filter
	if checkUID := query.Get("checkUid"); checkUID != "" {
		opts.CheckUID = &checkUID
	}

	// Parse incidentUid filter
	if incidentUID := query.Get("incidentUid"); incidentUID != "" {
		opts.IncidentUID = &incidentUID
	}

	// Parse cursor
	if cursor := query.Get("cursor"); cursor != "" {
		opts.Cursor = cursor
	}

	// Parse size
	if sizeParam := query.Get("size"); sizeParam != "" {
		size, err := strconv.Atoi(sizeParam)
		if err != nil || size < 1 {
			return h.WriteError(writer, http.StatusBadRequest, base.ErrorCodeValidationError, "Invalid size parameter")
		}
		if size > 100 {
			size = 100
		}
		opts.Size = size
	}

	response, err := h.svc.ListEvents(req.Context(), orgSlug, &opts)
	if err != nil {
		return h.handleError(writer, err)
	}

	return h.WriteJSON(writer, http.StatusOK, response)
}

// ListIncidentEvents handles GET /api/v1/orgs/:org/incidents/:uid/events.
func (h *Handler) ListIncidentEvents(writer http.ResponseWriter, req bunrouter.Request) error {
	orgSlug := req.Param("org")
	incidentUID := req.Param("uid")

	query := req.URL.Query()

	// Parse query parameters
	opts := ListEventsOptions{
		IncidentUID: &incidentUID,
		Size:        20, // Default size
	}

	// Parse cursor
	if cursor := query.Get("cursor"); cursor != "" {
		opts.Cursor = cursor
	}

	// Parse size
	if sizeParam := query.Get("size"); sizeParam != "" {
		size, err := strconv.Atoi(sizeParam)
		if err != nil || size < 1 {
			return h.WriteError(writer, http.StatusBadRequest, base.ErrorCodeValidationError, "Invalid size parameter")
		}
		if size > 100 {
			size = 100
		}
		opts.Size = size
	}

	response, err := h.svc.ListEvents(req.Context(), orgSlug, &opts)
	if err != nil {
		return h.handleError(writer, err)
	}

	return h.WriteJSON(writer, http.StatusOK, response)
}

// ListCheckEvents handles GET /api/v1/orgs/:org/checks/:checkUid/events.
func (h *Handler) ListCheckEvents(writer http.ResponseWriter, req bunrouter.Request) error {
	orgSlug := req.Param("org")
	checkUID := req.Param("checkUid")

	query := req.URL.Query()

	// Parse query parameters
	opts := ListEventsOptions{
		CheckUID: &checkUID,
		Size:     20, // Default size
	}

	// Parse cursor
	if cursor := query.Get("cursor"); cursor != "" {
		opts.Cursor = cursor
	}

	// Parse size
	if sizeParam := query.Get("size"); sizeParam != "" {
		size, err := strconv.Atoi(sizeParam)
		if err != nil || size < 1 {
			return h.WriteError(writer, http.StatusBadRequest, base.ErrorCodeValidationError, "Invalid size parameter")
		}
		if size > 100 {
			size = 100
		}
		opts.Size = size
	}

	response, err := h.svc.ListEvents(req.Context(), orgSlug, &opts)
	if err != nil {
		return h.handleError(writer, err)
	}

	return h.WriteJSON(writer, http.StatusOK, response)
}

// handleError translates service errors to HTTP responses.
func (h *Handler) handleError(writer http.ResponseWriter, err error) error {
	switch {
	case errors.Is(err, ErrOrganizationNotFound):
		return h.WriteError(writer, http.StatusNotFound, base.ErrorCodeOrganizationNotFound, "Organization not found")
	default:
		return h.WriteInternalError(writer, err)
	}
}
