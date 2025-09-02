package checkgroups

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/uptrace/bunrouter"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/handlers/base"
)

// Handler provides HTTP handlers for check group management endpoints.
type Handler struct {
	base.HandlerBase
	svc *Service
}

// NewHandler creates a new check groups handler.
func NewHandler(service *Service, cfg *config.Config) *Handler {
	return &Handler{
		HandlerBase: base.NewHandlerBase(cfg),
		svc:         service,
	}
}

// ListCheckGroups handles listing all check groups for an organization.
func (h *Handler) ListCheckGroups(writer http.ResponseWriter, req bunrouter.Request) error {
	orgSlug := req.Param("org")

	groups, err := h.svc.ListCheckGroups(req.Context(), orgSlug)
	if err != nil {
		return h.handleOrgError(writer, err)
	}

	return h.WriteJSON(writer, http.StatusOK, map[string]interface{}{
		"data": groups,
	})
}

// CreateCheckGroup handles creating a new check group.
func (h *Handler) CreateCheckGroup(writer http.ResponseWriter, req bunrouter.Request) error {
	orgSlug := req.Param("org")

	var createReq CreateCheckGroupRequest
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

	group, err := h.svc.CreateCheckGroup(req.Context(), orgSlug, createReq)
	if err != nil {
		return h.handleCreateError(writer, err)
	}

	return h.WriteJSON(writer, http.StatusCreated, group)
}

// GetCheckGroup handles retrieving a single check group by UID or slug.
func (h *Handler) GetCheckGroup(writer http.ResponseWriter, req bunrouter.Request) error {
	orgSlug := req.Param("org")
	identifier := req.Param("uid")

	group, err := h.svc.GetCheckGroup(req.Context(), orgSlug, identifier)
	if err != nil {
		return h.handleGroupError(writer, err)
	}

	return h.WriteJSON(writer, http.StatusOK, group)
}

// UpdateCheckGroup handles updating an existing check group.
func (h *Handler) UpdateCheckGroup(writer http.ResponseWriter, req bunrouter.Request) error {
	orgSlug := req.Param("org")
	identifier := req.Param("uid")

	var updateReq UpdateCheckGroupRequest
	if err := json.NewDecoder(req.Body).Decode(&updateReq); err != nil {
		return h.WriteValidationError(writer, "Invalid JSON", []base.ValidationErrorField{
			{Name: "body", Message: "Invalid JSON format"},
		})
	}

	group, err := h.svc.UpdateCheckGroup(req.Context(), orgSlug, identifier, updateReq)
	if err != nil {
		return h.handleUpdateError(writer, err)
	}

	return h.WriteJSON(writer, http.StatusOK, group)
}

// DeleteCheckGroup handles deleting a check group by UID or slug.
func (h *Handler) DeleteCheckGroup(writer http.ResponseWriter, req bunrouter.Request) error {
	orgSlug := req.Param("org")
	identifier := req.Param("uid")

	if err := h.svc.DeleteCheckGroup(req.Context(), orgSlug, identifier); err != nil {
		return h.handleGroupError(writer, err)
	}

	writer.WriteHeader(http.StatusNoContent)

	return nil
}

func (h *Handler) handleOrgError(writer http.ResponseWriter, err error) error {
	if errors.Is(err, ErrOrganizationNotFound) {
		return h.WriteErrorErr(
			writer, http.StatusNotFound, base.ErrorCodeOrganizationNotFound, "Organization not found", err)
	}

	return h.WriteInternalError(writer, err)
}

func (h *Handler) handleGroupError(writer http.ResponseWriter, err error) error {
	switch {
	case errors.Is(err, ErrOrganizationNotFound):
		return h.WriteErrorErr(
			writer, http.StatusNotFound, base.ErrorCodeOrganizationNotFound, "Organization not found", err)
	case errors.Is(err, ErrCheckGroupNotFound):
		return h.WriteErrorErr(
			writer, http.StatusNotFound, base.ErrorCodeCheckGroupNotFound, "Check group not found", err)
	default:
		return h.WriteInternalError(writer, err)
	}
}

func (h *Handler) handleCreateError(writer http.ResponseWriter, err error) error {
	switch {
	case errors.Is(err, ErrOrganizationNotFound):
		return h.WriteErrorErr(
			writer, http.StatusNotFound, base.ErrorCodeOrganizationNotFound, "Organization not found", err)
	case errors.Is(err, ErrSlugConflict):
		return h.WriteValidationError(writer, "Slug already exists", []base.ValidationErrorField{
			{Name: "slug", Message: "A check group with this slug already exists in this organization"},
		})
	case errors.Is(err, ErrInvalidSlugFormat):
		return h.WriteValidationError(writer, "Invalid slug format", []base.ValidationErrorField{
			{
				Name: "slug",
				Message: "Slug must start with a lowercase letter, be 3-40 characters, " +
					"and contain only lowercase letters, digits, or hyphens. UUIDs are not allowed.",
			},
		})
	default:
		return h.WriteInternalError(writer, err)
	}
}

func (h *Handler) handleUpdateError(writer http.ResponseWriter, err error) error {
	switch {
	case errors.Is(err, ErrOrganizationNotFound):
		return h.WriteErrorErr(
			writer, http.StatusNotFound, base.ErrorCodeOrganizationNotFound, "Organization not found", err)
	case errors.Is(err, ErrCheckGroupNotFound):
		return h.WriteErrorErr(
			writer, http.StatusNotFound, base.ErrorCodeCheckGroupNotFound, "Check group not found", err)
	case errors.Is(err, ErrSlugConflict):
		return h.WriteValidationError(writer, "Slug already exists", []base.ValidationErrorField{
			{Name: "slug", Message: "A check group with this slug already exists in this organization"},
		})
	case errors.Is(err, ErrInvalidSlugFormat):
		return h.WriteValidationError(writer, "Invalid slug format", []base.ValidationErrorField{
			{
				Name: "slug",
				Message: "Slug must start with a lowercase letter, be 3-40 characters, " +
					"and contain only lowercase letters, digits, or hyphens. UUIDs are not allowed.",
			},
		})
	default:
		return h.WriteInternalError(writer, err)
	}
}
