// Package channels provides HTTP handlers for notification channel management
// (Slack, Discord, webhook, email, etc.). The legacy `integration_connections`
// table backs this domain — see spec 2026-05-07-03 for the rename rationale.
package channels

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/fclairamb/solidping/server/internal/activation"
	"github.com/fclairamb/solidping/server/internal/crypto/credentials"
	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/integrations/freebox"
)

var (
	// ErrOrganizationNotFound is returned when an organization is not found.
	ErrOrganizationNotFound = errors.New("organization not found")
	// ErrConnectionNotFound is returned when a connection is not found.
	ErrConnectionNotFound = errors.New("connection not found")
	// ErrInvalidConnectionType is returned when an invalid connection type is provided.
	ErrInvalidConnectionType = errors.New("invalid connection type")
	// ErrFreeboxPairingFailed is returned when the Freebox pairing flow
	// fails (transport error, denial, timeout). The wrapped error keeps
	// the underlying cause so the handler can surface a useful message.
	ErrFreeboxPairingFailed = errors.New("freebox pairing failed")
	// ErrFreeboxNotPairing is returned when a status-poll is requested
	// against a channel that isn't currently in the pairing state.
	ErrFreeboxNotPairing = errors.New("freebox channel is not in pairing state")
	// ErrFreeboxTypeMismatch is returned when a pairing endpoint targets
	// a non-Freebox channel.
	ErrFreeboxTypeMismatch = errors.New("channel is not a freebox connection")
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

// ChannelResponse represents a connection in API responses.
type ChannelResponse struct {
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
	Data []*ChannelResponse `json:"data"`
}

// CreateChannelRequest represents the request to create a connection.
type CreateChannelRequest struct {
	Type      string         `json:"type"`
	Name      string         `json:"name"`
	Enabled   *bool          `json:"enabled,omitempty"`
	IsDefault *bool          `json:"isDefault,omitempty"`
	Settings  map[string]any `json:"settings,omitempty"`
}

// UpdateChannelRequest represents the request to update a connection.
type UpdateChannelRequest struct {
	Name      *string        `json:"name,omitempty"`
	Enabled   *bool          `json:"enabled,omitempty"`
	IsDefault *bool          `json:"isDefault,omitempty"`
	Settings  map[string]any `json:"settings,omitempty"`
}

// toResponse converts a model to a response. The Settings map is the
// already-public side: secret keys live in SettingsPrivate (encrypted) and
// are exposed only as names via SettingsPrivateKeys so the dashboard can
// show placeholder pills.
func toResponse(conn *models.Channel, includeSettings bool) *ChannelResponse {
	resp := &ChannelResponse{
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

// ListChannels returns all connections for an organization.
func (s *Service) ListChannels(
	ctx context.Context, orgSlug string, connType *string,
) (*ListConnectionsResponse, error) {
	org, err := s.db.GetOrganizationBySlug(ctx, orgSlug)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrOrganizationNotFound
		}

		return nil, err
	}

	filter := &models.ListChannelsFilter{
		OrganizationUID: org.UID,
	}

	if connType != nil {
		ct := models.ConnectionType(*connType)
		filter.Type = &ct
	}

	connections, err := s.db.ListChannels(ctx, filter)
	if err != nil {
		return nil, err
	}

	response := &ListConnectionsResponse{
		Data: make([]*ChannelResponse, 0, len(connections)),
	}

	for _, conn := range connections {
		response.Data = append(response.Data, toResponse(conn, false))
	}

	return response, nil
}

// GetChannel returns a connection by UID.
func (s *Service) GetChannel(
	ctx context.Context, orgSlug, connectionUID string,
) (*ChannelResponse, error) {
	org, err := s.db.GetOrganizationBySlug(ctx, orgSlug)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrOrganizationNotFound
		}

		return nil, err
	}

	conn, err := s.db.GetChannel(ctx, connectionUID)
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

// CreateChannel creates a new connection.
func (s *Service) CreateChannel(
	ctx context.Context, orgSlug string, req CreateChannelRequest,
) (*ChannelResponse, error) {
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
		models.ConnectionTypePushover, models.ConnectionTypeFreebox:
		// Valid types
	default:
		return nil, ErrInvalidConnectionType
	}

	conn := models.NewChannel(org.UID, connType, req.Name)

	if req.Enabled != nil {
		conn.Enabled = *req.Enabled
	}

	if req.IsDefault != nil {
		conn.IsDefault = *req.IsDefault
	}

	if err := s.applySettingsEncryption(ctx, conn, req.Settings); err != nil {
		return nil, err
	}

	if err := s.db.CreateChannel(ctx, conn); err != nil {
		return nil, err
	}

	activation.Emit(ctx, s.db, org.UID,
		models.EventTypeOrgActivationFirstNotificationConfigured,
		activation.SourceAPI, "")

	return toResponse(conn, true), nil
}

