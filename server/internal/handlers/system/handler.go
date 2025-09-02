package system

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/uptrace/bunrouter"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/handlers/base"
)

// Handler provides HTTP handlers for system parameter endpoints.
type Handler struct {
	base.HandlerBase
	svc *Service
}

// NewHandler creates a new system handler.
func NewHandler(service *Service, cfg *config.Config) *Handler {
	return &Handler{
		HandlerBase: base.NewHandlerBase(cfg),
		svc:         service,
	}
}

// ListParameters handles GET /api/v1/system/parameters.
func (h *Handler) ListParameters(writer http.ResponseWriter, req bunrouter.Request) error {
	params, err := h.svc.ListParameters(req.Context())
	if err != nil {
		return h.handleError(writer, err)
	}

	return h.WriteJSON(writer, http.StatusOK, params)
}

// GetParameter handles GET /api/v1/system/parameters/:key.
func (h *Handler) GetParameter(writer http.ResponseWriter, req bunrouter.Request) error {
	key := req.Param("key")

	param, err := h.svc.GetParameter(req.Context(), key)
	if err != nil {
		return h.handleError(writer, err)
	}

	return h.WriteJSON(writer, http.StatusOK, param)
}

// SetParameter handles PUT /api/v1/system/parameters/:key.
func (h *Handler) SetParameter(writer http.ResponseWriter, req bunrouter.Request) error {
	key := req.Param("key")

	var setReq SetParameterRequest
	if err := json.NewDecoder(req.Body).Decode(&setReq); err != nil {
		return h.WriteValidationError(writer, "Invalid JSON", []base.ValidationErrorField{
			{Name: "body", Message: "Invalid JSON format"},
		})
	}

	// Default secret to false if not provided
	secret := false
	if setReq.Secret != nil {
		secret = *setReq.Secret
	}

	param, err := h.svc.SetParameter(req.Context(), key, setReq.Value, secret)
	if err != nil {
		return h.handleError(writer, err)
	}

	return h.WriteJSON(writer, http.StatusOK, param)
}

// DeleteParameter handles DELETE /api/v1/system/parameters/:key.
func (h *Handler) DeleteParameter(writer http.ResponseWriter, req bunrouter.Request) error {
	key := req.Param("key")

	if err := h.svc.DeleteParameter(req.Context(), key); err != nil {
		return h.handleError(writer, err)
	}

	writer.WriteHeader(http.StatusNoContent)

	return nil
}

// TestEmail handles POST /api/v1/system/test-email.
func (h *Handler) TestEmail(writer http.ResponseWriter, req bunrouter.Request) error {
	var body TestEmailRequest
	if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
		return h.WriteValidationError(writer, "Invalid JSON", []base.ValidationErrorField{
			{Name: "body", Message: "Invalid JSON format"},
		})
	}

	if body.Recipient == "" {
		return h.WriteValidationError(writer, "Recipient is required", []base.ValidationErrorField{
			{Name: "recipient", Message: "Recipient email address is required"},
		})
	}

	result, err := h.svc.TestEmail(req.Context(), body.Recipient)
	if err != nil {
		return h.WriteInternalError(writer, err)
	}

	return h.WriteJSON(writer, http.StatusOK, result)
}

// handleError translates service errors to HTTP responses.
func (h *Handler) handleError(writer http.ResponseWriter, err error) error {
	switch {
	case errors.Is(err, ErrParameterNotFound):
		return h.WriteError(writer, http.StatusNotFound, base.ErrorCodeNotFound, "Parameter not found")
	default:
		return h.WriteInternalError(writer, err)
	}
}
