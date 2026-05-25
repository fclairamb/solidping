package webpush

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"

	webpushgo "github.com/SherClockHolmes/webpush-go"

	"github.com/fclairamb/solidping/server/internal/db"
)

const (
	// keyPublic is the app_settings key for the VAPID public key.
	keyPublic = "webpush.vapid_public_key"
	// keyPrivate is the app_settings key for the VAPID private key.
	keyPrivate = "webpush.vapid_private_key"
)

// WebPushConfig holds the web-push configuration block from the app config.
type WebPushConfig struct {
	VAPIDPublicKey  string
	VAPIDPrivateKey string
	Subject         string
	Enabled         bool
}

// GetOrCreateVAPIDKeys returns the server's VAPID keypair. It reads
// VAPIDPublicKey / VAPIDPrivateKey from cfg first; if absent, reads from the
// app_settings table; if still absent, generates a new pair, persists both to
// app_settings, and returns them.
// Must be called once during app startup and the result stored in config.
func GetOrCreateVAPIDKeys(ctx context.Context, cfg WebPushConfig, dbSvc db.Service) (pub, priv string, err error) {
	// 1. Use config values when both are present.
	if cfg.VAPIDPublicKey != "" && cfg.VAPIDPrivateKey != "" {
		return cfg.VAPIDPublicKey, cfg.VAPIDPrivateKey, nil
	}

	// 2. Try to read from the database.
	pub, privErr := readVAPIDFromDB(ctx, dbSvc)
	if privErr == nil {
		priv, privErr = dbSvc.GetAppSetting(ctx, keyPrivate)
	}

	if privErr == nil && pub != "" && priv != "" {
		return pub, priv, nil
	}

	// 3. Generate a new keypair and persist.
	priv, pub, err = webpushgo.GenerateVAPIDKeys()
	if err != nil {
		return "", "", fmt.Errorf("webpush: generate VAPID keys: %w", err)
	}

	if setErr := dbSvc.SetAppSetting(ctx, keyPublic, pub); setErr != nil {
		slog.WarnContext(ctx, "webpush: failed to persist VAPID public key", "err", setErr)
		return pub, priv, nil
	}

	if setErr := dbSvc.SetAppSetting(ctx, keyPrivate, priv); setErr != nil {
		slog.WarnContext(ctx, "webpush: failed to persist VAPID private key", "err", setErr)
	}

	slog.InfoContext(ctx, "Generated VAPID keys, persisted to app_settings")

	return pub, priv, nil
}

// readVAPIDFromDB attempts to read the public key from app_settings.
// Returns ("", nil) — not an error — when the key simply doesn't exist yet.
func readVAPIDFromDB(ctx context.Context, dbSvc db.Service) (string, error) {
	val, err := dbSvc.GetAppSetting(ctx, keyPublic)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", nil
		}
		// Non-"not found" errors bubble up.
		return "", fmt.Errorf("webpush: read public key: %w", err)
	}

	return val, nil
}
