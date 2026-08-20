// Package statusupdates provides HTTP handlers for status update management endpoints.
package statusupdates

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/handlers/base"
	"github.com/fclairamb/solidping/server/internal/httpx"
	"github.com/fclairamb/solidping/server/internal/middleware"
)

const (
	fieldBody      = "body"
	msgInvalidJSON = "Invalid JSON format"
)

// Handler provides HTTP handlers for status update management endpoints.
type Handler struct {
	base.HandlerBase
	svc *Service
}

// NewHandler creates a new status updates handler.
func NewHandler(service *Service, cfg *config.Config) *Handler {
	return &Handler{
		HandlerBase: base.NewHandlerBase(cfg),
		svc:         service,
	}
}

func (h *Handler) actorUID(req *http.Request) string {
	if user, ok := middleware.GetUserFromContext(req.Context()); ok && user != nil {
		return user.UID
	}

	return ""
}

// ListStatusUpdates handles GET /api/v1/orgs/:org/status-updates.
func (h *Handler) ListStatusUpdates(writer http.ResponseWriter, req *http.Request) error {
	orgSlug := httpx.Param(req, "org")
	query := req.URL.Query()

	limit := 50
	if rawLimit := query.Get("limit"); rawLimit != "" {
		parsed, err := strconv.Atoi(rawLimit)
		if err != nil || parsed <= 0 || parsed > 200 {
			return h.WriteError(
				writer, http.StatusBadRequest, base.ErrorCodeValidationError, "limit must be 1–200")
		}

		limit = parsed
	}

	offset := 0
	if rawOffset := query.Get("offset"); rawOffset != "" {
		parsed, err := strconv.Atoi(rawOffset)
		if err != nil || parsed < 0 {
			return h.WriteError(
				writer, http.StatusBadRequest, base.ErrorCodeValidationError, "offset must be non-negative")
		}

		offset = parsed
	}

	opts := ListStatusUpdatesOptions{
		StatusPage: query.Get("statusPage"),
		Section:    query.Get("section"),
		Check:      query.Get("check"),
		Incident:   query.Get("incident"),
		Limit:      limit,
		Offset:     offset,
	}

	updates, err := h.svc.ListStatusUpdates(req.Context(), orgSlug, &opts)
	if err != nil {
		return h.handleError(writer, req, err)
	}

	return h.WriteJSON(writer, http.StatusOK, map[string]interface{}{
		"data": updates,
	})
}

// CreateStatusUpdate handles POST /api/v1/orgs/:org/status-updates.
func (h *Handler) CreateStatusUpdate(writer http.ResponseWriter, req *http.Request) error {
	orgSlug := httpx.Param(req, "org")

	var createReq CreateStatusUpdateRequest
	if err := json.NewDecoder(req.Body).Decode(&createReq); err != nil {
		return h.WriteValidationError(writer, "Invalid JSON", []base.ValidationErrorField{
			{Name: fieldBody, Message: msgInvalidJSON},
		})
	}

	update, err := h.svc.CreateStatusUpdate(req.Context(), orgSlug, h.actorUID(req), &createReq)
	if err != nil {
		return h.handleError(writer, req, err)
	}

	return h.WriteJSON(writer, http.StatusCreated, update)
}

// GetStatusUpdate handles GET /api/v1/orgs/:org/status-updates/:uid.
func (h *Handler) GetStatusUpdate(writer http.ResponseWriter, req *http.Request) error {
	orgSlug := httpx.Param(req, "org")
	uid := httpx.Param(req, "uid")

	update, err := h.svc.GetStatusUpdate(req.Context(), orgSlug, uid)
	if err != nil {
		return h.handleError(writer, req, err)
	}

	return h.WriteJSON(writer, http.StatusOK, update)
}

