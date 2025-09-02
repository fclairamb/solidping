// Package incidents provides incident management HTTP handlers.
package incidents

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/uptrace/bunrouter"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/handlers/base"
)

// Handler handles HTTP requests for incidents.
type Handler struct {
	base.HandlerBase
	svc *Service
}

// NewHandler creates a new incidents handler.
func NewHandler(service *Service, cfg *config.Config) *Handler {
	return &Handler{
		HandlerBase: base.NewHandlerBase(cfg),
		svc:         service,
	}
}

// ListIncidents handles GET /api/v1/orgs/:org/incidents.
func (h *Handler) ListIncidents(writer http.ResponseWriter, req bunrouter.Request) error {
	orgSlug := req.Param("org")

	query := req.URL.Query()

	// Parse query parameters
	opts := ListIncidentsOptions{
		Size: 20, // Default size
	}

	// Parse checkUid filter (comma-separated)
	if checkParam := query.Get("checkUid"); checkParam != "" {
		opts.CheckUIDs = strings.Split(checkParam, ",")
	}

	// Parse state filter (comma-separated)
	if stateParam := query.Get("state"); stateParam != "" {
		opts.States = strings.Split(stateParam, ",")
	}

	// Parse since - RFC3339 timestamp
	sinceTime, parseErr := parseRFC3339(query.Get("since"))
	if parseErr != nil {
		return h.WriteError(writer, http.StatusBadRequest, base.ErrorCodeValidationError,
			"Invalid since: must be RFC3339")
	}
	opts.Since = sinceTime

	// Parse until - RFC3339 timestamp
	untilTime, parseErr := parseRFC3339(query.Get("until"))
	if parseErr != nil {
		return h.WriteError(writer, http.StatusBadRequest, base.ErrorCodeValidationError,
			"Invalid until: must be RFC3339")
	}
	opts.Until = untilTime

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

	// Parse with parameter (e.g., ?with=check)
	if withParam := query.Get("with"); withParam != "" {
		for _, w := range strings.Split(withParam, ",") {
			if w == "check" {
				opts.WithCheck = true
			}
		}
	}

	response, err := h.svc.ListIncidents(req.Context(), orgSlug, &opts)
	if err != nil {
		return h.handleError(writer, err)
	}

	return h.WriteJSON(writer, http.StatusOK, response)
}

// GetIncident handles GET /api/v1/orgs/:org/incidents/:uid.
func (h *Handler) GetIncident(writer http.ResponseWriter, req bunrouter.Request) error {
	orgSlug := req.Param("org")
	incidentUID := req.Param("uid")

	// Parse with parameter (e.g., ?with=check)
	opts := &GetIncidentOptions{}
	if withParam := req.URL.Query().Get("with"); withParam != "" {
		for _, w := range strings.Split(withParam, ",") {
			if w == "check" {
				opts.WithCheck = true
			}
		}
	}

	response, err := h.svc.GetIncident(req.Context(), orgSlug, incidentUID, opts)
	if err != nil {
		return h.handleError(writer, err)
	}

	return h.WriteJSON(writer, http.StatusOK, response)
}

// parseRFC3339 parses an RFC3339 timestamp string, returning nil for empty strings.
func parseRFC3339(value string) (*time.Time, error) {
	if value == "" {
		return nil, nil //nolint:nilnil // nil,nil is intentional for absent params
	}

	parsedTime, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return nil, err
	}

	return &parsedTime, nil
}

// handleError translates service errors to HTTP responses.
func (h *Handler) handleError(writer http.ResponseWriter, err error) error {
	switch {
	case errors.Is(err, ErrOrganizationNotFound):
		return h.WriteError(writer, http.StatusNotFound, base.ErrorCodeOrganizationNotFound, "Organization not found")
	case errors.Is(err, ErrIncidentNotFound):
		return h.WriteError(writer, http.StatusNotFound, base.ErrorCodeNotFound, "Incident not found")
	default:
		return h.WriteInternalError(writer, err)
	}
}
