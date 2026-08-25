package maintenancewindows

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/handlers/base"
	"github.com/fclairamb/solidping/server/internal/httpx"
)

const (
	fieldBody      = "body"
	msgInvalidJSON = "Invalid JSON format"
)

// Handler provides HTTP handlers for maintenance window management endpoints.
type Handler struct {
	base.HandlerBase
	svc *Service
}

// NewHandler creates a new maintenance windows handler.
func NewHandler(service *Service, cfg *config.Config) *Handler {
	return &Handler{
		HandlerBase: base.NewHandlerBase(cfg),
		svc:         service,
	}
}

// List handles listing all maintenance windows for an organization.
func (h *Handler) List(writer http.ResponseWriter, req *http.Request) error {
	orgSlug := httpx.Param(req, "org")
	query := req.URL.Query()

	status := query.Get("status")

	limit := 50
	if limitParam := query.Get("limit"); limitParam != "" {
		parsed, err := strconv.Atoi(limitParam)
		if err != nil {
			return h.WriteError(
				writer, http.StatusBadRequest, base.ErrorCodeValidationError, "Invalid limit parameter")
		}

		if parsed > 0 && parsed <= 100 {
			limit = parsed
		}
	}

	windows, err := h.svc.ListMaintenanceWindows(req.Context(), orgSlug, status, limit)
	if err != nil {
		return h.handleOrgError(writer, req, err)
	}

	return h.WriteJSON(writer, http.StatusOK, map[string]interface{}{
		"data": windows,
	})
}

// Create handles creating a new maintenance window.
func (h *Handler) Create(writer http.ResponseWriter, req *http.Request) error {
	orgSlug := httpx.Param(req, "org")

	var createReq CreateRequest
	if err := json.NewDecoder(req.Body).Decode(&createReq); err != nil {
		return h.WriteValidationError(writer, "Invalid JSON", []base.ValidationErrorField{
			{Name: fieldBody, Message: msgInvalidJSON},
		})
	}

	window, err := h.svc.CreateMaintenanceWindow(req.Context(), orgSlug, &createReq)
	if err != nil {
		return h.handleCreateError(writer, req, err)
	}

	return h.WriteJSON(writer, http.StatusCreated, window)
}

// Get handles retrieving a single maintenance window by UID.
func (h *Handler) Get(writer http.ResponseWriter, req *http.Request) error {
	orgSlug := httpx.Param(req, "org")
	uid := httpx.Param(req, "uid")

	window, err := h.svc.GetMaintenanceWindow(req.Context(), orgSlug, uid)
	if err != nil {
		return h.handleWindowError(writer, req, err)
	}

	return h.WriteJSON(writer, http.StatusOK, window)
}

// Update handles updating an existing maintenance window.
func (h *Handler) Update(writer http.ResponseWriter, req *http.Request) error {
	orgSlug := httpx.Param(req, "org")
	uid := httpx.Param(req, "uid")

	var updateReq UpdateRequest
	if err := json.NewDecoder(req.Body).Decode(&updateReq); err != nil {
		return h.WriteValidationError(writer, "Invalid JSON", []base.ValidationErrorField{
			{Name: fieldBody, Message: msgInvalidJSON},
		})
	}

	window, err := h.svc.UpdateMaintenanceWindow(req.Context(), orgSlug, uid, updateReq)
	if err != nil {
		return h.handleUpdateError(writer, req, err)
	}

	return h.WriteJSON(writer, http.StatusOK, window)
}

// Delete handles deleting a maintenance window.
func (h *Handler) Delete(writer http.ResponseWriter, req *http.Request) error {
	orgSlug := httpx.Param(req, "org")
	uid := httpx.Param(req, "uid")

	if err := h.svc.DeleteMaintenanceWindow(req.Context(), orgSlug, uid); err != nil {
		return h.handleWindowError(writer, req, err)
	}

	writer.WriteHeader(http.StatusNoContent)

	return nil
}

// ListChecks handles listing check associations for a maintenance window.
func (h *Handler) ListChecks(writer http.ResponseWriter, req *http.Request) error {
	orgSlug := httpx.Param(req, "org")
	uid := httpx.Param(req, "uid")

	checks, err := h.svc.ListChecks(req.Context(), orgSlug, uid)
	if err != nil {
		return h.handleWindowError(writer, req, err)
	}

	return h.WriteJSON(writer, http.StatusOK, map[string]interface{}{
		"data": checks,
	})
}

