package connections

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/uptrace/bunrouter"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/handlers/base"
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

// ListConnections handles listing all connections of an organization.
func (h *Handler) ListConnections(writer http.ResponseWriter, req bunrouter.Request) error {
	orgSlug := req.Param("org")
	connType := req.URL.Query().Get("type")

	var typeFilter *string
	if connType != "" {
		typeFilter = &connType
	}

	connections, err := h.svc.ListConnections(req.Context(), orgSlug, typeFilter)
	if err != nil {
		return h.handleError(writer, err)
	}

	return h.WriteJSON(writer, http.StatusOK, connections)
}

// GetConnection handles getting a specific connection by UID.
func (h *Handler) GetConnection(writer http.ResponseWriter, req bunrouter.Request) error {
	orgSlug := req.Param("org")
	connectionUID := req.Param("uid")

	connection, err := h.svc.GetConnection(req.Context(), orgSlug, connectionUID)
	if err != nil {
		return h.handleError(writer, err)
	}

	return h.WriteJSON(writer, http.StatusOK, connection)
}

// CreateConnection handles creating a new connection.
func (h *Handler) CreateConnection(writer http.ResponseWriter, req bunrouter.Request) error {
	orgSlug := req.Param("org")

	var createReq CreateConnectionRequest
	if err := json.NewDecoder(req.Body).Decode(&createReq); err != nil {
		return h.WriteValidationError(writer, "Invalid JSON", []base.ValidationErrorField{
			{Name: "body", Message: "Invalid JSON format"},
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

	connection, err := h.svc.CreateConnection(req.Context(), orgSlug, createReq)
	if err != nil {
		return h.handleError(writer, err)
	}

	return h.WriteJSON(writer, http.StatusCreated, connection)
}

// UpdateConnection handles updating a connection.
func (h *Handler) UpdateConnection(writer http.ResponseWriter, req bunrouter.Request) error {
	orgSlug := req.Param("org")
	connectionUID := req.Param("uid")

	var updateReq UpdateConnectionRequest
	if err := json.NewDecoder(req.Body).Decode(&updateReq); err != nil {
		return h.WriteValidationError(writer, "Invalid JSON", []base.ValidationErrorField{
			{Name: "body", Message: "Invalid JSON format"},
		})
	}

	connection, err := h.svc.UpdateConnection(req.Context(), orgSlug, connectionUID, updateReq)
	if err != nil {
		return h.handleError(writer, err)
	}

	return h.WriteJSON(writer, http.StatusOK, connection)
}

// DeleteConnection handles deleting a connection.
func (h *Handler) DeleteConnection(writer http.ResponseWriter, req bunrouter.Request) error {
	orgSlug := req.Param("org")
	connectionUID := req.Param("uid")

	if err := h.svc.DeleteConnection(req.Context(), orgSlug, connectionUID); err != nil {
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
	case errors.Is(err, ErrConnectionNotFound):
		return h.WriteError(writer, http.StatusNotFound, base.ErrorCodeConnectionNotFound, "Connection not found")
	case errors.Is(err, ErrInvalidConnectionType):
		return h.WriteValidationError(writer, "Invalid connection type", []base.ValidationErrorField{
			{Name: "type", Message: "Type must be one of: slack, discord, webhook, email"},
		})
	default:
		return h.WriteInternalError(writer, err)
	}
}
