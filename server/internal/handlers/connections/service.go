// Package connections provides HTTP handlers for integration connection management.
package connections

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
)

var (
	// ErrOrganizationNotFound is returned when an organization is not found.
	ErrOrganizationNotFound = errors.New("organization not found")
	// ErrConnectionNotFound is returned when a connection is not found.
	ErrConnectionNotFound = errors.New("connection not found")
	// ErrInvalidConnectionType is returned when an invalid connection type is provided.
	ErrInvalidConnectionType = errors.New("invalid connection type")
)

// Service provides business logic for connection management.
type Service struct {
	db db.Service
}

// NewService creates a new connections service.
func NewService(dbService db.Service) *Service {
	return &Service{
		db: dbService,
	}
}

// ConnectionResponse represents a connection in API responses.
type ConnectionResponse struct {
	UID       string         `json:"uid"`
	Type      string         `json:"type"`
	Name      string         `json:"name"`
	Enabled   bool           `json:"enabled"`
	IsDefault bool           `json:"isDefault"`
	Settings  map[string]any `json:"settings,omitempty"`
	CreatedAt time.Time      `json:"createdAt"`
	UpdatedAt time.Time      `json:"updatedAt"`
}

// ListConnectionsResponse represents the response for listing connections.
type ListConnectionsResponse struct {
	Data []*ConnectionResponse `json:"data"`
}

// CreateConnectionRequest represents the request to create a connection.
type CreateConnectionRequest struct {
	Type      string         `json:"type"`
	Name      string         `json:"name"`
	Enabled   *bool          `json:"enabled,omitempty"`
	IsDefault *bool          `json:"isDefault,omitempty"`
	Settings  map[string]any `json:"settings,omitempty"`
}

// UpdateConnectionRequest represents the request to update a connection.
type UpdateConnectionRequest struct {
	Name      *string        `json:"name,omitempty"`
	Enabled   *bool          `json:"enabled,omitempty"`
	IsDefault *bool          `json:"isDefault,omitempty"`
	Settings  map[string]any `json:"settings,omitempty"`
}

// toResponse converts a model to a response.
func toResponse(conn *models.IntegrationConnection, includeSettings bool) *ConnectionResponse {
	resp := &ConnectionResponse{
		UID:       conn.UID,
		Type:      string(conn.Type),
		Name:      conn.Name,
		Enabled:   conn.Enabled,
		IsDefault: conn.IsDefault,
		CreatedAt: conn.CreatedAt,
		UpdatedAt: conn.UpdatedAt,
	}

	if includeSettings && conn.Settings != nil {
		// Filter out sensitive fields
		settings := make(map[string]any)
		for k, v := range conn.Settings {
			if k != "access_token" {
				settings[k] = v
			}
		}
		resp.Settings = settings
	}

	return resp
}

// ListConnections returns all connections for an organization.
func (s *Service) ListConnections(
	ctx context.Context, orgSlug string, connType *string,
) (*ListConnectionsResponse, error) {
	org, err := s.db.GetOrganizationBySlug(ctx, orgSlug)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrOrganizationNotFound
		}

		return nil, err
	}

	filter := &models.ListIntegrationConnectionsFilter{
		OrganizationUID: org.UID,
	}

	if connType != nil {
		ct := models.ConnectionType(*connType)
		filter.Type = &ct
	}

	connections, err := s.db.ListIntegrationConnections(ctx, filter)
	if err != nil {
		return nil, err
	}

	response := &ListConnectionsResponse{
		Data: make([]*ConnectionResponse, 0, len(connections)),
	}

	for _, conn := range connections {
		response.Data = append(response.Data, toResponse(conn, false))
	}

	return response, nil
}

// GetConnection returns a connection by UID.
func (s *Service) GetConnection(
	ctx context.Context, orgSlug, connectionUID string,
) (*ConnectionResponse, error) {
	org, err := s.db.GetOrganizationBySlug(ctx, orgSlug)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrOrganizationNotFound
		}

		return nil, err
	}

	conn, err := s.db.GetIntegrationConnection(ctx, connectionUID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrConnectionNotFound
		}

		return nil, err
	}

	// Verify the connection belongs to the organization
	if conn.OrganizationUID != org.UID {
		return nil, ErrConnectionNotFound
	}

	return toResponse(conn, true), nil
}

// CreateConnection creates a new connection.
func (s *Service) CreateConnection(
	ctx context.Context, orgSlug string, req CreateConnectionRequest,
) (*ConnectionResponse, error) {
	org, err := s.db.GetOrganizationBySlug(ctx, orgSlug)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrOrganizationNotFound
		}

		return nil, err
	}

	// Validate connection type
	connType := models.ConnectionType(req.Type)
	switch connType {
	case models.ConnectionTypeSlack, models.ConnectionTypeDiscord,
		models.ConnectionTypeWebhook, models.ConnectionTypeEmail,
		models.ConnectionTypeGoogleChat, models.ConnectionTypeMattermost,
		models.ConnectionTypeNtfy, models.ConnectionTypeOpsgenie,
		models.ConnectionTypePushover:
		// Valid types
	default:
		return nil, ErrInvalidConnectionType
	}

	conn := models.NewIntegrationConnection(org.UID, connType, req.Name)

	if req.Enabled != nil {
		conn.Enabled = *req.Enabled
	}

	if req.IsDefault != nil {
		conn.IsDefault = *req.IsDefault
	}

	if req.Settings != nil {
		conn.Settings = models.JSONMap(req.Settings)
	}

	if err := s.db.CreateIntegrationConnection(ctx, conn); err != nil {
		return nil, err
	}

	return toResponse(conn, true), nil
}

// UpdateConnection updates a connection.
func (s *Service) UpdateConnection(
	ctx context.Context, orgSlug, connectionUID string, req UpdateConnectionRequest,
) (*ConnectionResponse, error) {
	org, err := s.db.GetOrganizationBySlug(ctx, orgSlug)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrOrganizationNotFound
		}

		return nil, err
	}

	conn, err := s.db.GetIntegrationConnection(ctx, connectionUID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrConnectionNotFound
		}

		return nil, err
	}

	// Verify the connection belongs to the organization
	if conn.OrganizationUID != org.UID {
		return nil, ErrConnectionNotFound
	}

	update := &models.IntegrationConnectionUpdate{
		Name:      req.Name,
		Enabled:   req.Enabled,
		IsDefault: req.IsDefault,
	}

	if req.Settings != nil {
		settings := models.JSONMap(req.Settings)
		update.Settings = &settings
	}

	if updateErr := s.db.UpdateIntegrationConnection(ctx, connectionUID, update); updateErr != nil {
		return nil, updateErr
	}

	// Fetch updated connection
	conn, err = s.db.GetIntegrationConnection(ctx, connectionUID)
	if err != nil {
		return nil, err
	}

	return toResponse(conn, true), nil
}

// DeleteConnection deletes a connection.
func (s *Service) DeleteConnection(ctx context.Context, orgSlug, connectionUID string) error {
	org, err := s.db.GetOrganizationBySlug(ctx, orgSlug)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrOrganizationNotFound
		}

		return err
	}

	conn, err := s.db.GetIntegrationConnection(ctx, connectionUID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrConnectionNotFound
		}

		return err
	}

	// Verify the connection belongs to the organization
	if conn.OrganizationUID != org.UID {
		return ErrConnectionNotFound
	}

	return s.db.DeleteIntegrationConnection(ctx, connectionUID)
}