// UpdateStatusUpdate handles PATCH /api/v1/orgs/:org/status-updates/:uid.
func (h *Handler) UpdateStatusUpdate(writer http.ResponseWriter, req *http.Request) error {
	orgSlug := httpx.Param(req, "org")
	uid := httpx.Param(req, "uid")

	var updateReq UpdateStatusUpdateRequest
	if err := json.NewDecoder(req.Body).Decode(&updateReq); err != nil {
		return h.WriteValidationError(writer, "Invalid JSON", []base.ValidationErrorField{
			{Name: fieldBody, Message: msgInvalidJSON},
		})
	}

	update, err := h.svc.UpdateStatusUpdate(req.Context(), orgSlug, uid, h.actorUID(req), &updateReq)
	if err != nil {
		return h.handleError(writer, req, err)
	}

	return h.WriteJSON(writer, http.StatusOK, update)
}

// DeleteStatusUpdate handles DELETE /api/v1/orgs/:org/status-updates/:uid → 204.
func (h *Handler) DeleteStatusUpdate(writer http.ResponseWriter, req *http.Request) error {
	orgSlug := httpx.Param(req, "org")
	uid := httpx.Param(req, "uid")

	if err := h.svc.DeleteStatusUpdate(req.Context(), orgSlug, uid, h.actorUID(req)); err != nil {
		return h.handleError(writer, req, err)
	}

	writer.WriteHeader(http.StatusNoContent)

	return nil
}

//nolint:cyclop // error taxonomy requires all cases
func (h *Handler) handleError(writer http.ResponseWriter, request *http.Request, err error) error {
	switch {
	case errors.Is(err, ErrOrganizationNotFound):
		return h.WriteError(writer, http.StatusNotFound, base.ErrorCodeOrganizationNotFound, "Organization not found")
	case errors.Is(err, ErrStatusUpdateNotFound):
		return h.WriteError(writer, http.StatusNotFound, base.ErrorCodeNotFound, "Status update not found")
	case errors.Is(err, ErrStatusPageNotFound):
		return h.WriteError(writer, http.StatusNotFound, base.ErrorCodeStatusPageNotFound, "Status page not found")
	case errors.Is(err, ErrStatusPageForbidden):
		return h.WriteError(
			writer, http.StatusForbidden, base.ErrorCodeForbidden,
			"Status page belongs to a different organization",
		)
	case errors.Is(err, ErrSectionNotFound):
		return h.WriteError(
			writer, http.StatusNotFound, base.ErrorCodeStatusPageSectionNotFound,
			"Section not found on status page",
		)
	case errors.Is(err, ErrCheckNotFound):
		return h.WriteError(writer, http.StatusNotFound, base.ErrorCodeCheckNotFound, "Check not found on status page")
	case errors.Is(err, ErrIncidentNotFound):
		return h.WriteError(writer, http.StatusNotFound, base.ErrorCodeNotFound, "Incident not found")
	case errors.Is(err, ErrInvalidKind):
		return h.WriteValidationError(writer, "Invalid kind", []base.ValidationErrorField{
			{Name: "kind", Message: "Must be one of: investigating, identified, monitoring, resolved, maintenance, info"},
		})
	case errors.Is(err, ErrTitleRequired):
		return h.WriteValidationError(writer, "Title required", []base.ValidationErrorField{
			{Name: "title", Message: "Title is required"},
		})
	case errors.Is(err, ErrTitleTooLong):
		return h.WriteValidationError(writer, "Title too long", []base.ValidationErrorField{
			{Name: "title", Message: "Title must be at most 200 characters"},
		})
	case errors.Is(err, ErrBodyRequired):
		return h.WriteValidationError(writer, "Body required", []base.ValidationErrorField{
			{Name: "bodyMarkdown", Message: "Body is required"},
		})
	case errors.Is(err, ErrBodyTooLong):
		return h.WriteValidationError(writer, "Body too long", []base.ValidationErrorField{
			{Name: "bodyMarkdown", Message: "Body must be at most 16384 characters"},
		})
	case errors.Is(err, ErrInvalidLinkURL):
		return h.WriteValidationError(writer, "Invalid link URL", []base.ValidationErrorField{
			{Name: "linkUrl", Message: "Must be a valid http or https URL"},
		})
	case errors.Is(err, ErrCheckUIDMismatch):
		return h.WriteValidationError(writer, "Check mismatch", []base.ValidationErrorField{
			{Name: "checkUid", Message: "Check does not match the incident's check"},
		})
	default:
		return h.WriteInternalError(writer, request, err)
	}
}
