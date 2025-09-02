package checkconnections

import (
	"context"
	"database/sql"
	"errors"

	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
)

var (
	// ErrOrganizationNotFound is returned when the organization is not found.
	ErrOrganizationNotFound = errors.New("organization not found")
	// ErrCheckNotFound is returned when the check is not found.
	ErrCheckNotFound = errors.New("check not found")
	// ErrConnectionNotFound is returned when the connection is not found.
	ErrConnectionNotFound = errors.New("connection not found")
)

// Service provides check-connection management functionality.
type Service struct {
	db db.Service
}

// NewService creates a new check-connection service.
func NewService(dbService db.Service) *Service {
	return &Service{db: dbService}
}

// ConnectionResponse represents a connection in API responses.
type ConnectionResponse struct {
	UID       string `json:"uid"`
	Type      string `json:"type"`
	Name      string `json:"name"`
	Enabled   bool   `json:"enabled"`
	IsDefault bool   `json:"isDefault"`
}

// ListConnectionsResponse represents the response for listing connections.
type ListConnectionsResponse struct {
	Data []*ConnectionResponse `json:"data"`
}

// SetConnectionsRequest represents the request to set all connections for a check.
type SetConnectionsRequest struct {
	ConnectionUIDs []string `json:"connectionUids"`
}

// UpdateConnectionSettingsRequest represents the request to update connection settings.
type UpdateConnectionSettingsRequest struct {
	Settings models.JSONMap `json:"settings"`
}

// ConnectionSettingsResponse represents a connection with its check-level settings.
type ConnectionSettingsResponse struct {
	UID       string          `json:"uid"`
	Type      string          `json:"type"`
	Name      string          `json:"name"`
	Enabled   bool            `json:"enabled"`
	IsDefault bool            `json:"isDefault"`
	Settings  *models.JSONMap `json:"settings,omitempty"`
}

// ListConnections lists all connections for a check.
func (s *Service) ListConnections(
	ctx context.Context, orgSlug, checkIdentifier string,
) (*ListConnectionsResponse, error) {
	org, err := s.db.GetOrganizationBySlug(ctx, orgSlug)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrOrganizationNotFound
		}
		return nil, err
	}

	check, err := s.db.GetCheckByUidOrSlug(ctx, org.UID, checkIdentifier)
	if err != nil || check == nil {
		return nil, ErrCheckNotFound
	}

	connections, err := s.db.ListConnectionsForCheck(ctx, check.UID)
	if err != nil {
		return nil, err
	}

	response := &ListConnectionsResponse{
		Data: make([]*ConnectionResponse, 0, len(connections)),
	}
	for _, conn := range connections {
		response.Data = append(response.Data, &ConnectionResponse{
			UID:       conn.UID,
			Type:      string(conn.Type),
			Name:      conn.Name,
			Enabled:   conn.Enabled,
			IsDefault: conn.IsDefault,
		})
	}

	return response, nil
}

// SetConnections replaces all connections for a check.
func (s *Service) SetConnections(
	ctx context.Context, orgSlug, checkIdentifier string, req SetConnectionsRequest,
) error {
	org, err := s.db.GetOrganizationBySlug(ctx, orgSlug)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrOrganizationNotFound
		}
		return err
	}

	check, err := s.db.GetCheckByUidOrSlug(ctx, org.UID, checkIdentifier)
	if err != nil || check == nil {
		return ErrCheckNotFound
	}

	// Validate all connections exist and belong to org
	for _, connUID := range req.ConnectionUIDs {
		conn, connErr := s.db.GetIntegrationConnection(ctx, connUID)
		if connErr != nil {
			return ErrConnectionNotFound
		}
		if conn.OrganizationUID != org.UID {
			return ErrConnectionNotFound
		}
	}

	return s.db.SetCheckConnections(ctx, check.UID, req.ConnectionUIDs)
}

// AddConnection adds a connection to a check.
func (s *Service) AddConnection(ctx context.Context, orgSlug, checkIdentifier, connectionUID string) error {
	org, err := s.db.GetOrganizationBySlug(ctx, orgSlug)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrOrganizationNotFound
		}
		return err
	}

	check, err := s.db.GetCheckByUidOrSlug(ctx, org.UID, checkIdentifier)
	if err != nil || check == nil {
		return ErrCheckNotFound
	}

	conn, err := s.db.GetIntegrationConnection(ctx, connectionUID)
	if err != nil || conn.OrganizationUID != org.UID {
		return ErrConnectionNotFound
	}

	checkConn := models.NewCheckConnection(check.UID, conn.UID, org.UID)
	return s.db.CreateCheckConnection(ctx, checkConn)
}

// RemoveConnection removes a connection from a check.
func (s *Service) RemoveConnection(ctx context.Context, orgSlug, checkIdentifier, connectionUID string) error {
	org, err := s.db.GetOrganizationBySlug(ctx, orgSlug)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrOrganizationNotFound
		}
		return err
	}

	check, err := s.db.GetCheckByUidOrSlug(ctx, org.UID, checkIdentifier)
	if err != nil || check == nil {
		return ErrCheckNotFound
	}

	return s.db.DeleteCheckConnection(ctx, check.UID, connectionUID)
}

// GetConnectionSettings retrieves settings for a specific check-connection.
func (s *Service) GetConnectionSettings(
	ctx context.Context, orgSlug, checkIdentifier, connectionUID string,
) (*ConnectionSettingsResponse, error) {
	org, err := s.db.GetOrganizationBySlug(ctx, orgSlug)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrOrganizationNotFound
		}
		return nil, err
	}

	check, err := s.db.GetCheckByUidOrSlug(ctx, org.UID, checkIdentifier)
	if err != nil || check == nil {
		return nil, ErrCheckNotFound
	}

	conn, err := s.db.GetIntegrationConnection(ctx, connectionUID)
	if err != nil || conn.OrganizationUID != org.UID {
		return nil, ErrConnectionNotFound
	}

	checkConn, err := s.db.GetCheckConnection(ctx, check.UID, connectionUID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrConnectionNotFound
		}
		return nil, err
	}

	return &ConnectionSettingsResponse{
		UID:       conn.UID,
		Type:      string(conn.Type),
		Name:      conn.Name,
		Enabled:   conn.Enabled,
		IsDefault: conn.IsDefault,
		Settings:  checkConn.Settings,
	}, nil
}

// UpdateConnectionSettings updates the settings for a specific check-connection.
func (s *Service) UpdateConnectionSettings(
	ctx context.Context, orgSlug, checkIdentifier, connectionUID string, req UpdateConnectionSettingsRequest,
) error {
	org, err := s.db.GetOrganizationBySlug(ctx, orgSlug)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrOrganizationNotFound
		}
		return err
	}

	check, err := s.db.GetCheckByUidOrSlug(ctx, org.UID, checkIdentifier)
	if err != nil || check == nil {
		return ErrCheckNotFound
	}

	// Verify connection exists and belongs to org
	conn, err := s.db.GetIntegrationConnection(ctx, connectionUID)
	if err != nil || conn.OrganizationUID != org.UID {
		return ErrConnectionNotFound
	}

	update := &models.CheckConnectionUpdate{
		Settings: &req.Settings,
	}

	err = s.db.UpdateCheckConnection(ctx, check.UID, connectionUID, update)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrConnectionNotFound
		}
		return err
	}

	return nil
}
