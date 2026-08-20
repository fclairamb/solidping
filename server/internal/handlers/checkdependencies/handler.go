package checkdependencies

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/handlers/base"
	"github.com/fclairamb/solidping/server/internal/httpx"
)

// Handler exposes the check-dependency endpoints.
type Handler struct {
	base.HandlerBase

	svc *Service
}

// NewHandler builds the handler.
func NewHandler(svc *Service, cfg *config.Config) *Handler {
	return &Handler{
		HandlerBase: base.NewHandlerBase(cfg),
		svc:         svc,
	}
}

// ListForCheck handles GET /orgs/:org/checks/:check/dependencies.
func (h *Handler) ListForCheck(writer http.ResponseWriter, req *http.Request) error {
	orgSlug := httpx.Param(req, "org")
	check := httpx.Param(req, "check")

	deps, err := h.svc.ListForCheck(req.Context(), orgSlug, check)
	if err != nil {
		return h.handleError(writer, req, err)
	}

	return h.WriteJSON(writer, http.StatusOK, map[string]any{"data": deps})
}

// Create handles POST /orgs/:org/checks/:check/dependencies.
func (h *Handler) Create(writer http.ResponseWriter, req *http.Request) error {
	orgSlug := httpx.Param(req, "org")
	check := httpx.Param(req, "check")

	var body CreateDependencyRequest
	if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
		return h.WriteValidationError(writer, "Invalid JSON", []base.ValidationErrorField{
			{Name: "body", Message: "Invalid JSON format"},
		})
	}

	dep, err := h.svc.Create(req.Context(), orgSlug, check, body)
	if err != nil {
		return h.handleError(writer, req, err)
	}

	return h.WriteJSON(writer, http.StatusCreated, dep)
}

// Update handles PATCH /orgs/:org/checks/:check/dependencies/:uid.
func (h *Handler) Update(writer http.ResponseWriter, req *http.Request) error {
	orgSlug := httpx.Param(req, "org")
	depUID := httpx.Param(req, "uid")

	var body UpdateDependencyRequest
	if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
		return h.WriteValidationError(writer, "Invalid JSON", []base.ValidationErrorField{
			{Name: "body", Message: "Invalid JSON format"},
		})
	}

	dep, err := h.svc.Update(req.Context(), orgSlug, depUID, body)
	if err != nil {
		return h.handleError(writer, req, err)
	}

	return h.WriteJSON(writer, http.StatusOK, dep)
}

// Delete handles DELETE /orgs/:org/checks/:check/dependencies/:uid.
func (h *Handler) Delete(writer http.ResponseWriter, req *http.Request) error {
	orgSlug := httpx.Param(req, "org")
	depUID := httpx.Param(req, "uid")

	if err := h.svc.Delete(req.Context(), orgSlug, depUID); err != nil {
		return h.handleError(writer, req, err)
	}

	writer.WriteHeader(http.StatusNoContent)

	return nil
}

// Graph handles GET /orgs/:org/dependencies.
func (h *Handler) Graph(writer http.ResponseWriter, req *http.Request) error {
	orgSlug := httpx.Param(req, "org")

	graph, err := h.svc.Graph(req.Context(), orgSlug)
	if err != nil {
		return h.handleError(writer, req, err)
	}

	return h.WriteJSON(writer, http.StatusOK, map[string]any{"data": graph})
}

func (h *Handler) handleError(writer http.ResponseWriter, request *http.Request, err error) error {
	switch {
	case errors.Is(err, ErrOrganizationNotFound):
		return h.WriteErrorErr(
			writer, request, http.StatusNotFound, base.ErrorCodeOrganizationNotFound, "Organization not found", err)
	case errors.Is(err, ErrCheckNotFound):
		return h.WriteErrorErr(
			writer, request, http.StatusNotFound, base.ErrorCodeCheckNotFound, "Check not found", err)
	case errors.Is(err, ErrDependencyNotFound):
		return h.WriteErrorErr(
			writer, request, http.StatusNotFound, base.ErrorCodeDependencyNotFound, "Dependency not found", err)
	case errors.Is(err, ErrSelfEdge):
		return h.WriteErrorErr(
			writer, request, http.StatusBadRequest, base.ErrorCodeDependencySelf,
			"A check cannot depend on itself", err)
	case errors.Is(err, ErrCrossOrg):
		return h.WriteErrorErr(
			writer, request, http.StatusBadRequest, base.ErrorCodeDependencyCrossOrg,
			"Parent check belongs to a different organization", err)
	case errors.Is(err, ErrCycle):
		return h.WriteErrorErr(
			writer, request, http.StatusBadRequest, base.ErrorCodeDependencyCycle,
			"Adding this dependency would create a cycle", err)
	case errors.Is(err, ErrDuplicate):
		return h.WriteErrorErr(
			writer, request, http.StatusConflict, base.ErrorCodeDependencyDuplicate,
			"Dependency already exists", err)
	case errors.Is(err, ErrInvalidKind):
		return h.WriteErrorErr(
			writer, request, http.StatusBadRequest, base.ErrorCodeDependencyInvalidKind,
			"Dependency kind must be 'hard' or 'soft'", err)
	default:
		return h.WriteInternalError(writer, request, err)
	}
}
