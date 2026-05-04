package checkdependencies

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/uptrace/bunrouter"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/handlers/base"
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
func (h *Handler) ListForCheck(w http.ResponseWriter, req bunrouter.Request) error {
	orgSlug := req.Param("org")
	check := req.Param("check")

	deps, err := h.svc.ListForCheck(req.Context(), orgSlug, check)
	if err != nil {
		return h.handleError(w, err)
	}

	return h.WriteJSON(w, http.StatusOK, map[string]any{"data": deps})
}

// Create handles POST /orgs/:org/checks/:check/dependencies.
func (h *Handler) Create(w http.ResponseWriter, req bunrouter.Request) error {
	orgSlug := req.Param("org")
	check := req.Param("check")

	var body CreateDependencyRequest
	if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
		return h.WriteValidationError(w, "Invalid JSON", []base.ValidationErrorField{
			{Name: "body", Message: "Invalid JSON format"},
		})
	}

	dep, err := h.svc.Create(req.Context(), orgSlug, check, body)
	if err != nil {
		return h.handleError(w, err)
	}

	return h.WriteJSON(w, http.StatusCreated, dep)
}

// Update handles PATCH /orgs/:org/checks/:check/dependencies/:uid.
func (h *Handler) Update(w http.ResponseWriter, req bunrouter.Request) error {
	orgSlug := req.Param("org")
	depUID := req.Param("uid")

	var body UpdateDependencyRequest
	if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
		return h.WriteValidationError(w, "Invalid JSON", []base.ValidationErrorField{
			{Name: "body", Message: "Invalid JSON format"},
		})
	}

	dep, err := h.svc.Update(req.Context(), orgSlug, depUID, body)
	if err != nil {
		return h.handleError(w, err)
	}

	return h.WriteJSON(w, http.StatusOK, dep)
}

// Delete handles DELETE /orgs/:org/checks/:check/dependencies/:uid.
func (h *Handler) Delete(w http.ResponseWriter, req bunrouter.Request) error {
	orgSlug := req.Param("org")
	depUID := req.Param("uid")

	if err := h.svc.Delete(req.Context(), orgSlug, depUID); err != nil {
		return h.handleError(w, err)
	}

	w.WriteHeader(http.StatusNoContent)

	return nil
}

// Graph handles GET /orgs/:org/dependencies.
func (h *Handler) Graph(w http.ResponseWriter, req bunrouter.Request) error {
	orgSlug := req.Param("org")

	graph, err := h.svc.Graph(req.Context(), orgSlug)
	if err != nil {
		return h.handleError(w, err)
	}

	return h.WriteJSON(w, http.StatusOK, map[string]any{"data": graph})
}

func (h *Handler) handleError(w http.ResponseWriter, err error) error {
	switch {
	case errors.Is(err, ErrOrganizationNotFound):
		return h.WriteErrorErr(
			w, http.StatusNotFound, base.ErrorCodeOrganizationNotFound, "Organization not found", err)
	case errors.Is(err, ErrCheckNotFound):
		return h.WriteErrorErr(
			w, http.StatusNotFound, base.ErrorCodeCheckNotFound, "Check not found", err)
	case errors.Is(err, ErrDependencyNotFound):
		return h.WriteErrorErr(
			w, http.StatusNotFound, base.ErrorCodeDependencyNotFound, "Dependency not found", err)
	case errors.Is(err, ErrSelfEdge):
		return h.WriteErrorErr(
			w, http.StatusBadRequest, base.ErrorCodeDependencySelf,
			"A check cannot depend on itself", err)
	case errors.Is(err, ErrCrossOrg):
		return h.WriteErrorErr(
			w, http.StatusBadRequest, base.ErrorCodeDependencyCrossOrg,
			"Parent check belongs to a different organization", err)
	case errors.Is(err, ErrCycle):
		return h.WriteErrorErr(
			w, http.StatusBadRequest, base.ErrorCodeDependencyCycle,
			"Adding this dependency would create a cycle", err)
	case errors.Is(err, ErrDuplicate):
		return h.WriteErrorErr(
			w, http.StatusConflict, base.ErrorCodeDependencyDuplicate,
			"Dependency already exists", err)
	case errors.Is(err, ErrInvalidKind):
		return h.WriteErrorErr(
			w, http.StatusBadRequest, base.ErrorCodeDependencyInvalidKind,
			"Dependency kind must be 'hard' or 'soft'", err)
	default:
		return h.WriteInternalError(w, err)
	}
}
