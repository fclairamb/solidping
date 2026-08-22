// Package events provides event listing HTTP handlers.
package events

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/handlers/base"
	"github.com/fclairamb/solidping/server/internal/httpx"
	"github.com/fclairamb/solidping/server/internal/middleware"
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
func (h *Handler) ListEvents(writer http.ResponseWriter, req *http.Request) error {
	orgSlug := httpx.Param(req, "org")

	query := req.URL.Query()

	// Parse query parameters
	opts := ListEventsOptions{
		Size: 20, // Default size
	}

	opts.Caller = callerFrom(req)

	// Parse eventType filter (comma-separated, exact match)
	if typeParam := query.Get("eventType"); typeParam != "" {
		opts.EventTypes = splitCSV(typeParam)
	}

	// Parse type filter (comma-separated FAMILY prefixes, e.g. ?type=auth,member).
	// Deliberately a separate parameter from eventType rather than an overload
	// of it: "auth" is not an event type, and silently prefix-matching an exact
	// filter would make `?eventType=check.created` also admit a future
	// `check.created_from_template`.
	if familyParam := query.Get("type"); familyParam != "" {
		opts.EventTypePrefixes = splitCSV(familyParam)
	}

	// Parse actorUserUid (spec name) / actorUid (alias). The COLUMN is
	// actor_uid; the spec calls the concept actor_user_uid. Both spellings are
	// accepted so neither name is a trap.
	if actorUID := firstNonEmpty(query.Get("actorUserUid"), query.Get("actorUid")); actorUID != "" {
		opts.ActorUID = &actorUID
	}

	since, until, err := parseTimeRange(query)
	if err != nil {
		return h.WriteErrorErr(
			writer, req, http.StatusBadRequest, base.ErrorCodeValidationError,
			"Invalid since/until parameter", err)
	}

	opts.Since = since
	opts.Until = until

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

	// Parse limit (canonical) or size (deprecated alias). Default 20, max 100.
	limit, err := base.ParsePageLimit(query, opts.Size, 100)
	if err != nil {
		return h.WriteErrorErr(
			writer, req, http.StatusBadRequest, base.ErrorCodeValidationError, "Invalid limit parameter", err)
	}
	opts.Size = limit

	response, err := h.svc.ListEvents(req.Context(), orgSlug, &opts)
	if err != nil {
		return h.handleError(writer, req, err)
	}

	return h.WriteJSON(writer, http.StatusOK, response)
}

// ListIncidentEvents handles GET /api/v1/orgs/:org/incidents/:uid/events.
func (h *Handler) ListIncidentEvents(writer http.ResponseWriter, req *http.Request) error {
	orgSlug := httpx.Param(req, "org")
	incidentUID := httpx.Param(req, "uid")

	query := req.URL.Query()

	// Parse query parameters
	opts := ListEventsOptions{
		IncidentUID: &incidentUID,
		Size:        20, // Default size
		Caller:      callerFrom(req),
	}

	// Parse cursor
	if cursor := query.Get("cursor"); cursor != "" {
		opts.Cursor = cursor
	}

	// Parse limit (canonical) or size (deprecated alias). Default 20, max 100.
	limit, err := base.ParsePageLimit(query, opts.Size, 100)
	if err != nil {
		return h.WriteErrorErr(
			writer, req, http.StatusBadRequest, base.ErrorCodeValidationError, "Invalid limit parameter", err)
	}
	opts.Size = limit

	response, err := h.svc.ListEvents(req.Context(), orgSlug, &opts)
	if err != nil {
		return h.handleError(writer, req, err)
	}

	return h.WriteJSON(writer, http.StatusOK, response)
}

// ListCheckEvents handles GET /api/v1/orgs/:org/checks/:checkUid/events.
func (h *Handler) ListCheckEvents(writer http.ResponseWriter, req *http.Request) error {
	orgSlug := httpx.Param(req, "org")
	checkUID := httpx.Param(req, "checkUid")

	query := req.URL.Query()

	// Parse query parameters
	opts := ListEventsOptions{
		CheckUID: &checkUID,
		Size:     20, // Default size
		Caller:   callerFrom(req),
	}

	// Parse cursor
	if cursor := query.Get("cursor"); cursor != "" {
		opts.Cursor = cursor
	}

	// Parse limit (canonical) or size (deprecated alias). Default 20, max 100.
	limit, err := base.ParsePageLimit(query, opts.Size, 100)
	if err != nil {
		return h.WriteErrorErr(
			writer, req, http.StatusBadRequest, base.ErrorCodeValidationError, "Invalid limit parameter", err)
	}
	opts.Size = limit

	response, err := h.svc.ListEvents(req.Context(), orgSlug, &opts)
	if err != nil {
		return h.handleError(writer, req, err)
	}

	return h.WriteJSON(writer, http.StatusOK, response)
}

// handleError translates service errors to HTTP responses.
func (h *Handler) handleError(writer http.ResponseWriter, request *http.Request, err error) error {
	switch {
	case errors.Is(err, ErrOrganizationNotFound):
		return h.WriteError(writer, http.StatusNotFound, base.ErrorCodeOrganizationNotFound, "Organization not found")
	default:
		return h.WriteInternalError(writer, request, err)
	}
}

// callerFrom extracts the authenticated principal for the visibility gate. An
// unauthenticated request yields the zero Caller, which is treated as
// non-admin — these routes are behind RequireAuth, so that path is defensive
// rather than reachable.
func callerFrom(req *http.Request) Caller {
	user, ok := middleware.GetUserFromContext(req.Context())
	if !ok || user == nil {
		return Caller{}
	}

	return Caller{UserUID: user.UID, SuperAdmin: user.SuperAdmin}
}

// splitCSV splits a comma-separated parameter and drops empty entries, so
// "auth,,member" and a trailing comma do not produce a filter on "".
func splitCSV(value string) []string {
	parts := strings.Split(value, ",")
	out := make([]string, 0, len(parts))

	for _, part := range parts {
		trimmed := strings.TrimSpace(part)
		if trimmed != "" {
			out = append(out, trimmed)
		}
	}

	return out
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}

	return ""
}

// parseTimeRange reads the since/until window. Both are RFC3339; either may be
// omitted. They were declared on the options struct and the filter from the
// start but never actually parsed, so a time-bounded audit query silently
// returned the whole trail.
func parseTimeRange(query map[string][]string) (*time.Time, *time.Time, error) {
	get := func(key string) string {
		if values, ok := query[key]; ok && len(values) > 0 {
			return values[0]
		}

		return ""
	}

	var since, until *time.Time

	if raw := get("since"); raw != "" {
		parsed, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			return nil, nil, err
		}

		since = &parsed
	}

	if raw := get("until"); raw != "" {
		parsed, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			return nil, nil, err
		}

		until = &parsed
	}

	return since, until, nil
}
