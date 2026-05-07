// Package incidents provides incident management HTTP handlers.
package incidents

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/uptrace/bunrouter"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/handlers/base"
	"github.com/fclairamb/solidping/server/internal/middleware"
)

// Handler handles HTTP requests for incidents.
type Handler struct {
	base.HandlerBase
	svc *Service
	// jwtSecret is captured at construction so the magic-link ack handler
	// can sign and verify tokens without going through HandlerBase's
	// unexported cfg field.
	jwtSecret []byte
}

// NewHandler creates a new incidents handler.
func NewHandler(service *Service, cfg *config.Config) *Handler {
	return &Handler{
		HandlerBase: base.NewHandlerBase(cfg),
		svc:         service,
		jwtSecret:   []byte(cfg.Auth.JWTSecret),
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

	limit, err := base.ParsePageLimit(query, opts.Size, 100)
	if err != nil {
		return nil, err
	}
	opts.Size = limit

	applyListIncidentsExtras(query, opts)

	return opts, nil
}

// applyListIncidentsExtras handles the secondary query parameters (with,
// hideSuppressed, causedByIncidentUid). Split off so the parser stays under
// the cyclop limit.
func applyListIncidentsExtras(query url.Values, opts *ListIncidentsOptions) {
	if v := query.Get("with"); v != "" {
		for _, w := range strings.Split(v, ",") {
			if w == "check" {
				opts.WithCheck = true
			}
		}
	}

	if v := query.Get("hideSuppressed"); v == "true" || v == "1" {
		opts.HideSuppressed = true
	}

	if v := query.Get("causedByIncidentUid"); v != "" {
		opts.CausedByUID = v
	}
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
	case errors.Is(err, ErrSnoozeUntilInPast),
		errors.Is(err, ErrSnoozeTooLong),
		errors.Is(err, ErrSnoozeMissingDur),
		errors.Is(err, ErrSnoozeInvalidDur):
		return h.WriteError(writer, http.StatusBadRequest, base.ErrorCodeValidationError, err.Error())
	default:
		return h.WriteInternalError(writer, err)
	}
}

func (h *Handler) actorUID(req bunrouter.Request) string {
	if user, ok := middleware.GetUserFromContext(req.Context()); ok && user != nil {
		return user.UID
	}

	return ""
}

// AcknowledgeIncidentByLink handles GET /api/v1/orgs/:org/incidents/:uid/ack?token=…
// — the magic-link path used from email notifications. Returns text/html so
// it renders nicely when opened from a mail client (the link is opened via a
// browser navigation, not a fetch call). Token verification both authenticates
// and identifies the recipient.
func (h *Handler) AcknowledgeIncidentByLink(writer http.ResponseWriter, req bunrouter.Request) error {
	orgSlug := req.Param("org")
	incidentUID := req.Param("uid")
	token := req.URL.Query().Get("token")

	if token == "" {
		writeAckHTML(writer, http.StatusBadRequest, ackHTMLMissingToken)

		return nil
	}

	payload, err := VerifyAckToken(h.jwtSecret, incidentUID, token)
	if err != nil {
		switch {
		case errors.Is(err, ErrAckTokenExpired):
			writeAckHTML(writer, http.StatusGone, ackHTMLExpired)
		case errors.Is(err, ErrAckTokenSignature),
			errors.Is(err, ErrAckTokenIncidentMismatch),
			errors.Is(err, ErrAckTokenMalformed):
			writeAckHTML(writer, http.StatusBadRequest, ackHTMLInvalid)
		default:
			writeAckHTML(writer, http.StatusInternalServerError, ackHTMLError)
		}

		return nil
	}

	// Look up the user by email so the audit trail records a UID when
	// possible. Unknown emails are still allowed — the recipient_email goes
	// into the event payload either way.
	ackedBy := h.svc.lookupUserUIDByEmail(req.Context(), payload.RecipientEmail)

	if h.svc.tryEmailAck(req.Context(), orgSlug, incidentUID, ackedBy, payload.RecipientEmail) {
		writeAckHTML(writer, http.StatusOK, ackHTMLSuccess)
	} else {
		writeAckHTML(writer, http.StatusInternalServerError, ackHTMLError)
	}

	return nil
}

