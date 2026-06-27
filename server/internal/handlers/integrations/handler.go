package integrations

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/uptrace/bunrouter"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/handlers/base"
)

// Validation-error field/message constants. Kept as constants to satisfy
// goconst and to keep the messages consistent across endpoints.
const (
	invalidJSONField   = "body"
	invalidJSONMessage = "Invalid JSON format"
)

// Handler provides HTTP handlers for connection management endpoints.
type Handler struct {
	base.HandlerBase
	svc *Service
}

// NewHandler creates a new connections handler.
func NewHandler(service *Service, cfg *config.Config) *Handler {
	return &Handler{
		HandlerBase: base.NewHandlerBase(cfg),
		svc:         service,
	}
}

// ListIntegrations handles listing all connections of an organization.
func (h *Handler) ListIntegrations(writer http.ResponseWriter, req bunrouter.Request) error {
	orgSlug := req.Param("org")
	connType := req.URL.Query().Get("type")

	var typeFilter *string
	if connType != "" {
		typeFilter = &connType
	}

	connections, err := h.svc.ListIntegrations(req.Context(), orgSlug, typeFilter)
	if err != nil {
		return h.handleError(writer, err)
	}

	return h.WriteJSON(writer, http.StatusOK, connections)
}

// GetIntegration handles getting a specific connection by UID.
func (h *Handler) GetIntegration(writer http.ResponseWriter, req bunrouter.Request) error {
	orgSlug := req.Param("org")
	connectionUID := req.Param("uid")

	connection, err := h.svc.GetIntegration(req.Context(), orgSlug, connectionUID)
	if err != nil {
		return h.handleError(writer, err)
	}

	return h.WriteJSON(writer, http.StatusOK, connection)
}

// CreateIntegration handles creating a new connection.
func (h *Handler) CreateIntegration(writer http.ResponseWriter, req bunrouter.Request) error {
	orgSlug := req.Param("org")

	var createReq CreateIntegrationRequest
	if err := json.NewDecoder(req.Body).Decode(&createReq); err != nil {
		return h.WriteValidationError(writer, "Invalid JSON", []base.ValidationErrorField{
			{Name: invalidJSONField, Message: invalidJSONMessage},
		})
	}

	// Validate required fields
	var validationErrors []base.ValidationErrorField
	if createReq.Type == "" {
		validationErrors = append(validationErrors, base.ValidationErrorField{
			Name: "type", Message: "Type is required",
		})
	}

	if createReq.Name == "" {
		validationErrors = append(validationErrors, base.ValidationErrorField{
			Name: "name", Message: "Name is required",
		})
	}

	if len(validationErrors) > 0 {
		return h.WriteValidationError(writer, "Validation error", validationErrors)
	}

	connection, err := h.svc.CreateIntegration(req.Context(), orgSlug, createReq)
	if err != nil {
		return h.handleError(writer, err)
	}

	return h.WriteJSON(writer, http.StatusCreated, connection)
}

// UpdateIntegration handles updating a connection.
func (h *Handler) UpdateIntegration(writer http.ResponseWriter, req bunrouter.Request) error {
	orgSlug := req.Param("org")
	connectionUID := req.Param("uid")

	var updateReq UpdateIntegrationRequest
	if err := json.NewDecoder(req.Body).Decode(&updateReq); err != nil {
		return h.WriteValidationError(writer, "Invalid JSON", []base.ValidationErrorField{
			{Name: invalidJSONField, Message: invalidJSONMessage},
		})
	}

	connection, err := h.svc.UpdateIntegration(req.Context(), orgSlug, connectionUID, updateReq)
	if err != nil {
		return h.handleError(writer, err)
	}

	return h.WriteJSON(writer, http.StatusOK, connection)
}

// DeleteIntegration handles deleting a connection.
func (h *Handler) DeleteIntegration(writer http.ResponseWriter, req bunrouter.Request) error {
	orgSlug := req.Param("org")
	connectionUID := req.Param("uid")

	if err := h.svc.DeleteIntegration(req.Context(), orgSlug, connectionUID); err != nil {
		return h.handleError(writer, err)
	}

	writer.WriteHeader(http.StatusNoContent)

	return nil
}

