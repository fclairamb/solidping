package freebox

import (
	"context"
	"errors"
	"fmt"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

// ErrNilSettings is returned when a service-layer helper is called
// without a settings struct — callers that mean "no settings" should
// pass a zero-valued struct, not nil.
var ErrNilSettings = errors.New("freebox: nil settings")

// StartPairing kicks off the Freebox pairing flow. The returned
// AuthorizeResult contains the permanent app_token and the short-lived
// track_id used to poll the LCD-approval status; both must be persisted
// onto the IntegrationConnection record by the caller (app_token
// encrypted, track_id in the public settings).
//
// The caller is expected to mutate `settings` to reflect the new state
// (Status = pairing, TrackID = result.TrackID); we leave that side
// effect to the caller so the service stays free of DB concerns.
func StartPairing(
	ctx context.Context, settings *models.FreeboxSettings,
) (*AuthorizeResult, error) {
	if settings == nil {
		return nil, ErrNilSettings
	}

	appID := settings.AppID
	if appID == "" {
		appID = DefaultAppID
	}

	deviceName := settings.DeviceName
	if deviceName == "" {
		deviceName = DefaultDeviceName
	}

	// We don't yet have an app_token; pass empty — Authorize doesn't
	// need it.
	client := NewClientWithAppID(settings.BaseURL, appID, "")

	result, err := client.Authorize(ctx, deviceName)
	if err != nil {
		return nil, fmt.Errorf("authorize: %w", err)
	}

	return result, nil
}

// CheckPairingStatus polls /login/authorize/{trackID} once and returns
// the raw status string (StatusUnknown / StatusPending / StatusGranted /
// StatusDenied / StatusTimeout). Returns ErrPairingTimeout /
// ErrPairingDenied when the Freebox itself confirms a terminal failure
// — those are the only two cases where re-pairing is required.
func CheckPairingStatus(
	ctx context.Context, settings *models.FreeboxSettings, trackID int,
) (string, error) {
	if settings == nil {
		return "", ErrNilSettings
	}

	client := NewClientWithAppID(settings.BaseURL, settings.AppID, "")

	status, err := client.PollPairing(ctx, trackID)
	if err != nil {
		return "", fmt.Errorf("poll pairing: %w", err)
	}

	switch status {
	case StatusGranted, StatusPending, StatusUnknown:
		return status, nil
	case StatusDenied:
		return status, ErrPairingDenied
	case StatusTimeout:
		return status, ErrPairingTimeout
	default:
		// Unknown status string — surface it so the UI can show it
		// verbatim while we keep the pairing record around.
		return status, nil
	}
}

// ValidateConnection opens a session against the Freebox using the
// stored app_token and immediately closes it (no side effect). Used by
// the channel handler when the operator clicks "test connection".
//
// Returns ErrAuthRequired if the Freebox no longer accepts the token
// (revoked from the admin), and the usual transport errors otherwise.
func ValidateConnection(
	ctx context.Context,
	settings *models.FreeboxSettings,
	priv *models.FreeboxPrivateSettings,
) error {
	if settings == nil {
		return ErrNilSettings
	}

	if priv == nil || priv.AppToken == "" {
		return ErrAuthRequired
	}

	client := NewClientWithAppID(settings.BaseURL, settings.AppID, priv.AppToken)

	if _, err := client.ensureSession(ctx); err != nil {
		return err
	}

	return nil
}
