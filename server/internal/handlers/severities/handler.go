package severities

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/handlers/base"
	"github.com/fclairamb/solidping/server/internal/httpx"
)

// Handler exposes the severity CRUD endpoints over HTTP.
type Handler struct {
	base.HandlerBase
	svc *Service
}

// NewHandler wires up a severities handler.
func NewHandler(service *Service, cfg *config.Config) *Handler {
	return &Handler{
		HandlerBase: base.NewHandlerBase(cfg),
		svc:         service,
	}
}

// ListSeverities handles GET /orgs/$org/severities.
func (h *Handler) ListSeverities(writer http.ResponseWriter, req *http.Request) error {
	severities, err := h.svc.ListSeverities(req.Context(), httpx.Param(req, "org"))
	if err != nil {
		return h.handleErr(writer, req, err)
	}

	return h.WriteJSON(writer, http.StatusOK, map[string]any{"data": severities})
}

// CreateSeverity handles POST /orgs/$org/severities.
func (h *Handler) CreateSeverity(writer http.ResponseWriter, req *http.Request) error {
	var createReq CreateSeverityRequest
	if err := json.NewDecoder(req.Body).Decode(&createReq); err != nil {
		return h.WriteValidationError(writer, "Invalid JSON", []base.ValidationErrorField{
			{Name: "body", Message: "Invalid JSON format"},
		})
	}

	if createReq.Name == "" {
		return h.WriteValidationError(writer, "Validation error", []base.ValidationErrorField{
			{Name: "name", Message: "Name is required"},
		})
	}

	severity, err := h.svc.CreateSeverity(req.Context(), httpx.Param(req, "org"), createReq)
	if err != nil {
		return h.handleErr(writer, req, err)
	}

	return h.WriteJSON(writer, http.StatusCreated, severity)
}

// GetSeverity handles GET /orgs/$org/severities/$identifier.
func (h *Handler) GetSeverity(writer http.ResponseWriter, req *http.Request) error {
	severity, err := h.svc.GetSeverity(req.Context(), httpx.Param(req, "org"), httpx.Param(req, "uid"))
	if err != nil {
		return h.handleErr(writer, req, err)
	}

	return h.WriteJSON(writer, http.StatusOK, severity)
}

// UpdateSeverity handles PATCH /orgs/$org/severities/$identifier.
func (h *Handler) UpdateSeverity(writer http.ResponseWriter, req *http.Request) error {
	var updateReq UpdateSeverityRequest
	if err := json.NewDecoder(req.Body).Decode(&updateReq); err != nil {
		return h.WriteValidationError(writer, "Invalid JSON", []base.ValidationErrorField{
			{Name: "body", Message: "Invalid JSON format"},
		})
	}

	severity, err := h.svc.UpdateSeverity(req.Context(), httpx.Param(req, "org"), httpx.Param(req, "uid"), updateReq)
	if err != nil {
		return h.handleErr(writer, req, err)
	}

	return h.WriteJSON(writer, http.StatusOK, severity)
}

// DeleteSeverity handles DELETE /orgs/$org/severities/$identifier.
func (h *Handler) DeleteSeverity(writer http.ResponseWriter, req *http.Request) error {
	if err := h.svc.DeleteSeverity(req.Context(), httpx.Param(req, "org"), httpx.Param(req, "uid")); err != nil {
		return h.handleErr(writer, req, err)
	}
	writer.WriteHeader(http.StatusNoContent)

	return nil
}

func (h *Handler) handleErr(writer http.ResponseWriter, request *http.Request, err error) error {
	switch {
	case errors.Is(err, ErrOrganizationNotFound):
		return h.WriteErrorErr(
			writer, request, http.StatusNotFound, base.ErrorCodeOrganizationNotFound, "Organization not found", err)
	case errors.Is(err, ErrSeverityNotFound):
		return h.WriteErrorErr(
			writer, request, http.StatusNotFound, base.ErrorCodeSeverityNotFound, "Severity not found", err)
	case errors.Is(err, ErrSlugConflict):
		return h.WriteValidationError(writer, "Slug already exists", []base.ValidationErrorField{
			{Name: "slug", Message: "A severity with this slug already exists in this organization"},
		})
	case errors.Is(err, ErrInvalidSlug):
		return h.WriteValidationError(writer, "Invalid slug format", []base.ValidationErrorField{
			{Name: "slug", Message: "Slug must be 3-100 lowercase letters/digits/hyphens, starting with a letter"},
		})
	case errors.Is(err, ErrInvalidChannel):
		return h.WriteValidationError(writer, "Invalid channel type", []base.ValidationErrorField{
			{Name: "channels", Message: err.Error()},
		})
	case errors.Is(err, ErrCannotDeleteDefault):
		return h.WriteErrorErr(writer, request, http.StatusConflict, base.ErrorCodeSeverityInUse, err.Error(), err)
	default:
		return h.WriteInternalError(writer, request, err)
	}
}
