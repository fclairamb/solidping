package incidentpublications

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/handlers/base"
	"github.com/fclairamb/solidping/server/internal/httpx"
	"github.com/fclairamb/solidping/server/internal/middleware"
	"github.com/fclairamb/solidping/server/internal/statuspagecache"
	"github.com/fclairamb/solidping/server/internal/statuspagelock"
)

const (
	fieldBody      = "body"
	msgInvalidJSON = "Invalid JSON format"
	paramOrg       = "org"
	paramPage      = "statusPageUid"
	paramUID       = "uid"
	paramIncident  = "incidentUid"
	keyData        = "data"
	// valueTrue is the only accepted spelling of a boolean query flag.
	valueTrue = "true"
)

// Handler exposes the publication overlay over HTTP.
type Handler struct {
	base.HandlerBase
	svc *Service
}

// NewHandler builds the publication handler.
func NewHandler(service *Service, cfg *config.Config) *Handler {
	return &Handler{HandlerBase: base.NewHandlerBase(cfg), svc: service}
}

func (h *Handler) actorUID(req *http.Request) string {
	if user, ok := middleware.GetUserFromContext(req.Context()); ok && user != nil {
		return user.UID
	}

	return ""
}

// List handles GET /api/v1/orgs/:org/status-pages/:statusPageUid/incidents.
func (h *Handler) List(writer http.ResponseWriter, req *http.Request) error {
	query := req.URL.Query()

	opts := ListOptions{State: query.Get("state")}
	if query.Get("active") == valueTrue {
		opts.ActiveOnly = true
	}

	// ?stale=true is the "open on the page, recovered in reality" feed dash0's
	// warning banner reads (spec 2026-09-02-05). It filters the page of rows
	// the other parameters selected; every row still carries its own `stale`
	// flag, so a caller that wants both kinds can ask once and split.
	if query.Get("stale") == valueTrue {
		opts.StaleOnly = true
	}

	if raw := query.Get("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed <= 0 || parsed > 200 {
			return h.WriteError(writer, http.StatusBadRequest, base.ErrorCodeValidationError, "limit must be 1–200")
		}

		opts.Limit = parsed
	}

	if raw := query.Get("offset"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 0 {
			return h.WriteError(
				writer, http.StatusBadRequest, base.ErrorCodeValidationError, "offset must be non-negative")
		}

		opts.Offset = parsed
	}

	pubs, err := h.svc.ListPublications(
		req.Context(), httpx.Param(req, paramOrg), httpx.Param(req, paramPage), opts)
	if err != nil {
		return h.handleError(writer, req, err)
	}

	return h.WriteJSON(writer, http.StatusOK, map[string]any{keyData: pubs})
}

// Create handles POST /api/v1/orgs/:org/status-pages/:statusPageUid/incidents.
func (h *Handler) Create(writer http.ResponseWriter, req *http.Request) error {
	var body CreatePublicationRequest
	if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
		return h.WriteValidationError(writer, "Invalid JSON", []base.ValidationErrorField{
			{Name: fieldBody, Message: msgInvalidJSON},
		})
	}

	pub, err := h.svc.CreatePublication(
		req.Context(), httpx.Param(req, paramOrg), httpx.Param(req, paramPage), h.actorUID(req), &body)
	if err != nil {
		return h.handleError(writer, req, err)
	}

	return h.WriteJSON(writer, http.StatusCreated, pub)
}

// Get handles GET /api/v1/orgs/:org/status-pages/:statusPageUid/incidents/:uid.
func (h *Handler) Get(writer http.ResponseWriter, req *http.Request) error {
	pub, err := h.svc.GetPublication(
		req.Context(), httpx.Param(req, paramOrg), httpx.Param(req, paramPage), httpx.Param(req, paramUID))
	if err != nil {
		return h.handleError(writer, req, err)
	}

	return h.WriteJSON(writer, http.StatusOK, pub)
}

