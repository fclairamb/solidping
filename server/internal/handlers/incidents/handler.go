// Package incidents provides incident management HTTP handlers.
package incidents

import (
	"errors"
	"net/http"
	"net/url"
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

	opts, parseErr := parseListIncidentsOptions(req.URL.Query())
	if parseErr != nil {
		return h.WriteError(writer, http.StatusBadRequest, base.ErrorCodeValidationError, parseErr.Error())
	}

	response, err := h.svc.ListIncidents(req.Context(), orgSlug, opts)
	if err != nil {
		return h.handleError(writer, err)
	}

	return h.WriteJSON(writer, http.StatusOK, response)
}

// Sentinel errors for query parsing. Capitalized strings are kept after
// translation in the handler so the API user-facing message reads naturally.
var (
	errInvalidSince = errors.New("invalid since: must be RFC3339")
	errInvalidUntil = errors.New("invalid until: must be RFC3339")
	errInvalidSize  = errors.New("invalid size parameter")
)

// parseListIncidentsOptions extracts ListIncidents query parameters. Kept
// out of the handler so the handler stays under the cyclop limit.
func parseListIncidentsOptions(query url.Values) (*ListIncidentsOptions, error) {
	opts := &ListIncidentsOptions{Size: 20}

	if v := query.Get("checkUid"); v != "" {
		opts.CheckUIDs = strings.Split(v, ",")
	}

	if v := query.Get("checkGroupUid"); v != "" {
		opts.CheckGroupUID = v
	}

	if v := query.Get("memberCheckUid"); v != "" {
		opts.MemberCheckUID = v
	}

	if v := query.Get("state"); v != "" {
		opts.States = strings.Split(v, ",")
	}

	since, err := parseRFC3339(query.Get("since"))
	if err != nil {
		return nil, errInvalidSince
	}
	opts.Since = since

	until, err := parseRFC3339(query.Get("until"))
	if err != nil {
		return nil, errInvalidUntil
	}
	opts.Until = until

	if v := query.Get("cursor"); v != "" {
		opts.Cursor = v
	}

	if v := query.Get("size"); v != "" {
		size, err := strconv.Atoi(v)
		if err != nil || size < 1 {
			return nil, errInvalidSize
		}

		if size > 100 {
			size = 100
		}

		opts.Size = size
	}

	if v := query.Get("with"); v != "" {
		for _, w := range strings.Split(v, ",") {
			if w == "check" {
				opts.WithCheck = true
			}
		}
	}

	return opts, nil
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
