package sloalerts

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/handlers/base"
	"github.com/fclairamb/solidping/server/internal/httpx"
)

const msgValidation = "Validation error"

// Handler exposes the burn-rate alert policies nested under an SLO.
type Handler struct {
	base.HandlerBase
	svc *Service
}

// NewHandler creates a new SLO alerting handler.
func NewHandler(service *Service, cfg *config.Config) *Handler {
	return &Handler{HandlerBase: base.NewHandlerBase(cfg), svc: service}
}

// List handles listing an SLO's burn-rate alert policies.
func (h *Handler) List(writer http.ResponseWriter, req *http.Request) error {
	rows, err := h.svc.ListPolicies(
		req.Context(), httpx.Param(req, "org"), httpx.Param(req, "uid"), time.Now(),
	)
	if err != nil {
		return h.handleError(writer, req, err)
	}

	return h.WriteJSON(writer, http.StatusOK, map[string]any{"data": rows})
}

// Get handles retrieving one burn-rate alert policy.
func (h *Handler) Get(writer http.ResponseWriter, req *http.Request) error {
	row, err := h.svc.GetPolicy(
		req.Context(), httpx.Param(req, "org"), httpx.Param(req, "uid"),
		httpx.Param(req, "policyUid"), time.Now(),
	)
	if err != nil {
		return h.handleError(writer, req, err)
	}

	return h.WriteJSON(writer, http.StatusOK, row)
}

// Update handles tuning one burn-rate alert policy.
func (h *Handler) Update(writer http.ResponseWriter, req *http.Request) error {
	var updateReq UpdatePolicyRequest
	if err := json.NewDecoder(req.Body).Decode(&updateReq); err != nil {
		return h.WriteValidationError(writer, "Invalid JSON", []base.ValidationErrorField{
			{Name: "body", Message: "Invalid JSON format"},
		})
	}

	row, err := h.svc.UpdatePolicy(
		req.Context(), httpx.Param(req, "org"), httpx.Param(req, "uid"),
		httpx.Param(req, "policyUid"), updateReq, time.Now(),
	)
	if err != nil {
		return h.handleError(writer, req, err)
	}

	return h.WriteJSON(writer, http.StatusOK, row)
}

func (h *Handler) handleError(writer http.ResponseWriter, request *http.Request, err error) error {
	switch {
	case errors.Is(err, ErrOrganizationNotFound):
		return h.WriteErrorErr(
			writer, request, http.StatusNotFound, base.ErrorCodeOrganizationNotFound, "Organization not found", err)
	case errors.Is(err, ErrSLONotFound):
		return h.WriteErrorErr(
			writer, request, http.StatusNotFound, base.ErrorCodeSLONotFound, "SLO not found", err)
	case errors.Is(err, ErrPolicyNotFound):
		return h.WriteErrorErr(
			writer, request, http.StatusNotFound, base.ErrorCodeNotFound, "Alert policy not found", err)
	case errors.Is(err, ErrInvalidWindows):
		return h.WriteValidationError(writer, msgValidation, []base.ValidationErrorField{
			{Name: "shortWindowSeconds", Message: ErrInvalidWindows.Error()},
		})
	case errors.Is(err, ErrInvalidThreshold):
		return h.WriteValidationError(writer, msgValidation, []base.ValidationErrorField{
			{Name: "threshold", Message: ErrInvalidThreshold.Error()},
		})
	case errors.Is(err, ErrInvalidSeverity):
		return h.WriteValidationError(writer, msgValidation, []base.ValidationErrorField{
			{Name: "severity", Message: ErrInvalidSeverity.Error()},
		})
	case errors.Is(err, ErrInvalidMinSamples):
		return h.WriteValidationError(writer, msgValidation, []base.ValidationErrorField{
			{Name: "minSamples", Message: ErrInvalidMinSamples.Error()},
		})
	default:
		return h.WriteInternalError(writer, request, err)
	}
}
