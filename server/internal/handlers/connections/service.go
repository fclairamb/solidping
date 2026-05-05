// Package connections provides HTTP handlers for integration connection management.
package connections

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/fclairamb/solidping/server/internal/crypto/credentials"
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
	// creds is always non-nil — its .Enabled() reports whether a master
	// key is configured. Used to encrypt secret fields in Settings.
	creds credentials.Service
}

// NewService creates a new connections service.
func NewService(dbService db.Service, creds credentials.Service) *Service {
	return &Service{
		db:    dbService,
		creds: creds,
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
	// SettingsPrivateKeys lists which top-level Settings keys are stored
	// encrypted (and therefore stripped from the Settings map in the
	// response). Lets the dashboard render placeholder pills like
	// "•••• access_token" without echoing the secret value.
	SettingsPrivateKeys []string  `json:"settingsPrivateKeys,omitempty"`
	CreatedAt           time.Time `json:"createdAt"`
	UpdatedAt           time.Time `json:"updatedAt"`
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

// toResponse converts a model to a response. The Settings map is the
// already-public side: secret keys live in SettingsPrivate (encrypted) and
// are exposed only as names via SettingsPrivateKeys so the dashboard can
// show placeholder pills.
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

	secretSet := map[string]struct{}{}
	for _, k := range credentials.ConnectionSecretFields(conn.Type) {
		secretSet[k] = struct{}{}
	}

	if includeSettings && conn.Settings != nil {
		settings := make(map[string]any, len(conn.Settings))
		for k, v := range conn.Settings {
			if _, isSecret := secretSet[k]; isSecret {
				continue
			}
			settings[k] = v
		}
		resp.Settings = settings
	}

	if conn.SettingsPrivateKeys != nil && *conn.SettingsPrivateKeys != "" {
		var keys []string
		if err := json.Unmarshal([]byte(*conn.SettingsPrivateKeys), &keys); err == nil {
			resp.SettingsPrivateKeys = keys
		}
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

	if err := s.applySettingsEncryption(ctx, conn, req.Settings); err != nil {
		return nil, err
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
		// PATCH-merge: secret keys absent from the incoming map are
		// preserved from the existing config_private; explicit null/empty
		// clears them. Non-secret keys follow replace-wholesale semantics.
		existing, decErr := s.loadDecryptedSettings(ctx, conn)
		if decErr != nil {
			return nil, decErr
		}

		secrets := credentials.ConnectionSecretFields(conn.Type)
		merged := credentials.MergePatch(existing, req.Settings, secrets)

		if encErr := s.applySettingsEncryption(ctx, conn, merged); encErr != nil {
			return nil, encErr
		}

		settings := conn.Settings
		update.Settings = &settings
		update.SettingsPrivate = conn.SettingsPrivate
		update.SettingsPrivateKeys = conn.SettingsPrivateKeys
		update.ClearSettingsPrivate = conn.SettingsPrivate == nil
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

// applySettingsEncryption splits Settings into public/private using the
// connection-type's declared secret keys, encrypts the private side, and
// writes Settings + SettingsPrivate + SettingsPrivateKeys onto the
// connection. When encryption is disabled at the server, secrets stay
// plaintext on Settings (logged-once startup warning covers the gap).
func (s *Service) applySettingsEncryption(
	ctx context.Context, conn *models.IntegrationConnection, effective map[string]any,
) error {
	if effective == nil {
		effective = map[string]any{}
	}

	secrets := credentials.ConnectionSecretFields(conn.Type)
	public, private := credentials.SplitConfig(effective, secrets)
	conn.Settings = models.JSONMap(public)

	if !s.creds.Enabled() || len(private) == 0 {
		conn.SettingsPrivate = nil
		conn.SettingsPrivateKeys = nil
		// Plaintext fallback: secrets must still be persisted so the
		// integration actually works.
		if !s.creds.Enabled() {
			for k, v := range private {
				conn.Settings[k] = v
			}
		}

		return nil
	}

	envelope, err := s.creds.EncryptForOrg(ctx, conn.OrganizationUID, private)
	if err != nil {
		return fmt.Errorf("encrypt connection settings: %w", err)
	}

	conn.SettingsPrivate = &envelope

	keysJSON, err := json.Marshal(credentials.SortedKeys(private))
	if err != nil {
		return fmt.Errorf("marshal settings private keys: %w", err)
	}

	keysStr := string(keysJSON)
	conn.SettingsPrivateKeys = &keysStr

	return nil
}

// loadDecryptedSettings returns the merged plaintext effective Settings
// of a connection. Used by the PATCH path so secret-preservation has the
// existing values to merge into.
func (s *Service) loadDecryptedSettings(
	ctx context.Context, conn *models.IntegrationConnection,
) (map[string]any, error) {
	if conn.SettingsPrivate == nil || *conn.SettingsPrivate == "" {
		out := make(map[string]any, len(conn.Settings))
		for k, v := range conn.Settings {
			out[k] = v
		}

		return out, nil
	}

	if !s.creds.Enabled() {
		return nil, fmt.Errorf("decrypt connection %s: %w", conn.UID, credentials.ErrDisabled)
	}

	private, err := s.creds.DecryptForOrg(ctx, conn.OrganizationUID, *conn.SettingsPrivate)
	if err != nil {
		return nil, fmt.Errorf("decrypt connection %s: %w", conn.UID, err)
	}

	out := make(map[string]any, len(conn.Settings)+len(private))
	for k, v := range conn.Settings {
		out[k] = v
	}

	for k, v := range private {
		out[k] = v
	}

	return out, nil
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