// Update handles PATCH /api/v1/orgs/:org/status-pages/:statusPageUid/incidents/:uid.
func (h *Handler) Update(writer http.ResponseWriter, req *http.Request) error {
	var body UpdatePublicationRequest
	if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
		return h.WriteValidationError(writer, "Invalid JSON", []base.ValidationErrorField{
			{Name: fieldBody, Message: msgInvalidJSON},
		})
	}

	pub, err := h.svc.UpdatePublication(
		req.Context(), httpx.Param(req, paramOrg), httpx.Param(req, paramPage),
		httpx.Param(req, paramUID), h.actorUID(req), &body)
	if err != nil {
		return h.handleError(writer, req, err)
	}

	return h.WriteJSON(writer, http.StatusOK, pub)
}

// AppendUpdate handles POST .../incidents/:uid/updates.
func (h *Handler) AppendUpdate(writer http.ResponseWriter, req *http.Request) error {
	var body AppendUpdateRequest
	if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
		return h.WriteValidationError(writer, "Invalid JSON", []base.ValidationErrorField{
			{Name: fieldBody, Message: msgInvalidJSON},
		})
	}

	update, err := h.svc.AppendUpdate(
		req.Context(), httpx.Param(req, paramOrg), httpx.Param(req, paramPage),
		httpx.Param(req, paramUID), h.actorUID(req), &body)
	if err != nil {
		return h.handleError(writer, req, err)
	}

	return h.WriteJSON(writer, http.StatusCreated, update)
}

// ListForIncident handles GET /api/v1/orgs/:org/incidents/:incidentUid/publications.
func (h *Handler) ListForIncident(writer http.ResponseWriter, req *http.Request) error {
	pubs, err := h.svc.ListForIncident(
		req.Context(), httpx.Param(req, paramOrg), httpx.Param(req, paramIncident))
	if err != nil {
		return h.handleError(writer, req, err)
	}

	return h.WriteJSON(writer, http.StatusOK, map[string]any{keyData: pubs})
}

// PublishIncident handles POST /api/v1/orgs/:org/incidents/:incidentUid/publications.
func (h *Handler) PublishIncident(writer http.ResponseWriter, req *http.Request) error {
	var body PublishIncidentRequest
	if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
		return h.WriteValidationError(writer, "Invalid JSON", []base.ValidationErrorField{
			{Name: fieldBody, Message: msgInvalidJSON},
		})
	}

	if body.StatusPageUID == "" {
		return h.WriteValidationError(writer, "Status page required", []base.ValidationErrorField{
			{Name: "statusPageUid", Message: "statusPageUid is required"},
		})
	}

	pub, err := h.svc.PublishIncident(
		req.Context(), httpx.Param(req, paramOrg), httpx.Param(req, paramIncident), h.actorUID(req), &body)
	if err != nil {
		return h.handleError(writer, req, err)
	}

	return h.WriteJSON(writer, http.StatusCreated, pub)
}

// Unpublish handles DELETE /api/v1/orgs/:org/incidents/:incidentUid/publications/:uid.
func (h *Handler) Unpublish(writer http.ResponseWriter, req *http.Request) error {
	err := h.svc.UnpublishIncident(
		req.Context(), httpx.Param(req, paramOrg), httpx.Param(req, paramIncident),
		httpx.Param(req, paramUID), h.actorUID(req))
	if err != nil {
		return h.handleError(writer, req, err)
	}

	writer.WriteHeader(http.StatusNoContent)

	return nil
}

