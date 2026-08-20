package integrations

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/fclairamb/solidping/server/internal/handlers/base"
	"github.com/fclairamb/solidping/server/internal/httpx"
)

// ListIdentities returns every org member's mapping status on an integration.
//
// Route: GET /api/v1/orgs/:org/integrations/:uid/identities.
func (h *Handler) ListIdentities(writer http.ResponseWriter, req *http.Request) error {
	orgSlug := httpx.Param(req, "org")
	integrationUID := httpx.Param(req, "uid")

	resp, err := h.svc.ListIdentities(req.Context(), orgSlug, integrationUID)
	if err != nil {
		return h.handleIdentityError(writer, req, err)
	}

	return h.WriteJSON(writer, http.StatusOK, resp)
}

// SyncIdentities re-runs the email auto-match against the workspace.
//
// Route: POST /api/v1/orgs/:org/integrations/:uid/identities/sync.
func (h *Handler) SyncIdentities(writer http.ResponseWriter, req *http.Request) error {
	orgSlug := httpx.Param(req, "org")
	integrationUID := httpx.Param(req, "uid")

	resp, err := h.svc.SyncIdentities(req.Context(), orgSlug, integrationUID)
	if err != nil {
		return h.handleIdentityError(writer, req, err)
	}

	return h.WriteJSON(writer, http.StatusOK, resp)
}

// SetIdentity records an admin's manual mapping for one member.
//
// Route: PUT /api/v1/orgs/:org/integrations/:uid/identities/:userUid.
func (h *Handler) SetIdentity(writer http.ResponseWriter, req *http.Request) error {
	orgSlug := httpx.Param(req, "org")
	integrationUID := httpx.Param(req, "uid")
	userUID := httpx.Param(req, "userUid")

	var body SetIdentityRequest
	if err := json.NewDecoder(req.Body).Decode(&body); err != nil && !errors.Is(err, io.EOF) {
		return h.WriteValidationError(writer, "Invalid JSON", []base.ValidationErrorField{
			{Name: invalidJSONField, Message: invalidJSONMessage},
		})
	}

	resp, err := h.svc.SetIdentity(req.Context(), orgSlug, integrationUID, userUID, body)
	if err != nil {
		return h.handleIdentityError(writer, req, err)
	}

	return h.WriteJSON(writer, http.StatusOK, resp)
}

// DeleteIdentity clears a member's mapping on an integration.
//
// Route: DELETE /api/v1/orgs/:org/integrations/:uid/identities/:userUid.
func (h *Handler) DeleteIdentity(writer http.ResponseWriter, req *http.Request) error {
	orgSlug := httpx.Param(req, "org")
	integrationUID := httpx.Param(req, "uid")
	userUID := httpx.Param(req, "userUid")

	if err := h.svc.DeleteIdentity(req.Context(), orgSlug, integrationUID, userUID); err != nil {
		return h.handleIdentityError(writer, req, err)
	}

	writer.WriteHeader(http.StatusNoContent)

	return nil
}

// handleIdentityError maps the identity-specific service errors, falling back
// to the shared integration error mapping.
func (h *Handler) handleIdentityError(writer http.ResponseWriter, request *http.Request, err error) error {
	switch {
	case errors.Is(err, ErrIdentitiesUnsupportedType):
		return h.WriteError(writer, http.StatusBadRequest, base.ErrorCodeValidationError,
			"Identity mapping is only available for Slack integrations")
	case errors.Is(err, ErrSlackNotConnected):
		return h.WriteError(writer, http.StatusConflict, base.ErrorCodeChannelNotConnected,
			"Slack integration is not connected — install the Slack app")
	case errors.Is(err, ErrIdentityExternalIDRequired):
		return h.WriteValidationError(writer, "Validation error", []base.ValidationErrorField{
			{Name: "externalId", Message: "externalId is required"},
		})
	case errors.Is(err, ErrIdentityMemberNotFound):
		return h.WriteError(writer, http.StatusNotFound, base.ErrorCodeUserNotFound,
			"User is not a member of this organization")
	case errors.Is(err, ErrIdentityAlreadyClaimed):
		return h.WriteError(writer, http.StatusConflict, base.ErrorCodeConflict,
			"That workspace user is already mapped to another member")
	default:
		return h.handleError(writer, request, err)
	}
}