// SetChecks handles setting check associations for a maintenance window.
func (h *Handler) SetChecks(writer http.ResponseWriter, req *http.Request) error {
	orgSlug := httpx.Param(req, "org")
	uid := httpx.Param(req, "uid")

	var setReq SetChecksRequest
	if err := json.NewDecoder(req.Body).Decode(&setReq); err != nil {
		return h.WriteValidationError(writer, "Invalid JSON", []base.ValidationErrorField{
			{Name: fieldBody, Message: msgInvalidJSON},
		})
	}

	if err := h.svc.SetChecks(req.Context(), orgSlug, uid, setReq); err != nil {
		return h.handleWindowError(writer, req, err)
	}

	writer.WriteHeader(http.StatusNoContent)

	return nil
}

func (h *Handler) handleOrgError(writer http.ResponseWriter, request *http.Request, err error) error {
	if errors.Is(err, ErrOrganizationNotFound) {
		return h.WriteErrorErr(
			writer, request, http.StatusNotFound, base.ErrorCodeOrganizationNotFound, "Organization not found", err)
	}

	return h.WriteInternalError(writer, request, err)
}

func (h *Handler) handleWindowError(writer http.ResponseWriter, request *http.Request, err error) error {
	switch {
	case errors.Is(err, ErrOrganizationNotFound):
		return h.WriteErrorErr(
			writer, request, http.StatusNotFound, base.ErrorCodeOrganizationNotFound, "Organization not found", err)
	case errors.Is(err, ErrMaintenanceWindowNotFound):
		return h.WriteErrorErr(
			writer, request, http.StatusNotFound, base.ErrorCodeMaintenanceWindowNotFound,
			"Maintenance window not found", err)
	default:
		return h.WriteInternalError(writer, request, err)
	}
}

func (h *Handler) handleCreateError(writer http.ResponseWriter, request *http.Request, err error) error {
	switch {
	case errors.Is(err, ErrOrganizationNotFound):
		return h.WriteErrorErr(
			writer, request, http.StatusNotFound, base.ErrorCodeOrganizationNotFound, "Organization not found", err)
	case errors.Is(err, ErrTitleRequired):
		return h.WriteValidationError(writer, "Validation error", []base.ValidationErrorField{
			{Name: "title", Message: "Title is required"},
		})
	case errors.Is(err, ErrInvalidTimeRange):
		return h.WriteValidationError(writer, "Validation error", []base.ValidationErrorField{
			{Name: "endAt", Message: "End time must be after start time"},
		})
	case errors.Is(err, ErrInvalidRecurrence):
		return h.WriteValidationError(writer, "Validation error", []base.ValidationErrorField{
			{Name: fieldRecurrence, Message: "Recurrence must be none, daily, weekly, or monthly"},
		})
	default:
		return h.WriteInternalError(writer, request, err)
	}
}

func (h *Handler) handleUpdateError(writer http.ResponseWriter, request *http.Request, err error) error {
	switch {
	case errors.Is(err, ErrOrganizationNotFound):
		return h.WriteErrorErr(
			writer, request, http.StatusNotFound, base.ErrorCodeOrganizationNotFound, "Organization not found", err)
	case errors.Is(err, ErrMaintenanceWindowNotFound):
		return h.WriteErrorErr(
			writer, request, http.StatusNotFound, base.ErrorCodeMaintenanceWindowNotFound,
			"Maintenance window not found", err)
	case errors.Is(err, ErrInvalidTimeRange):
		return h.WriteValidationError(writer, "Validation error", []base.ValidationErrorField{
			{Name: "endAt", Message: "End time must be after start time"},
		})
	case errors.Is(err, ErrInvalidRecurrence):
		return h.WriteValidationError(writer, "Validation error", []base.ValidationErrorField{
			{Name: fieldRecurrence, Message: "Recurrence must be none, daily, weekly, or monthly"},
		})
	default:
		return h.WriteInternalError(writer, request, err)
	}
}
