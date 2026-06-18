package freebox

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/fclairamb/solidping/server/internal/crypto/credentials"
	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
)

// ErrFreeboxChannelNotFound is returned when the requested channel does not
// exist, belongs to another organization, or is not a Freebox channel. It is
// deliberately coarse so callers can map any of those to a single 404/422
// without leaking which condition failed.
var ErrFreeboxChannelNotFound = errors.New("freebox: channel not found")

// ListLanHostsForChannel resolves a paired Freebox channel by UID, builds an
// authenticated client from its (decrypted) app_token, and returns the LAN
// hosts the Freebox knows about.
//
// It is the handler-independent core of what the channels service used to do
// inline: the discovery job calls it directly with the job context's db +
// credentials services, and channels.ListFreeboxLanHosts delegates to it.
// Depending only on db.Service + credentials.Service keeps the freebox package
// free of any handlers import (avoiding a layering inversion).
//
// Returns ErrFreeboxNotGranted when the channel exists but has not finished
// the LCD-pairing flow (no app_token, or status != granted), and
// ErrFreeboxChannelNotFound when the channel is missing / wrong org / wrong
// type.
func ListLanHostsForChannel(
	ctx context.Context,
	dbSvc db.Service,
	creds credentials.Service,
	orgUID, channelUID string,
) ([]LanHost, error) {
	settings, appToken, err := resolveGrantedFreeboxChannel(ctx, dbSvc, creds, orgUID, channelUID)
	if err != nil {
		return nil, err
	}

	client := NewClientWithAppID(settings.BaseURL, settings.AppID, appToken)

	hosts, err := ListLanHosts(ctx, client)
	if err != nil {
		return nil, fmt.Errorf("list lan hosts: %w", err)
	}

	return hosts, nil
}

// resolveGrantedFreeboxChannel loads the channel, verifies it belongs to the
// org and is a granted Freebox connection, and returns its public settings
// plus the decrypted app_token. Mirrors the channels service's
// loadGrantedFreeboxChannel + resolveFreeboxAppToken, but with no handler
// dependencies.
func resolveGrantedFreeboxChannel(
	ctx context.Context,
	dbSvc db.Service,
	creds credentials.Service,
	orgUID, channelUID string,
) (*models.FreeboxSettings, string, error) {
	conn, err := dbSvc.GetChannel(ctx, channelUID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, "", ErrFreeboxChannelNotFound
		}

		return nil, "", fmt.Errorf("get channel: %w", err)
	}

	if conn.OrganizationUID != orgUID || conn.Type != models.ConnectionTypeFreebox {
		return nil, "", ErrFreeboxChannelNotFound
	}

	settings, err := models.FreeboxSettingsFromJSONMap(conn.Settings)
	if err != nil {
		return nil, "", fmt.Errorf("parse freebox settings: %w", err)
	}

	appToken, err := resolveAppToken(ctx, creds, conn)
	if err != nil {
		return nil, "", err
	}

	if appToken == "" || settings.Status != models.FreeboxStatusGranted {
		return nil, "", ErrFreeboxNotGranted
	}

	return settings, appToken, nil
}

// resolveAppToken decrypts the app_token stored on a Freebox channel. Honors
// both the encrypted-private path and the plaintext fallback (credentials
// disabled), matching the channels service's resolveFreeboxAppToken.
func resolveAppToken(
	ctx context.Context, creds credentials.Service, conn *models.Integration,
) (string, error) {
	if conn.SettingsPrivate != nil && *conn.SettingsPrivate != "" {
		if !creds.Enabled() {
			return "", fmt.Errorf("decrypt freebox app_token: %w (credentials disabled)", ErrFreeboxNotGranted)
		}

		priv, err := creds.DecryptForOrg(ctx, conn.OrganizationUID, *conn.SettingsPrivate)
		if err != nil {
			return "", fmt.Errorf("decrypt freebox app_token: %w", err)
		}

		if v, ok := priv["appToken"].(string); ok && v != "" {
			return v, nil
		}
	}

	// Plaintext fallback (credentials disabled): the same key lives on the
	// public Settings map.
	if v, ok := conn.Settings["appToken"].(string); ok {
		return v, nil
	}

	return "", nil
}
