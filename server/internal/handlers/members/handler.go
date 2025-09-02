package members

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/uptrace/bunrouter"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/handlers/base"
	"github.com/fclairamb/solidping/server/internal/middleware"
)

// Handler provides HTTP handlers for member management endpoints.
type Handler struct {
	base.HandlerBase
	svc *Service
}

// NewHandler creates a new members handler.
func NewHandler(service *Service, cfg *config.Config) *Handler {
	return &Handler{
		HandlerBase: base.NewHandlerBase(cfg),
		svc:         service,
	}
}

// ListMembers handles listing all members of an organization.
func (h *Handler) ListMembers(writer http.ResponseWriter, req bunrouter.Request) error {
	orgSlug := req.Param("org")

	members, err := h.svc.ListMembers(req.Context(), orgSlug)
	if err != nil {
		return h.handleError(writer, err)
	}

	return h.WriteJSON(writer, http.StatusOK, members)
}

// GetMember handles getting a specific member by UID.
func (h *Handler) GetMember(writer http.ResponseWriter, req bunrouter.Request) error {
	orgSlug := req.Param("org")
	memberUID := req.Param("uid")

	member, err := h.svc.GetMember(req.Context(), orgSlug, memberUID)
	if err != nil {
		return h.handleError(writer, err)
	}

	return h.WriteJSON(writer, http.StatusOK, member)
}

// AddMember handles adding a new member to the organization.
func (h *Handler) AddMember(writer http.ResponseWriter, req bunrouter.Request) error {
	orgSlug := req.Param("org")

	var addReq AddMemberRequest
	if err := json.NewDecoder(req.Body).Decode(&addReq); err != nil {
		return h.WriteValidationError(writer, "Invalid JSON", []base.ValidationErrorField{
			{Name: "body", Message: "Invalid JSON format"},
		})
	}

	// Validate required fields
	var validationErrors []base.ValidationErrorField
	if addReq.Email == "" {
		validationErrors = append(validationErrors, base.ValidationErrorField{
			Name: "email", Message: "Email is required",
		})
	}

	if addReq.Role == "" {
		validationErrors = append(validationErrors, base.ValidationErrorField{
			Name: "role", Message: "Role is required",
		})
	}

	if len(validationErrors) > 0 {
		return h.WriteValidationError(writer, "Validation error", validationErrors)
	}

	// Get inviter UID from context
	var inviterUID *string
	if user, ok := middleware.GetUserFromContext(req.Context()); ok {
		inviterUID = &user.UID
	}

	member, err := h.svc.AddMember(req.Context(), orgSlug, addReq, inviterUID)
	if err != nil {
		return h.handleError(writer, err)
	}

	return h.WriteJSON(writer, http.StatusCreated, member)
}

// UpdateMember handles updating a member's role.
func (h *Handler) UpdateMember(writer http.ResponseWriter, req bunrouter.Request) error {
	orgSlug := req.Param("org")
	memberUID := req.Param("uid")

	var updateReq UpdateMemberRequest
	if err := json.NewDecoder(req.Body).Decode(&updateReq); err != nil {
		return h.WriteValidationError(writer, "Invalid JSON", []base.ValidationErrorField{
			{Name: "body", Message: "Invalid JSON format"},
		})
	}

	member, err := h.svc.UpdateMember(req.Context(), orgSlug, memberUID, updateReq)
	if err != nil {
		return h.handleError(writer, err)
	}

	return h.WriteJSON(writer, http.StatusOK, member)
}

// RemoveMember handles removing a member from the organization.
func (h *Handler) RemoveMember(writer http.ResponseWriter, req bunrouter.Request) error {
	orgSlug := req.Param("org")
	memberUID := req.Param("uid")

	if err := h.svc.RemoveMember(req.Context(), orgSlug, memberUID); err != nil {
		return h.handleError(writer, err)
	}

	writer.WriteHeader(http.StatusNoContent)

	return nil
}

// handleError maps service errors to HTTP responses.
func (h *Handler) handleError(writer http.ResponseWriter, err error) error {
	switch {
	case errors.Is(err, ErrOrganizationNotFound):
		return h.WriteError(writer, http.StatusNotFound, base.ErrorCodeOrganizationNotFound, "Organization not found")
	case errors.Is(err, ErrMemberNotFound):
		return h.WriteError(writer, http.StatusNotFound, base.ErrorCodeNotFound, "Member not found")
	case errors.Is(err, ErrUserNotFound):
		return h.WriteError(writer, http.StatusNotFound, base.ErrorCodeUserNotFound, "User not found")
	case errors.Is(err, ErrAlreadyMember):
		return h.WriteError(writer, http.StatusConflict, base.ErrorCodeConflict,
			"User is already a member of this organization")
	case errors.Is(err, ErrCannotRemoveLastAdmin):
		return h.WriteError(writer, http.StatusConflict, base.ErrorCodeConflict,
			"Cannot remove the last admin from the organization")
	case errors.Is(err, ErrCannotDemoteLastAdmin):
		return h.WriteError(writer, http.StatusConflict, base.ErrorCodeConflict, "Cannot demote the last admin")
	case errors.Is(err, ErrInvalidRole):
		return h.WriteValidationError(writer, "Invalid role", []base.ValidationErrorField{
			{Name: "role", Message: "Role must be one of: admin, user, viewer"},
		})
	default:
		return h.WriteInternalError(writer, err)
	}
}
