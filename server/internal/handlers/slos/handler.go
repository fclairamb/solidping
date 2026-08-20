package slos

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/fclairamb/solidping/server/internal/config"
	entcore "github.com/fclairamb/solidping/server/internal/entitlements"
	"github.com/fclairamb/solidping/server/internal/handlers/base"
	entitlementshandler "github.com/fclairamb/solidping/server/internal/handlers/entitlements"
	"github.com/fclairamb/solidping/server/internal/httpx"
)

const (
	fieldBody      = "body"
	msgInvalidJSON = "Invalid JSON format"
	msgValidation  = "Validation error"
)

// Handler provides HTTP handlers for SLO endpoints.
type Handler struct {
	base.HandlerBase
	svc *Service
}

// NewHandler creates a new SLOs handler.
func NewHandler(service *Service, cfg *config.Config) *Handler {
	return &Handler{HandlerBase: base.NewHandlerBase(cfg), svc: service}
}

// List handles listing an organization's SLOs.
func (h *Handler) List(writer http.ResponseWriter, req *http.Request) error {
	orgSlug := httpx.Param(req, "org")
	query := req.URL.Query()

	limit := 100
	if raw := query.Get("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed <= 0 || parsed > 200 {
			return h.WriteError(
				writer, http.StatusBadRequest, base.ErrorCodeValidationError, "Invalid limit parameter")
		}

		limit = parsed
	}

	rows, err := h.svc.ListSLOs(req.Context(), orgSlug, query.Get("checkUid"), limit)
	if err != nil {
		return h.handleError(writer, req, err)
	}

	return h.WriteJSON(writer, http.StatusOK, map[string]any{"data": rows})
}

// Create handles creating a new SLO.
func (h *Handler) Create(writer http.ResponseWriter, req *http.Request) error {
	orgSlug := httpx.Param(req, "org")

	var createReq CreateRequest
	if err := json.NewDecoder(req.Body).Decode(&createReq); err != nil {
		return h.WriteValidationError(writer, "Invalid JSON", []base.ValidationErrorField{
			{Name: fieldBody, Message: msgInvalidJSON},
		})
	}

	row, err := h.svc.CreateSLO(req.Context(), orgSlug, &createReq)
	if err != nil {
		return h.handleError(writer, req, err)
	}

	return h.WriteJSON(writer, http.StatusCreated, row)
}

// Get handles retrieving a single SLO.
func (h *Handler) Get(writer http.ResponseWriter, req *http.Request) error {
	row, err := h.svc.GetSLO(req.Context(), httpx.Param(req, "org"), httpx.Param(req, "uid"))
	if err != nil {
		return h.handleError(writer, req, err)
	}

	return h.WriteJSON(writer, http.StatusOK, row)
}

// Update handles updating an SLO.
func (h *Handler) Update(writer http.ResponseWriter, req *http.Request) error {
	var updateReq UpdateRequest
	if err := json.NewDecoder(req.Body).Decode(&updateReq); err != nil {
		return h.WriteValidationError(writer, "Invalid JSON", []base.ValidationErrorField{
			{Name: fieldBody, Message: msgInvalidJSON},
		})
	}

	row, err := h.svc.UpdateSLO(req.Context(), httpx.Param(req, "org"), httpx.Param(req, "uid"), updateReq)
	if err != nil {
		return h.handleError(writer, req, err)
	}

	return h.WriteJSON(writer, http.StatusOK, row)
}

// Delete handles deleting an SLO.
func (h *Handler) Delete(writer http.ResponseWriter, req *http.Request) error {
	if err := h.svc.DeleteSLO(req.Context(), httpx.Param(req, "org"), httpx.Param(req, "uid")); err != nil {
		return h.handleError(writer, req, err)
	}

	writer.WriteHeader(http.StatusNoContent)

	return nil
}