// AcknowledgeIncident handles POST /api/v1/orgs/:org/incidents/:uid/ack.
func (h *Handler) AcknowledgeIncident(writer http.ResponseWriter, req bunrouter.Request) error {
	orgSlug := req.Param("org")
	incidentUID := req.Param("uid")

	var body struct {
		Note string `json:"note"`
	}

	if req.Body != nil {
		_ = json.NewDecoder(req.Body).Decode(&body) // body is optional
	}

	incident, err := h.svc.AcknowledgeIncident(req.Context(), orgSlug, &AcknowledgeIncidentRequest{
		IncidentUID:    incidentUID,
		AcknowledgedBy: h.actorUID(req),
		Note:           body.Note,
		Via:            viaWeb,
	})
	if err != nil {
		return h.handleError(writer, err)
	}

	return h.WriteJSON(writer, http.StatusOK, incident)
}

// UnacknowledgeIncident handles POST /api/v1/orgs/:org/incidents/:uid/unack.
func (h *Handler) UnacknowledgeIncident(writer http.ResponseWriter, req bunrouter.Request) error {
	orgSlug := req.Param("org")
	incidentUID := req.Param("uid")

	incident, err := h.svc.UnacknowledgeIncident(req.Context(), orgSlug, incidentUID, h.actorUID(req), viaWeb)
	if err != nil {
		return h.handleError(writer, err)
	}

	return h.WriteJSON(writer, http.StatusOK, incident)
}

// SnoozeIncident handles POST /api/v1/orgs/:org/incidents/:uid/snooze.
func (h *Handler) SnoozeIncident(writer http.ResponseWriter, req bunrouter.Request) error {
	orgSlug := req.Param("org")
	incidentUID := req.Param("uid")

	var body struct {
		Until    *time.Time `json:"until"`
		Duration string     `json:"duration"`
		Reason   string     `json:"reason"`
	}

	if req.Body != nil {
		if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
			return h.WriteError(writer, http.StatusBadRequest, base.ErrorCodeValidationError, "Invalid JSON body")
		}
	}

	snoozeReq := &SnoozeIncidentRequest{
		IncidentUID: incidentUID,
		ActorUID:    h.actorUID(req),
		Until:       body.Until,
		Reason:      body.Reason,
		Via:         viaWeb,
	}

	if body.Duration != "" {
		dur, err := time.ParseDuration(body.Duration)
		if err != nil {
			return h.WriteError(writer, http.StatusBadRequest, base.ErrorCodeValidationError, ErrSnoozeInvalidDur.Error())
		}

		snoozeReq.Duration = &dur
	}

	incident, err := h.svc.SnoozeIncident(req.Context(), orgSlug, snoozeReq)
	if err != nil {
		return h.handleError(writer, err)
	}

	return h.WriteJSON(writer, http.StatusOK, incident)
}

// UnsnoozeIncident handles POST /api/v1/orgs/:org/incidents/:uid/unsnooze.
func (h *Handler) UnsnoozeIncident(writer http.ResponseWriter, req bunrouter.Request) error {
	orgSlug := req.Param("org")
	incidentUID := req.Param("uid")

	incident, err := h.svc.UnsnoozeIncident(req.Context(), orgSlug, incidentUID, h.actorUID(req), "manual")
	if err != nil {
		return h.handleError(writer, err)
	}

	return h.WriteJSON(writer, http.StatusOK, incident)
}

// ResolveIncident handles POST /api/v1/orgs/:org/incidents/:uid/resolve.
func (h *Handler) ResolveIncident(writer http.ResponseWriter, req bunrouter.Request) error {
	orgSlug := req.Param("org")
	incidentUID := req.Param("uid")

	var body struct {
		Note string `json:"note"`
	}

	if req.Body != nil {
		_ = json.NewDecoder(req.Body).Decode(&body)
	}

	incident, err := h.svc.ResolveIncident(req.Context(), orgSlug, &ResolveIncidentRequest{
		IncidentUID: incidentUID,
		ActorUID:    h.actorUID(req),
		Note:        body.Note,
		Via:         viaWeb,
	})
	if err != nil {
		return h.handleError(writer, err)
	}

	return h.WriteJSON(writer, http.StatusOK, incident)
}
