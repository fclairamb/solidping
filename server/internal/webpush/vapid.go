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

// Config holds the web-push configuration used for VAPID key management.
// It mirrors the relevant fields from config.WebPushConfig.
type Config struct {
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
func GetOrCreateVAPIDKeys(ctx context.Context, cfg Config, dbSvc db.Service) (string, string, error) {
	// 1. Use config values when both are present.
	if cfg.VAPIDPublicKey != "" && cfg.VAPIDPrivateKey != "" {
		return cfg.VAPIDPublicKey, cfg.VAPIDPrivateKey, nil
	}

	// 2. Try to read from the database.
	pub, pubErr := readVAPIDFromDB(ctx, dbSvc)
	var priv string

	if pubErr == nil && pub != "" {
		var privErr error
		priv, privErr = dbSvc.GetAppSetting(ctx, keyPrivate)
		if privErr == nil && priv != "" {
			return pub, priv, nil
		}
	}

	// 3. Generate a new keypair and persist.
	generatedPriv, generatedPub, err := webpushgo.GenerateVAPIDKeys()
	if err != nil {
		return "", "", fmt.Errorf("webpush: generate VAPID keys: %w", err)
	}

	if setErr := dbSvc.SetAppSetting(ctx, keyPublic, generatedPub); setErr != nil {
		slog.WarnContext(ctx, "webpush: failed to persist VAPID public key", "err", setErr)
		return generatedPub, generatedPriv, nil
	}

	if setErr := dbSvc.SetAppSetting(ctx, keyPrivate, generatedPriv); setErr != nil {
		slog.WarnContext(ctx, "webpush: failed to persist VAPID private key", "err", setErr)
	}

	slog.InfoContext(ctx, "Generated VAPID keys, persisted to app_settings")

	return generatedPub, generatedPriv, nil
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