// UpdateChannel updates a connection.
func (s *Service) UpdateChannel(
	ctx context.Context, orgSlug, connectionUID string, req UpdateChannelRequest,
) (*ChannelResponse, error) {
	org, err := s.db.GetOrganizationBySlug(ctx, orgSlug)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrOrganizationNotFound
		}

		return nil, err
	}

	conn, err := s.db.GetChannel(ctx, connectionUID)
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

	update := &models.ChannelUpdate{
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

	if updateErr := s.db.UpdateChannel(ctx, connectionUID, update); updateErr != nil {
		return nil, updateErr
	}

	// Fetch updated connection
	conn, err = s.db.GetChannel(ctx, connectionUID)
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
	ctx context.Context, conn *models.Channel, effective map[string]any,
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
	ctx context.Context, conn *models.Channel,
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

// StartFreeboxPairingRequest is the body for the start-pairing endpoint.
type StartFreeboxPairingRequest struct {
	// Name is used for the IntegrationConnection row and for the
	// device_name shown on the Freebox LCD prompt. Optional — defaults
	// to "SolidPing" if absent.
	Name string `json:"name,omitempty"`
	// BaseURL of the Freebox API. Defaults to
	// "http://mafreebox.freebox.fr" when empty.
	BaseURL string `json:"baseUrl,omitempty"`
}

// FreeboxPairingResponse is the body returned by the start-pairing
// endpoint.
type FreeboxPairingResponse struct {
	ConnectionUID string `json:"connectionUid"`
	TrackID       int    `json:"trackId"`
	Status        string `json:"status"`
}

// FreeboxPairingStatusResponse is the body returned by the
// poll-status endpoint.
type FreeboxPairingStatusResponse struct {
	Status string `json:"status"`
}

// StartFreeboxPairing kicks off a Freebox pairing flow and persists the
// resulting permanent app_token (encrypted) onto a new IntegrationConnection
// row. The connection starts in `Status = "pairing"` until the operator
// approves the prompt on the Freebox LCD.
func (s *Service) StartFreeboxPairing(
	ctx context.Context, orgSlug string, req StartFreeboxPairingRequest,
) (*FreeboxPairingResponse, error) {
	org, err := s.db.GetOrganizationBySlug(ctx, orgSlug)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrOrganizationNotFound
		}

		return nil, err
	}

	settings := &models.FreeboxSettings{
		BaseURL:    req.BaseURL,
		AppID:      freebox.DefaultAppID,
		DeviceName: req.Name,
		Status:     models.FreeboxStatusPairing,
	}
	if settings.BaseURL == "" {
		settings.BaseURL = freebox.DefaultBaseURL
	}
	if settings.DeviceName == "" {
		settings.DeviceName = freebox.DefaultDeviceName
	}

	authResult, err := freebox.StartPairing(ctx, settings)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrFreeboxPairingFailed, err)
	}

	settings.TrackID = authResult.TrackID

	channelName := req.Name
	if channelName == "" {
		channelName = "Freebox"
	}

	conn := models.NewChannel(org.UID, models.ConnectionTypeFreebox, channelName)

	effective, err := mergeFreeboxSettings(settings, &models.FreeboxPrivateSettings{
		AppToken: authResult.AppToken,
	})
	if err != nil {
		return nil, err
	}

	if err := s.applySettingsEncryption(ctx, conn, effective); err != nil {
		return nil, err
	}

	if err := s.db.CreateChannel(ctx, conn); err != nil {
		return nil, err
	}

	activation.Emit(ctx, s.db, org.UID,
		models.EventTypeOrgActivationFirstNotificationConfigured,
		activation.SourceAPI, "")

	return &FreeboxPairingResponse{
		ConnectionUID: conn.UID,
		TrackID:       authResult.TrackID,
		Status:        models.FreeboxStatusPairing,
	}, nil
}