// Status handles the current-window objective status.
func (h *Handler) Status(writer http.ResponseWriter, req *http.Request) error {
	status, err := h.svc.GetStatus(
		req.Context(), httpx.Param(req, "org"), httpx.Param(req, "uid"), time.Now(),
	)
	if err != nil {
		return h.handleError(writer, req, err)
	}

	return h.WriteJSON(writer, http.StatusOK, status)
}

// Burndown handles the current window's error-budget burn-down series.
func (h *Handler) Burndown(writer http.ResponseWriter, req *http.Request) error {
	series, err := h.svc.GetBurndown(
		req.Context(), httpx.Param(req, "org"), httpx.Param(req, "uid"), time.Now(),
	)
	if err != nil {
		return h.handleError(writer, req, err)
	}

	return h.WriteJSON(writer, http.StatusOK, series)
}

// History handles the past monthly windows.
func (h *Handler) History(writer http.ResponseWriter, req *http.Request) error {
	months := 0

	if raw := req.URL.Query().Get("months"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed <= 0 {
			return h.WriteError(
				writer, http.StatusBadRequest, base.ErrorCodeValidationError, "Invalid months parameter")
		}

		months = parsed
	}

	history, err := h.svc.GetHistory(
		req.Context(), httpx.Param(req, "org"), httpx.Param(req, "uid"), months, time.Now(),
	)
	if err != nil {
		return h.handleError(writer, req, err)
	}

	return h.WriteJSON(writer, http.StatusOK, history)
}

func (h *Handler) handleError(writer http.ResponseWriter, request *http.Request, err error) error {
	switch {
	case errors.Is(err, ErrOrganizationNotFound):
		return h.WriteErrorErr(
			writer, request, http.StatusNotFound, base.ErrorCodeOrganizationNotFound, "Organization not found", err)
	case errors.Is(err, ErrSLONotFound):
		return h.WriteErrorErr(
			writer, request, http.StatusNotFound, base.ErrorCodeSLONotFound, "SLO not found", err)
	case errors.Is(err, ErrNameRequired):
		return h.WriteValidationError(writer, msgValidation, []base.ValidationErrorField{
			{Name: "name", Message: "Name is required"},
		})
	case errors.Is(err, ErrInvalidSlug):
		return h.WriteValidationError(writer, msgValidation, []base.ValidationErrorField{
			{Name: "slug", Message: ErrInvalidSlug.Error()},
		})
	case errors.Is(err, ErrSlugTaken):
		return h.WriteErrorErr(writer, request, http.StatusConflict, base.ErrorCodeConflict, "Slug already in use", err)
	case errors.Is(err, ErrScopeRequired):
		return h.WriteValidationError(writer, msgValidation, []base.ValidationErrorField{
			{Name: "checkUid", Message: ErrScopeRequired.Error()},
		})
	case errors.Is(err, ErrScopeNotFound):
		return h.WriteValidationError(writer, msgValidation, []base.ValidationErrorField{
			{Name: "checkUid", Message: ErrScopeNotFound.Error()},
		})
	case errors.Is(err, ErrInvalidTarget):
		return h.WriteValidationError(writer, msgValidation, []base.ValidationErrorField{
			{Name: "targetPct", Message: ErrInvalidTarget.Error()},
		})
	case errors.Is(err, ErrInvalidTimezone):
		return h.WriteValidationError(writer, msgValidation, []base.ValidationErrorField{
			{Name: "timezone", Message: ErrInvalidTimezone.Error()},
		})
	case errors.Is(err, entcore.ErrEntitlementExceeded):
		var quotaErr *entcore.QuotaError
		if !errors.As(err, &quotaErr) {
			return h.WriteInternalError(writer, request, err)
		}

		body := entitlementshandler.FormatQuotaError(quotaErr)
		body["code"] = string(base.ErrorCodeQuotaExceeded)

		return h.WriteJSON(writer, http.StatusPaymentRequired, body)
	default:
		return h.WriteInternalError(writer, request, err)
	}
}
