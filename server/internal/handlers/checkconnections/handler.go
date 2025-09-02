// Package checkconnections provides HTTP handlers for managing
// check-to-integration-connection associations for incident notifications.
package checkconnections

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/uptrace/bunrouter"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/handlers/base"
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

// ListConnections handles GET /api/v1/orgs/:org/checks/:check/connections.
func (h *Handler) ListConnections(writer http.ResponseWriter, req bunrouter.Request) error {
	orgSlug := req.Param("org")
	checkID := req.Param("check")

	connections, err := h.svc.ListConnections(req.Context(), orgSlug, checkID)
	if err != nil {
		return h.handleError(writer, err)
	}

	return h.WriteJSON(writer, http.StatusOK, connections)
}

// SetConnections handles PUT /api/v1/orgs/:org/checks/:check/connections.
func (h *Handler) SetConnections(writer http.ResponseWriter, req bunrouter.Request) error {
	orgSlug := req.Param("org")
	checkID := req.Param("check")

	var setReq SetConnectionsRequest
	if err := json.NewDecoder(req.Body).Decode(&setReq); err != nil {
		return h.WriteValidationError(writer, "Invalid JSON", []base.ValidationErrorField{
			{Name: "body", Message: "Invalid JSON format"},
		})
	}

	if err := h.svc.SetConnections(req.Context(), orgSlug, checkID, setReq); err != nil {
		return h.handleError(writer, err)
	}

	writer.WriteHeader(http.StatusNoContent)
	return nil
}

// AddConnection handles POST /api/v1/orgs/:org/checks/:check/connections/:connection.
func (h *Handler) AddConnection(writer http.ResponseWriter, req bunrouter.Request) error {
	orgSlug := req.Param("org")
	checkID := req.Param("check")
	connectionUID := req.Param("connection")

	if err := h.svc.AddConnection(req.Context(), orgSlug, checkID, connectionUID); err != nil {
		return h.handleError(writer, err)
	}

	writer.WriteHeader(http.StatusCreated)
	return nil
}

// RemoveConnection handles DELETE /api/v1/orgs/:org/checks/:check/connections/:connection.
func (h *Handler) RemoveConnection(writer http.ResponseWriter, req bunrouter.Request) error {
	orgSlug := req.Param("org")
	checkID := req.Param("check")
	connectionUID := req.Param("connection")

	if err := h.svc.RemoveConnection(req.Context(), orgSlug, checkID, connectionUID); err != nil {
		return h.handleError(writer, err)
	}

	writer.WriteHeader(http.StatusNoContent)
	return nil
}

// GetConnectionSettings handles GET /api/v1/orgs/:org/checks/:check/connections/:connection.
func (h *Handler) GetConnectionSettings(writer http.ResponseWriter, req bunrouter.Request) error {
	orgSlug := req.Param("org")
	checkID := req.Param("check")
	connectionUID := req.Param("connection")

	response, err := h.svc.GetConnectionSettings(req.Context(), orgSlug, checkID, connectionUID)
	if err != nil {
		return h.handleError(writer, err)
	}

	return h.WriteJSON(writer, http.StatusOK, response)
}

// UpdateConnectionSettings handles PATCH /api/v1/orgs/:org/checks/:check/connections/:connection.
func (h *Handler) UpdateConnectionSettings(writer http.ResponseWriter, req bunrouter.Request) error {
	orgSlug := req.Param("org")
	checkID := req.Param("check")
	connectionUID := req.Param("connection")

	var updateReq UpdateConnectionSettingsRequest
	if err := json.NewDecoder(req.Body).Decode(&updateReq); err != nil {
		return h.WriteValidationError(writer, "Invalid JSON", []base.ValidationErrorField{
			{Name: "body", Message: "Invalid JSON format"},
		})
	}

	if err := h.svc.UpdateConnectionSettings(req.Context(), orgSlug, checkID, connectionUID, updateReq); err != nil {
		return h.handleError(writer, err)
	}

	writer.WriteHeader(http.StatusNoContent)
	return nil
}

func (h *Handler) handleError(writer http.ResponseWriter, err error) error {
	switch {
	case errors.Is(err, ErrOrganizationNotFound):
		return h.WriteError(writer, http.StatusNotFound, base.ErrorCodeOrganizationNotFound, "Organization not found")
	case errors.Is(err, ErrCheckNotFound):
		return h.WriteError(writer, http.StatusNotFound, base.ErrorCodeCheckNotFound, "Check not found")
	case errors.Is(err, ErrConnectionNotFound):
		return h.WriteError(writer, http.StatusNotFound, base.ErrorCodeNotFound, "Connection not found")
	default:
		return h.WriteInternalError(writer, err)
	}
}
