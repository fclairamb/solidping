package auth

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/uptrace/bunrouter"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/handlers/base"
)

// CreateMembershipRequestHandler — POST /api/v1/auth/membership-requests.
func (h *Handler) CreateMembershipRequestHandler(
	writer http.ResponseWriter, req bunrouter.Request,
) error {
	claims, ok := getClaimsFromContext(req)
	if !ok {
		return h.WriteError(
			writer, http.StatusUnauthorized,
			base.ErrorCodeUnauthorized, "Authentication required",
		)
	}

	var body MembershipRequestCreateRequest
	if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
		return h.WriteValidationError(writer, "Invalid JSON", []base.ValidationErrorField{
			{Name: fieldBody, Message: msgInvalidJSON},
		})
	}

	if body.OrgSlug == "" {
		return h.WriteValidationError(writer, "Validation error", []base.ValidationErrorField{
			{Name: "orgSlug", Message: "orgSlug is required"},
		})
	}

	resp, err := h.svc.CreateMembershipRequest(req.Context(), claims.UserUID, body)
	if err != nil {
		return h.writeMembershipRequestError(writer, err)
	}

	return h.WriteJSON(writer, http.StatusCreated, resp)
}

// ListOwnMembershipRequestsHandler — GET /api/v1/auth/membership-requests.
func (h *Handler) ListOwnMembershipRequestsHandler(
	writer http.ResponseWriter, req bunrouter.Request,
) error {
	claims, ok := getClaimsFromContext(req)
	if !ok {
		return h.WriteError(
			writer, http.StatusUnauthorized,
			base.ErrorCodeUnauthorized, "Authentication required",
		)
	}

	resp, err := h.svc.ListOwnMembershipRequests(req.Context(), claims.UserUID)
	if err != nil {
		return h.WriteInternalError(writer, err)
	}

	return h.WriteJSON(writer, http.StatusOK, resp)
}

// CancelMembershipRequestHandler — DELETE /api/v1/auth/membership-requests/{uid}.
func (h *Handler) CancelMembershipRequestHandler(
	writer http.ResponseWriter, req bunrouter.Request,
) error {
	claims, ok := getClaimsFromContext(req)
	if !ok {
		return h.WriteError(
			writer, http.StatusUnauthorized,
			base.ErrorCodeUnauthorized, "Authentication required",
		)
	}

	requestUID := req.Param("uid")
	if err := h.svc.CancelMembershipRequest(req.Context(), claims.UserUID, requestUID); err != nil {
		return h.writeMembershipRequestError(writer, err)
	}

	writer.WriteHeader(http.StatusNoContent)

	return nil
}

// ListOrgMembershipRequestsHandler — GET /api/v1/orgs/{org}/membership-requests.
func (h *Handler) ListOrgMembershipRequestsHandler(
	writer http.ResponseWriter, req bunrouter.Request,
) error {
	claims, ok := getClaimsFromContext(req)
	if !ok {
		return h.WriteError(
			writer, http.StatusUnauthorized,
			base.ErrorCodeUnauthorized, "Authentication required",
		)
	}

	if claims.Role != roleAdmin && claims.Role != RoleSuperAdmin {
		return h.WriteError(
			writer, http.StatusForbidden,
			base.ErrorCodeForbidden, "Admin access required",
		)
	}

	orgSlug := req.Param("org")
	status := models.MembershipRequestStatus(req.URL.Query().Get("status"))

	resp, err := h.svc.ListOrgMembershipRequests(req.Context(), orgSlug, status)
	if err != nil {
		return h.writeMembershipRequestError(writer, err)
	}

	return h.WriteJSON(writer, http.StatusOK, resp)
}

// ApproveMembershipRequestHandler — POST .../approve.
func (h *Handler) ApproveMembershipRequestHandler(
	writer http.ResponseWriter, req bunrouter.Request,
) error {
	claims, ok := getClaimsFromContext(req)
	if !ok {
		return h.WriteError(
			writer, http.StatusUnauthorized,
			base.ErrorCodeUnauthorized, "Authentication required",
		)
	}

	if claims.Role != roleAdmin && claims.Role != RoleSuperAdmin {
		return h.WriteError(
			writer, http.StatusForbidden,
			base.ErrorCodeForbidden, "Admin access required",
		)
	}

	var body MembershipRequestApproveRequest
	if req.ContentLength > 0 {
		if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
			return h.WriteValidationError(writer, "Invalid JSON", []base.ValidationErrorField{
				{Name: fieldBody, Message: msgInvalidJSON},
			})
		}
	}

	orgSlug := req.Param("org")
	requestUID := req.Param("uid")

	if err := h.svc.ApproveMembershipRequest(
		req.Context(), claims.UserUID, orgSlug, requestUID, body.Role,
	); err != nil {
		return h.writeMembershipRequestError(writer, err)
	}

	writer.WriteHeader(http.StatusOK)

	return nil
}

// RejectMembershipRequestHandler — POST .../reject.
func (h *Handler) RejectMembershipRequestHandler(
	writer http.ResponseWriter, req bunrouter.Request,
) error {
	claims, ok := getClaimsFromContext(req)
	if !ok {
		return h.WriteError(
			writer, http.StatusUnauthorized,
			base.ErrorCodeUnauthorized, "Authentication required",
		)
	}

	if claims.Role != roleAdmin && claims.Role != RoleSuperAdmin {
		return h.WriteError(
			writer, http.StatusForbidden,
			base.ErrorCodeForbidden, "Admin access required",
		)
	}

	var body MembershipRequestRejectRequest
	if req.ContentLength > 0 {
		if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
			return h.WriteValidationError(writer, "Invalid JSON", []base.ValidationErrorField{
				{Name: fieldBody, Message: msgInvalidJSON},
			})
		}
	}

	orgSlug := req.Param("org")
	requestUID := req.Param("uid")

	if err := h.svc.RejectMembershipRequest(
		req.Context(), claims.UserUID, orgSlug, requestUID, body.Reason,
	); err != nil {
		return h.writeMembershipRequestError(writer, err)
	}

	writer.WriteHeader(http.StatusOK)

	return nil
}

// writeMembershipRequestError translates the service errors to HTTP.
func (h *Handler) writeMembershipRequestError(
	writer http.ResponseWriter, err error,
) error {
	switch {
	case errors.Is(err, ErrOrganizationNotFound):
		return h.WriteErrorErr(
			writer, http.StatusNotFound,
			base.ErrorCodeOrganizationNotFound, "Organization not found", err,
		)
	case errors.Is(err, ErrAlreadyAMember):
		return h.WriteErrorErr(
			writer, http.StatusConflict,
			base.ErrorCodeAlreadyAMember, "Already a member", err,
		)
	case errors.Is(err, ErrRequestPending):
		return h.WriteErrorErr(
			writer, http.StatusConflict,
			base.ErrorCodeRequestPending, "A request is already pending", err,
		)
	case errors.Is(err, ErrRequestNotFound):
		return h.WriteErrorErr(
			writer, http.StatusNotFound,
			base.ErrorCodeRequestNotFound, "Membership request not found", err,
		)
	case errors.Is(err, ErrRequestCooldownActive):
		return h.WriteErrorErr(
			writer, http.StatusConflict,
			base.ErrorCodeRequestCooldownActive,
			"A previous request was rejected — please wait before re-requesting",
			err,
		)
	default:
		return h.WriteInternalError(writer, err)
	}
}
