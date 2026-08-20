// Package checkchannels provides HTTP handlers for managing
// check-to-integration-connection associations for incident notifications.
package checkchannels

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/handlers/base"
	"github.com/fclairamb/solidping/server/internal/httpx"
)

// Handler provides HTTP handlers for check-connection management endpoints.
type Handler struct {
	base.HandlerBase
	svc *Service
}

// NewHandler creates a new check-connections handler.
func NewHandler(service *Service, cfg *config.Config) *Handler {
	return &Handler{
		HandlerBase: base.NewHandlerBase(cfg),
		svc:         service,
	}
}

// ListChannels handles GET /api/v1/orgs/:org/checks/:check/connections.
func (h *Handler) ListChannels(writer http.ResponseWriter, req *http.Request) error {
	orgSlug := httpx.Param(req, "org")
	checkID := httpx.Param(req, "check")

	connections, err := h.svc.ListChannels(req.Context(), orgSlug, checkID)
	if err != nil {
		return h.handleError(writer, req, err)
	}

	return h.WriteJSON(writer, http.StatusOK, connections)
}

// SetChannels handles PUT /api/v1/orgs/:org/checks/:check/connections.
func (h *Handler) SetChannels(writer http.ResponseWriter, req *http.Request) error {
	orgSlug := httpx.Param(req, "org")
	checkID := httpx.Param(req, "check")

	var setReq SetConnectionsRequest
	if err := json.NewDecoder(req.Body).Decode(&setReq); err != nil {
		return h.WriteValidationError(writer, "Invalid JSON", []base.ValidationErrorField{
			{Name: "body", Message: "Invalid JSON format"},
		})
	}

	if err := h.svc.SetChannels(req.Context(), orgSlug, checkID, setReq); err != nil {
		return h.handleError(writer, req, err)
	}

	writer.WriteHeader(http.StatusNoContent)
	return nil
}

// AddChannel handles POST /api/v1/orgs/:org/checks/:check/connections/:connection.
func (h *Handler) AddChannel(writer http.ResponseWriter, req *http.Request) error {
	orgSlug := httpx.Param(req, "org")
	checkID := httpx.Param(req, "check")
	connectionUID := httpx.Param(req, "connection")

	if err := h.svc.AddChannel(req.Context(), orgSlug, checkID, connectionUID); err != nil {
		return h.handleError(writer, req, err)
	}

	writer.WriteHeader(http.StatusCreated)
	return nil
}

// RemoveConnection handles DELETE /api/v1/orgs/:org/checks/:check/connections/:connection.
func (h *Handler) RemoveConnection(writer http.ResponseWriter, req *http.Request) error {
	orgSlug := httpx.Param(req, "org")
	checkID := httpx.Param(req, "check")
	connectionUID := httpx.Param(req, "connection")

	if err := h.svc.RemoveConnection(req.Context(), orgSlug, checkID, connectionUID); err != nil {
		return h.handleError(writer, req, err)
	}

	writer.WriteHeader(http.StatusNoContent)
	return nil
}

// GetConnectionSettings handles GET /api/v1/orgs/:org/checks/:check/connections/:connection.
func (h *Handler) GetConnectionSettings(writer http.ResponseWriter, req *http.Request) error {
	orgSlug := httpx.Param(req, "org")
	checkID := httpx.Param(req, "check")
	connectionUID := httpx.Param(req, "connection")

	response, err := h.svc.GetConnectionSettings(req.Context(), orgSlug, checkID, connectionUID)
	if err != nil {
		return h.handleError(writer, req, err)
	}

	return h.WriteJSON(writer, http.StatusOK, response)
}

// UpdateConnectionSettings handles PATCH /api/v1/orgs/:org/checks/:check/connections/:connection.
func (h *Handler) UpdateConnectionSettings(writer http.ResponseWriter, req *http.Request) error {
	orgSlug := httpx.Param(req, "org")
	checkID := httpx.Param(req, "check")
	connectionUID := httpx.Param(req, "connection")

	var updateReq UpdateConnectionSettingsRequest
	if err := json.NewDecoder(req.Body).Decode(&updateReq); err != nil {
		return h.WriteValidationError(writer, "Invalid JSON", []base.ValidationErrorField{
			{Name: "body", Message: "Invalid JSON format"},
		})
	}

	if err := h.svc.UpdateConnectionSettings(req.Context(), orgSlug, checkID, connectionUID, updateReq); err != nil {
		return h.handleError(writer, req, err)
	}

	writer.WriteHeader(http.StatusNoContent)
	return nil
}

func (h *Handler) handleError(writer http.ResponseWriter, request *http.Request, err error) error {
	switch {
	case errors.Is(err, ErrOrganizationNotFound):
		return h.WriteError(writer, http.StatusNotFound, base.ErrorCodeOrganizationNotFound, "Organization not found")
	case errors.Is(err, ErrCheckNotFound):
		return h.WriteError(writer, http.StatusNotFound, base.ErrorCodeCheckNotFound, "Check not found")
	case errors.Is(err, ErrConnectionNotFound):
		return h.WriteError(writer, http.StatusNotFound, base.ErrorCodeNotFound, "Connection not found")
	case errors.Is(err, ErrNotNotifyCapable):
		return h.WriteError(
			writer, http.StatusBadRequest, base.ErrorCodeValidationError,
			"This integration cannot receive notifications.",
		)
	case errors.Is(err, ErrMSTeamsBotUnknownDestination):
		return h.WriteError(
			writer, http.StatusBadRequest, base.ErrorCodeValidationError,
			"That Microsoft Teams channel is not one this bot has been added to.",
		)
	default:
		return h.WriteInternalError(writer, request, err)
	}
}
