// Package heartbeat provides HTTP handlers for heartbeat check ingestion.
package heartbeat

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/uptrace/bunrouter"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/handlers/base"
)

// Handler provides HTTP handlers for heartbeat ingestion endpoints.
type Handler struct {
	base.HandlerBase
	svc *Service
}

// NewHandler creates a new heartbeat handler.
func NewHandler(service *Service, cfg *config.Config) *Handler {
	return &Handler{
		HandlerBase: base.NewHandlerBase(cfg),
		svc:         service,
	}
}

// heartbeatBody represents the optional JSON body for heartbeat requests.
type heartbeatBody struct {
	Message string `json:"message"`
}

// ReceiveHeartbeat handles incoming heartbeat pings.
func (h *Handler) ReceiveHeartbeat(writer http.ResponseWriter, req bunrouter.Request) error {
	orgSlug := req.Param("org")
	identifier := req.Param("identifier")
	token := req.URL.Query().Get("token")
	status := req.URL.Query().Get("status")

	// Parse optional JSON body for message
	var message string
	if req.Body != nil && req.Header.Get("Content-Type") == "application/json" {
		var body heartbeatBody
		if err := json.NewDecoder(req.Body).Decode(&body); err == nil {
			message = body.Message
		}
	}

	if err := h.svc.ReceiveHeartbeat(req.Context(), orgSlug, identifier, token, status, message); err != nil {
		return h.handleError(writer, err)
	}

	return h.WriteJSON(writer, http.StatusOK, map[string]string{"status": "ok"})
}

func (h *Handler) handleError(writer http.ResponseWriter, err error) error {
	switch {
	case errors.Is(err, ErrOrganizationNotFound):
		return h.WriteErrorErr(writer, http.StatusNotFound, base.ErrorCodeOrganizationNotFound, "Organization not found", err)
	case errors.Is(err, ErrCheckNotFound):
		return h.WriteErrorErr(writer, http.StatusNotFound, base.ErrorCodeCheckNotFound, "Check not found", err)
	case errors.Is(err, ErrNotHeartbeatCheck):
		return h.WriteError(writer, http.StatusBadRequest, base.ErrorCodeValidationError, "Check is not a heartbeat type")
	case errors.Is(err, ErrMissingToken):
		return h.WriteError(writer, http.StatusUnauthorized, base.ErrorCodeUnauthorized, "Missing token parameter")
	case errors.Is(err, ErrInvalidToken):
		return h.WriteError(writer, http.StatusUnauthorized, base.ErrorCodeUnauthorized, "Invalid token")
	case errors.Is(err, ErrInvalidStatus):
		return h.WriteError(writer, http.StatusBadRequest, base.ErrorCodeValidationError,
			"Invalid status, must be one of: running, up, down, error")
	default:
		return h.WriteInternalError(writer, err)
	}
}
