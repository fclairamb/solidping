// Package incidents provides incident management HTTP handlers.
package incidents

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/handlers/base"
	"github.com/fclairamb/solidping/server/internal/httpx"
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
func (h *Handler) ListIncidents(writer http.ResponseWriter, req *http.Request) error {
	orgSlug := httpx.Param(req, "org")

	opts, parseErr := parseListIncidentsOptions(req.URL.Query())
	if parseErr != nil {
		return h.WriteError(writer, http.StatusBadRequest, base.ErrorCodeValidationError, parseErr.Error())
	}

	response, err := h.svc.ListIncidents(req.Context(), orgSlug, opts)
	if err != nil {
		return h.handleError(writer, req, err)
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
			switch w {
			case "check":
				opts.WithCheck = true
			case "members":
				opts.WithMembers = true
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
func (h *Handler) GetIncident(writer http.ResponseWriter, req *http.Request) error {
	orgSlug := httpx.Param(req, "org")
	incidentUID := httpx.Param(req, "uid")

	// Parse with parameter (e.g., ?with=check,members)
	//
	// BaseURL roots the signed attachment download links at the host this
	// request arrived on, so a link handed to a tenant works for that tenant.
	opts := &GetIncidentOptions{BaseURL: requestOrigin(req)}
	if withParam := req.URL.Query().Get("with"); withParam != "" {
		for _, w := range strings.Split(withParam, ",") {
			switch w {
			case "check":
				opts.WithCheck = true
			case "members":
				opts.WithMembers = true
			}
		}
	}

	response, err := h.svc.GetIncident(req.Context(), orgSlug, incidentUID, opts)
	if err != nil {
		return h.handleError(writer, req, err)
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
func (h *Handler) handleError(writer http.ResponseWriter, request *http.Request, err error) error {
	switch {
	case errors.Is(err, ErrOrganizationNotFound):
		return h.WriteError(writer, http.StatusNotFound, base.ErrorCodeOrganizationNotFound, "Organization not found")
	case errors.Is(err, ErrIncidentNotFound):
		return h.WriteError(writer, http.StatusNotFound, base.ErrorCodeNotFound, "Incident not found")
	case errors.Is(err, ErrSnoozeUntilInPast),
		errors.Is(err, ErrSnoozeTooLong),
		errors.Is(err, ErrSnoozeMissingDur),
		errors.Is(err, ErrSnoozeInvalidDur),
		errors.Is(err, ErrCommentEmpty),
		errors.Is(err, ErrCommentTooLong):
		return h.WriteError(writer, http.StatusBadRequest, base.ErrorCodeValidationError, err.Error())
	default:
		return h.WriteInternalError(writer, request, err)
	}
}

func (h *Handler) actorUID(req *http.Request) string {
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
func (h *Handler) AcknowledgeIncidentByLink(writer http.ResponseWriter, req *http.Request) error {
	orgSlug := httpx.Param(req, "org")
	incidentUID := httpx.Param(req, "uid")
	token := req.URL.Query().Get("token")

	if token == "" {
		writeAckHTML(writer, http.StatusBadRequest, ackHTMLMissingToken, orgSlug, incidentUID)

		return nil
	}

	payload, err := VerifyAckToken(h.jwtSecret, incidentUID, token)
	if err != nil {
		switch {
		case errors.Is(err, ErrAckTokenExpired):
			writeAckHTML(writer, http.StatusGone, ackHTMLExpired, orgSlug, incidentUID)
		case errors.Is(err, ErrAckTokenSignature),
			errors.Is(err, ErrAckTokenIncidentMismatch),
			errors.Is(err, ErrAckTokenMalformed),
			errors.Is(err, ErrAckTokenPurposeMismatch):
			// PurposeMismatch covers a well-formed, correctly-signed token
			// presented here whose purpose isn't "ack" — e.g. an unsubscribe
			// token pasted into an ack URL. Same "invalid" bucket as a
			// tampered token: both are client errors, not server errors, and
			// telling them apart would leak which failure mode succeeded.
			writeAckHTML(writer, http.StatusBadRequest, ackHTMLInvalid, orgSlug, incidentUID)
		default:
			writeAckHTML(writer, http.StatusInternalServerError, ackHTMLError, orgSlug, incidentUID)
		}

		return nil
	}

	// Look up the user by email so the audit trail records a UID when
	// possible. Unknown emails are still allowed — the recipient_email goes
	// into the event payload either way.
	ackedBy := h.svc.lookupUserUIDByEmail(req.Context(), payload.RecipientEmail)

	// orgSlug/incidentUID are only safe to build a redirect URL from once we
	// reach this point: incidentUID was just verified against the token by
	// VerifyAckToken above, and tryEmailAck's DB ack below only succeeds if
	// this exact (orgSlug, incidentUID) pair resolves to a real incident —
	// so a mismatched org in the URL path fails the ack and falls into the
	// non-redirecting error page instead.
	if h.svc.tryEmailAck(req.Context(), orgSlug, incidentUID, ackedBy, payload.RecipientEmail) {
		writeAckHTML(writer, http.StatusOK, ackHTMLSuccess, orgSlug, incidentUID)
	} else {
		writeAckHTML(writer, http.StatusInternalServerError, ackHTMLError, orgSlug, incidentUID)
	}

	return nil
}

// AcknowledgeIncident handles POST /api/v1/orgs/:org/incidents/:uid/ack.
func (h *Handler) AcknowledgeIncident(writer http.ResponseWriter, req *http.Request) error {
	orgSlug := httpx.Param(req, "org")
	incidentUID := httpx.Param(req, "uid")

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
		return h.handleError(writer, req, err)
	}

	return h.WriteJSON(writer, http.StatusOK, incidentToResponse(incident))
}

// UnacknowledgeIncident handles POST /api/v1/orgs/:org/incidents/:uid/unack.
func (h *Handler) UnacknowledgeIncident(writer http.ResponseWriter, req *http.Request) error {
	orgSlug := httpx.Param(req, "org")
	incidentUID := httpx.Param(req, "uid")

	incident, err := h.svc.UnacknowledgeIncident(req.Context(), orgSlug, incidentUID, h.actorUID(req), viaWeb)
	if err != nil {
		return h.handleError(writer, req, err)
	}

	return h.WriteJSON(writer, http.StatusOK, incidentToResponse(incident))
}

// SnoozeIncident handles POST /api/v1/orgs/:org/incidents/:uid/snooze.
func (h *Handler) SnoozeIncident(writer http.ResponseWriter, req *http.Request) error {
	orgSlug := httpx.Param(req, "org")
	incidentUID := httpx.Param(req, "uid")

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
		return h.handleError(writer, req, err)
	}

	return h.WriteJSON(writer, http.StatusOK, incidentToResponse(incident))
}

// UnsnoozeIncident handles POST /api/v1/orgs/:org/incidents/:uid/unsnooze.
func (h *Handler) UnsnoozeIncident(writer http.ResponseWriter, req *http.Request) error {
	orgSlug := httpx.Param(req, "org")
	incidentUID := httpx.Param(req, "uid")

	incident, err := h.svc.UnsnoozeIncident(req.Context(), orgSlug, incidentUID, h.actorUID(req), "manual")
	if err != nil {
		return h.handleError(writer, req, err)
	}

	return h.WriteJSON(writer, http.StatusOK, incidentToResponse(incident))
}

// ResolveIncident handles POST /api/v1/orgs/:org/incidents/:uid/resolve.
func (h *Handler) ResolveIncident(writer http.ResponseWriter, req *http.Request) error {
	orgSlug := httpx.Param(req, "org")
	incidentUID := httpx.Param(req, "uid")

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
		return h.handleError(writer, req, err)
	}

	return h.WriteJSON(writer, http.StatusOK, incidentToResponse(incident))
}

// commentEventResponse mirrors the events-list DTO shape so the dashboard can
// reuse its Event type for the row returned by the create-comment endpoint.
type commentEventResponse struct {
	UID         string         `json:"uid"`
	IncidentUID *string        `json:"incidentUid,omitempty"`
	CheckUID    *string        `json:"checkUid,omitempty"`
	EventType   string         `json:"eventType"`
	ActorType   string         `json:"actorType"`
	ActorUID    *string        `json:"actorUid,omitempty"`
	Payload     map[string]any `json:"payload,omitempty"`
	CreatedAt   time.Time      `json:"createdAt"`
}

// AddComment handles POST /api/v1/orgs/:org/incidents/:uid/comments — an
// authenticated dashboard user appends a free-text comment to the incident
// timeline. Returns the created event so the client can render it optimistically.
func (h *Handler) AddComment(writer http.ResponseWriter, req *http.Request) error {
	orgSlug := httpx.Param(req, "org")
	incidentUID := httpx.Param(req, "uid")

	var body struct {
		Text string `json:"text"`
	}

	if req.Body != nil {
		if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
			return h.WriteError(writer, http.StatusBadRequest, base.ErrorCodeValidationError, "Invalid JSON body")
		}
	}

	event, err := h.svc.AddComment(req.Context(), orgSlug, &AddCommentRequest{
		IncidentUID: incidentUID,
		Text:        body.Text,
		Source:      CommentSourceWeb,
		ActorUID:    h.actorUID(req),
	})
	if err != nil {
		return h.handleError(writer, req, err)
	}

	return h.WriteJSON(writer, http.StatusCreated, commentEventResponse{
		UID:         event.UID,
		IncidentUID: event.IncidentUID,
		CheckUID:    event.CheckUID,
		EventType:   string(event.EventType),
		ActorType:   string(event.ActorType),
		ActorUID:    event.ActorUID,
		Payload:     event.Payload,
		CreatedAt:   event.CreatedAt,
	})
}

// requestScheme resolves the request scheme, preferring the first token of a
// proxy-set X-Forwarded-Proto, then the TLS state, defaulting to http. A twin
// of the statuspages helper: both build a self-referential absolute URL from
// the request, and neither package should import the other for it.
func requestScheme(req *http.Request) string {
	if proto := req.Header.Get("X-Forwarded-Proto"); proto != "" {
		if idx := strings.IndexByte(proto, ','); idx != -1 {
			proto = proto[:idx]
		}

		if proto = strings.TrimSpace(proto); proto != "" {
			return proto
		}
	}

	if req.TLS != nil {
		return "https"
	}

	return "http"
}

// requestOrigin builds the "scheme://host" origin for a request.
func requestOrigin(req *http.Request) string {
	return requestScheme(req) + "://" + req.Host
}