// StartFreeboxPairing kicks off the Freebox LCD-pairing flow. On
// success, returns the new connectionUid + trackId; the dashboard then
// polls GetFreeboxPairingStatus every 2 s until the user approves the
// prompt on the Freebox.
func (h *Handler) StartFreeboxPairing(writer http.ResponseWriter, req bunrouter.Request) error {
	orgSlug := req.Param("org")

	var body StartFreeboxPairingRequest
	if err := json.NewDecoder(req.Body).Decode(&body); err != nil && !errors.Is(err, io.EOF) {
		return h.WriteValidationError(writer, "Invalid JSON", []base.ValidationErrorField{
			{Name: invalidJSONField, Message: invalidJSONMessage},
		})
	}

	resp, err := h.svc.StartFreeboxPairing(req.Context(), orgSlug, body)
	if err != nil {
		return h.handleError(writer, err)
	}

	return h.WriteJSON(writer, http.StatusCreated, resp)
}

// GetFreeboxPairingStatus returns the current Freebox pairing status
// for a channel. The dashboard polls this every ~2 s until the response
// is `granted`, `denied`, or `timeout`.
func (h *Handler) GetFreeboxPairingStatus(writer http.ResponseWriter, req bunrouter.Request) error {
	orgSlug := req.Param("org")
	connectionUID := req.Param("uid")

	resp, err := h.svc.CheckFreeboxPairingStatus(req.Context(), orgSlug, connectionUID)
	if err != nil {
		return h.handleError(writer, err)
	}

	return h.WriteJSON(writer, http.StatusOK, resp)
}

// RotateWebhookSecret rotates the signing secret of a webhook channel. The
// current secret becomes the previous one (valid for a 24 h grace window) and
// a fresh secret is generated. Returns the updated channel.
func (h *Handler) RotateWebhookSecret(writer http.ResponseWriter, req bunrouter.Request) error {
	orgSlug := req.Param("org")
	connectionUID := req.Param("uid")

	connection, err := h.svc.RotateWebhookSecret(req.Context(), orgSlug, connectionUID)
	if err != nil {
		return h.handleError(writer, err)
	}

	return h.WriteJSON(writer, http.StatusOK, connection)
}

// TestIntegration sends a sample notification through the integration and
// reports the outcome. Always returns HTTP 200 for a notifiable integration —
// the caller inspects the `success` field to know whether delivery worked.
func (h *Handler) TestIntegration(writer http.ResponseWriter, req bunrouter.Request) error {
	orgSlug := req.Param("org")
	connectionUID := req.Param("uid")

	result, err := h.svc.TestIntegration(req.Context(), orgSlug, connectionUID)
	if err != nil {
		return h.handleError(writer, err)
	}

	return h.WriteJSON(writer, http.StatusOK, result)
}

// handleError maps service errors to HTTP responses.
func (h *Handler) handleError(writer http.ResponseWriter, err error) error {
	switch {
	case errors.Is(err, ErrOrganizationNotFound):
		return h.WriteError(writer, http.StatusNotFound, base.ErrorCodeOrganizationNotFound, "Organization not found")
	case errors.Is(err, ErrConnectionNotFound):
		return h.WriteError(writer, http.StatusNotFound, base.ErrorCodeIntegrationNotFound, "Connection not found")
	case errors.Is(err, ErrInvalidConnectionType):
		return h.WriteValidationError(writer, "Invalid connection type", []base.ValidationErrorField{
			{Name: "type", Message: "Type must be one of: slack, discord, webhook, email, freebox, kubernetes"},
		})
	case errors.Is(err, ErrFreeboxNotPairing):
		return h.WriteError(writer, http.StatusConflict, base.ErrorCodeConflict,
			"Freebox channel is not in pairing state")
	case errors.Is(err, ErrFreeboxTypeMismatch):
		return h.WriteError(writer, http.StatusBadRequest, base.ErrorCodeValidationError,
			"Channel is not a Freebox connection")
	case errors.Is(err, ErrNotWebhookChannel):
		return h.WriteError(writer, http.StatusBadRequest, base.ErrorCodeValidationError,
			"Channel is not a webhook connection")
	case errors.Is(err, ErrIntegrationNotNotifiable):
		return h.WriteError(writer, http.StatusBadRequest, base.ErrorCodeValidationError,
			"This integration type cannot send notifications")
	case errors.Is(err, ErrSlackManualCreate):
		return h.WriteError(writer, http.StatusBadRequest, base.ErrorCodeValidationError,
			"Slack channels are added by installing the Slack app")
	case errors.Is(err, ErrFreeboxPairingFailed):
		return h.WriteError(writer, http.StatusBadGateway, base.ErrorCodeInternalError, err.Error())
	default:
		return h.WriteInternalError(writer, err)
	}
}