// ViewPublicIncidents handles GET /api/v1/status-pages/:org/:slug/incidents —
// the PUBLIC history feed. It applies the identical visibility gate the rest of
// the public status-page surface uses, so a private page's incidents are as
// invisible as the page itself, and it caches by the same rule: this body
// quotes incident titles and update bodies verbatim, and the route is on the
// custom-host allowlist, so a gated page's narrative must never be retained by
// the CDN sitting in front of that host (spec 2026-08-22-06).
func (h *Handler) ViewPublicIncidents(writer http.ResponseWriter, req *http.Request) error {
	view, err := h.svc.ViewPublicIncidents(
		req.Context(), httpx.Param(req, paramOrg), httpx.Param(req, "slug"),
		req.URL.Query().Get("active") == valueTrue)
	if err != nil {
		return h.handlePublicError(writer, req, err)
	}

	statuspagecache.Apply(writer.Header(), view.Visibility, statuspagecache.PageMaxAge)

	return h.WriteJSON(writer, http.StatusOK, map[string]any{keyData: view.Incidents})
}

// handlePublicError is handleError with the never-shared cache directive
// applied first: a cacheable 404 on the public surface is an existence oracle,
// and a cacheable 401 is worse — it is the gated page's own URL, cached.
func (h *Handler) handlePublicError(writer http.ResponseWriter, req *http.Request, err error) error {
	statuspagecache.ApplyGated(writer.Header())

	return h.handleError(writer, req, err)
}

func (h *Handler) handleError(writer http.ResponseWriter, request *http.Request, err error) error {
	switch {
	case errors.Is(err, ErrOrganizationNotFound):
		return h.WriteError(writer, http.StatusNotFound, base.ErrorCodeNotFound, "Organization not found")
	case errors.Is(err, ErrStatusPageNotFound):
		return h.WriteError(
			writer, http.StatusNotFound, base.ErrorCodeStatusPageNotFound, "Status page not found")
	case errors.Is(err, statuspagelock.ErrLocked):
		return h.WriteError(writer, http.StatusUnauthorized, base.ErrorCodeStatusPageLocked,
			"This status page is password protected")
	case errors.Is(err, ErrPublicationNotFound):
		return h.WriteError(writer, http.StatusNotFound, base.ErrorCodeNotFound, "Incident publication not found")
	case errors.Is(err, ErrIncidentNotFound):
		return h.WriteError(writer, http.StatusNotFound, base.ErrorCodeNotFound, "Incident not found")
	case errors.Is(err, ErrAlreadyPublished):
		return h.WriteError(
			writer, http.StatusConflict, base.ErrorCodeConflict,
			"Incident is already published on this status page")
	case errors.Is(err, ErrTitleRequired):
		return h.WriteValidationError(writer, "Title required", []base.ValidationErrorField{
			{Name: "title", Message: "Title is required"},
		})
	case errors.Is(err, ErrTitleTooLong):
		return h.WriteValidationError(writer, "Title too long", []base.ValidationErrorField{
			{Name: "title", Message: "Title must be at most 200 characters"},
		})
	case errors.Is(err, ErrInvalidState):
		return h.WriteValidationError(writer, "Invalid state", []base.ValidationErrorField{
			{Name: "state", Message: "Must be one of: investigating, identified, monitoring, resolved"},
		})
	case errors.Is(err, ErrInvalidSeverity):
		return h.WriteValidationError(writer, "Invalid severity", []base.ValidationErrorField{
			{Name: "severity", Message: "Must be one of: minor, major, critical"},
		})
	case errors.Is(err, ErrInvalidKind):
		return h.WriteValidationError(writer, "Invalid kind", []base.ValidationErrorField{
			{Name: "kind", Message: "Must be one of: investigating, identified, monitoring, resolved, maintenance, info"},
		})
	case errors.Is(err, ErrBodyRequired):
		return h.WriteValidationError(writer, "Body required", []base.ValidationErrorField{
			{Name: "bodyMarkdown", Message: "Body is required"},
		})
	case errors.Is(err, ErrBodyTooLong):
		return h.WriteValidationError(writer, "Body too long", []base.ValidationErrorField{
			{Name: "bodyMarkdown", Message: "Body must be at most 16384 characters"},
		})
	default:
		return h.WriteInternalError(writer, request, err)
	}
}
