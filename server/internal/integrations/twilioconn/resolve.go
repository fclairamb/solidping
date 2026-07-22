// Package twilioconn resolves and decrypts the org-scoped Twilio connection
// used by the phone verification flow, the escalation dispatcher, and the
// inbound voice/status callbacks. It centralizes the decrypt-and-merge dance so
// every caller sees plaintext TwilioSettings without duplicating the credential
// handling.
package twilioconn

import (
	"context"
	"errors"
	"fmt"

	"github.com/fclairamb/solidping/server/internal/crypto/credentials"
	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
)

// ErrNoTwilioConnection is returned when the org has no enabled Twilio
// connection to send through.
var ErrNoTwilioConnection = errors.New("no enabled twilio connection configured for organization")

// ErrEncryptionDisabled is returned when a connection carries an encrypted
// envelope but no master key is available to open it.
var ErrEncryptionDisabled = errors.New("connection has encrypted settings but encryption is disabled")

// DecryptSettings returns the plaintext TwilioSettings for a specific
// integration, opening its encrypted envelope when present. creds may be nil
// only when the connection has no encrypted envelope (plaintext fallback).
func DecryptSettings(
	ctx context.Context, creds credentials.Service, conn *models.Integration,
) (*models.TwilioSettings, error) {
	settings := conn.Settings

	if conn.SettingsPrivate != nil && *conn.SettingsPrivate != "" {
		if creds == nil || (credentials.RequiresKey(*conn.SettingsPrivate) && !creds.Enabled()) {
			return nil, fmt.Errorf("%w: %s", ErrEncryptionDisabled, conn.UID)
		}

		private, err := creds.DecryptForOrg(ctx, conn.OrganizationUID, *conn.SettingsPrivate)
		if err != nil {
			return nil, fmt.Errorf("decrypt twilio settings: %w", err)
		}

		settings = models.JSONMap(credentials.MergeConfig(conn.Settings, private))
	}

	ts, err := models.TwilioSettingsFromJSONMap(settings)
	if err != nil {
		return nil, fmt.Errorf("parse twilio settings: %w", err)
	}

	return ts, nil
}

// ResolveDefault finds the org's default enabled Twilio connection (preferring
// the one flagged IsDefault, else the first) and returns it with decrypted
// settings. Returns ErrNoTwilioConnection when none exists.
func ResolveDefault(
	ctx context.Context, dbSvc db.Service, creds credentials.Service, orgUID string,
) (*models.Integration, *models.TwilioSettings, error) {
	conn, err := resolveDefaultConn(ctx, dbSvc, orgUID)
	if err != nil {
		return nil, nil, err
	}

	settings, err := DecryptSettings(ctx, creds, conn)
	if err != nil {
		return nil, nil, err
	}

	return conn, settings, nil
}

func resolveDefaultConn(
	ctx context.Context, dbSvc db.Service, orgUID string,
) (*models.Integration, error) {
	twilioType := models.ConnectionTypeTwilio
	enabled := true

	conns, err := dbSvc.ListChannels(ctx, &models.ListIntegrationsFilter{
		OrganizationUID: orgUID,
		Type:            &twilioType,
		Enabled:         &enabled,
	})
	if err != nil {
		return nil, fmt.Errorf("list twilio connections: %w", err)
	}

	if len(conns) == 0 {
		return nil, ErrNoTwilioConnection
	}

	for _, c := range conns {
		if c.IsDefault {
			return c, nil
		}
	}

	return conns[0], nil
}
