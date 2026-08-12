package members

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/handlers/base"
	"github.com/fclairamb/solidping/server/internal/httpx"
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
func (h *Handler) ListMembers(writer http.ResponseWriter, req *http.Request) error {
	orgSlug := httpx.Param(req, "org")

	members, err := h.svc.ListMembers(req.Context(), orgSlug)
	if err != nil {
		return h.handleError(writer, err)
	}

	return h.WriteJSON(writer, http.StatusOK, members)
}

// ListCoverage handles listing per-member paging coverage.
//
// Route: GET /api/v1/orgs/:org/members/coverage (admin).
func (h *Handler) ListCoverage(writer http.ResponseWriter, req *http.Request) error {
	orgSlug := httpx.Param(req, "org")

	coverage, err := h.svc.ListCoverage(req.Context(), orgSlug)
	if err != nil {
		return h.handleError(writer, err)
	}

	return h.WriteJSON(writer, http.StatusOK, coverage)
}

// AddMemberContact pre-provisions an UNVERIFIED paging contact for a member.
//
// Route: POST /api/v1/orgs/:org/members/:uid/contacts (admin).
func (h *Handler) AddMemberContact(writer http.ResponseWriter, req *http.Request) error {
	orgSlug := httpx.Param(req, "org")
	memberUID := httpx.Param(req, "uid")

	var body AdminAddContactRequest
	if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
		return h.WriteValidationError(writer, "Invalid JSON", []base.ValidationErrorField{
			{Name: "body", Message: "Invalid JSON format"},
		})
	}

	contact, err := h.svc.AddMemberContact(req.Context(), orgSlug, memberUID, body)
	if err != nil {
		return h.handleError(writer, err)
	}

	return h.WriteJSON(writer, http.StatusCreated, contact)
}

// SendPagingNudge emails a member asking them to finish their paging setup.
//
// Route: POST /api/v1/orgs/:org/members/:uid/paging-nudge (admin).
func (h *Handler) SendPagingNudge(writer http.ResponseWriter, req *http.Request) error {
	orgSlug := httpx.Param(req, "org")
	memberUID := httpx.Param(req, "uid")

	if err := h.svc.SendPagingNudge(req.Context(), orgSlug, memberUID); err != nil {
		return h.handleError(writer, err)
	}

	writer.WriteHeader(http.StatusNoContent)

	return nil
}

// GetMember handles getting a specific member by UID.
func (h *Handler) GetMember(writer http.ResponseWriter, req *http.Request) error {
	orgSlug := httpx.Param(req, "org")
	memberUID := httpx.Param(req, "uid")

	member, err := h.svc.GetMember(req.Context(), orgSlug, memberUID)
	if err != nil {
		return h.handleError(writer, err)
	}

	return h.WriteJSON(writer, http.StatusOK, member)
}

// AddMember handles adding a new member to the organization.
func (h *Handler) AddMember(writer http.ResponseWriter, req *http.Request) error {
	orgSlug := httpx.Param(req, "org")

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

	member, err := h.svc.AddMember(req.Context(), orgSlug, addReq, inviterUID, callerFrom(req))
	if err != nil {
		return h.handleError(writer, err)
	}

	return h.WriteJSON(writer, http.StatusCreated, member)
}

// UpdateMember handles updating a member's role.
func (h *Handler) UpdateMember(writer http.ResponseWriter, req *http.Request) error {
	orgSlug := httpx.Param(req, "org")
	memberUID := httpx.Param(req, "uid")

	var updateReq UpdateMemberRequest
	if err := json.NewDecoder(req.Body).Decode(&updateReq); err != nil {
		return h.WriteValidationError(writer, "Invalid JSON", []base.ValidationErrorField{
			{Name: "body", Message: "Invalid JSON format"},
		})
	}

	member, err := h.svc.UpdateMember(req.Context(), orgSlug, memberUID, updateReq, callerFrom(req))
	if err != nil {
		return h.handleError(writer, err)
	}

	return h.WriteJSON(writer, http.StatusOK, member)
}

// RemoveMember handles removing a member from the organization.
func (h *Handler) RemoveMember(writer http.ResponseWriter, req *http.Request) error {
	orgSlug := httpx.Param(req, "org")
	memberUID := httpx.Param(req, "uid")

	if err := h.svc.RemoveMember(req.Context(), orgSlug, memberUID, callerFrom(req)); err != nil {
		return h.handleError(writer, err)
	}

	writer.WriteHeader(http.StatusNoContent)

	return nil
}

// callerFrom builds the member-management Caller from the authenticated request
// context. It carries identity only — the caller's *role* is re-read from the
// membership row inside the service, never taken from the (possibly stale) JWT
// role claim.
func callerFrom(req *http.Request) Caller {
	user, ok := middleware.GetUserFromContext(req.Context())
	if !ok {
		return Caller{}
	}

	return Caller{UserUID: user.UID, SuperAdmin: user.SuperAdmin}
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
	case errors.Is(err, ErrCannotRemoveLastOwner):
		return h.WriteError(writer, http.StatusConflict, base.ErrorCodeConflict,
			"Cannot remove the last owner from the organization")
	case errors.Is(err, ErrCannotDemoteLastOwner):
		return h.WriteError(writer, http.StatusConflict, base.ErrorCodeConflict, "Cannot demote the last owner")
	case errors.Is(err, ErrOwnerRoleRequired):
		return h.WriteError(writer, http.StatusForbidden, base.ErrorCodeForbidden,
			"Only an owner can grant, revoke or remove ownership")
	case errors.Is(err, ErrInvalidRole):
		return h.WriteValidationError(writer, "Invalid role", []base.ValidationErrorField{
			{Name: "role", Message: "Role must be one of: owner, admin, user, viewer"},
		})
	case errors.Is(err, ErrContactTypeNotProvisionable):
		return h.WriteValidationError(writer, "Validation error", []base.ValidationErrorField{
			{Name: "type", Message: err.Error()},
		})
	case errors.Is(err, ErrInvalidContactValue):
		return h.WriteValidationError(writer, "Validation error", []base.ValidationErrorField{
			{Name: "value", Message: err.Error()},
		})
	case errors.Is(err, ErrContactAlreadyExists):
		return h.WriteError(writer, http.StatusConflict, base.ErrorCodeConflict, err.Error())
	case errors.Is(err, ErrEmailSenderNotConfigured):
		return h.WriteError(writer, http.StatusBadRequest, base.ErrorCodeValidationError, err.Error())
	default:
		return h.WriteInternalError(writer, err)
	}
}