// CheckFreeboxPairingStatus polls the Freebox once for the current
// pairing status and reconciles the IntegrationConnection record:
//
//   - granted → clears TrackID, sets Status = granted
//   - denied / timeout → sets Status accordingly, leaves the row in
//     place so the operator can see the failure (and re-pair from the UI)
//   - pending / unknown → unchanged
func (s *Service) CheckFreeboxPairingStatus(
	ctx context.Context, orgSlug, connectionUID string,
) (*FreeboxPairingStatusResponse, error) {
	org, err := s.db.GetOrganizationBySlug(ctx, orgSlug)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrOrganizationNotFound
		}

		return nil, err
	}

	conn, err := s.db.GetChannel(ctx, connectionUID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrConnectionNotFound
		}

		return nil, err
	}

	if conn.OrganizationUID != org.UID {
		return nil, ErrConnectionNotFound
	}

	if conn.Type != models.ConnectionTypeFreebox {
		return nil, ErrFreeboxTypeMismatch
	}

	settings, err := models.FreeboxSettingsFromJSONMap(conn.Settings)
	if err != nil {
		return nil, fmt.Errorf("parse freebox settings: %w", err)
	}

	if settings.TrackID == 0 || settings.Status != models.FreeboxStatusPairing {
		return nil, ErrFreeboxNotPairing
	}

	status, pollErr := freebox.CheckPairingStatus(ctx, settings, settings.TrackID)
	switch {
	case errors.Is(pollErr, freebox.ErrPairingDenied):
		settings.Status = models.FreeboxStatusDenied
	case errors.Is(pollErr, freebox.ErrPairingTimeout):
		settings.Status = models.FreeboxStatusTimeout
	case pollErr != nil:
		return nil, fmt.Errorf("%w: %w", ErrFreeboxPairingFailed, pollErr)
	default:
		switch status {
		case freebox.StatusGranted:
			settings.Status = models.FreeboxStatusGranted
			settings.TrackID = 0
		case freebox.StatusPending, freebox.StatusUnknown:
			// No state change needed.
		}
	}

	// Persist any state transition that happened above.
	if err := s.persistFreeboxSettings(ctx, conn, settings); err != nil {
		return nil, err
	}

	return &FreeboxPairingStatusResponse{Status: settings.Status}, nil
}

// mergeFreeboxSettings flattens the public/private structs into one
// effective map that applySettingsEncryption can split again. Keeping
// this in one place avoids leaking the freebox secret-key names into
// the channel handler.
func mergeFreeboxSettings(
	pub *models.FreeboxSettings, priv *models.FreeboxPrivateSettings,
) (map[string]any, error) {
	asMap, err := pub.ToJSONMap()
	if err != nil {
		return nil, fmt.Errorf("marshal freebox settings: %w", err)
	}

	out := make(map[string]any, len(asMap)+1)
	for k, v := range asMap {
		out[k] = v
	}

	if priv != nil && priv.AppToken != "" {
		out["appToken"] = priv.AppToken
	}

	return out, nil
}

// persistFreeboxSettings writes the freebox-specific Settings struct
// back to the channel without disturbing the encrypted app_token. It
// loads the existing decrypted secrets, replaces the public side, and
// goes back through the standard split-and-encrypt pipeline.
func (s *Service) persistFreeboxSettings(
	ctx context.Context, conn *models.Channel, settings *models.FreeboxSettings,
) error {
	existing, err := s.loadDecryptedSettings(ctx, conn)
	if err != nil {
		return err
	}

	pubMap, err := settings.ToJSONMap()
	if err != nil {
		return fmt.Errorf("marshal freebox settings: %w", err)
	}

	merged := make(map[string]any, len(pubMap)+1)
	for k, v := range pubMap {
		merged[k] = v
	}
	// Preserve the encrypted secret if present.
	if v, ok := existing["appToken"]; ok {
		merged["appToken"] = v
	}

	if err := s.applySettingsEncryption(ctx, conn, merged); err != nil {
		return err
	}

	update := &models.ChannelUpdate{
		Settings:             &conn.Settings,
		SettingsPrivate:      conn.SettingsPrivate,
		SettingsPrivateKeys:  conn.SettingsPrivateKeys,
		ClearSettingsPrivate: conn.SettingsPrivate == nil,
	}

	return s.db.UpdateChannel(ctx, conn.UID, update)
}

// DeleteChannel deletes a connection.
func (s *Service) DeleteChannel(ctx context.Context, orgSlug, connectionUID string) error {
	org, err := s.db.GetOrganizationBySlug(ctx, orgSlug)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrOrganizationNotFound
		}

		return err
	}

	conn, err := s.db.GetChannel(ctx, connectionUID)
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

	return s.db.DeleteChannel(ctx, connectionUID)
}
