package webpush

import (
	"context"
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

// ErrIncompleteVAPIDKeys is returned when, right after a write, app_settings
// still does not hold both halves of the VAPID keypair.
var ErrIncompleteVAPIDKeys = errors.New("webpush: stored VAPID keypair is incomplete")

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
// app_settings table; if still absent, generates a new pair, stores it, and
// returns what app_settings holds afterwards.
//
// It never returns a pair that is not stored: a database that cannot be read
// or written is an error, never a freshly generated pair that the next restart
// would replace (every browser subscribed in between would be bound to a key
// the server no longer signs with). It therefore needs a migrated database,
// and runs from Server.Initialize, after the migrations.
func GetOrCreateVAPIDKeys(ctx context.Context, cfg Config, dbSvc db.Service) (string, string, error) {
	// 1. Use config values when both are present. No database access.
	if cfg.VAPIDPublicKey != "" && cfg.VAPIDPrivateKey != "" {
		return cfg.VAPIDPublicKey, cfg.VAPIDPrivateKey, nil
	}

	// 2. Read the stored pair.
	pub, priv, err := readVAPIDFromDB(ctx, dbSvc)
	if err != nil {
		return "", "", err
	}

	if pub != "" && priv != "" {
		return pub, priv, nil
	}

	// 3. Generate a new keypair and store it.
	generatedPriv, generatedPub, err := webpushgo.GenerateVAPIDKeys()
	if err != nil {
		return "", "", fmt.Errorf("webpush: generate VAPID keys: %w", err)
	}

	pair := map[string]string{keyPublic: generatedPub, keyPrivate: generatedPriv}
	if err = storeVAPIDKeys(ctx, dbSvc, pair, pub != "" || priv != ""); err != nil {
		return "", "", err
	}

	// 4. Return what is stored, not what was generated.
	storedPub, storedPriv, err := readVAPIDFromDB(ctx, dbSvc)
	if err != nil {
		return "", "", err
	}

	if storedPub == "" || storedPriv == "" {
		return "", "", ErrIncompleteVAPIDKeys
	}

	if storedPub == generatedPub {
		slog.InfoContext(ctx, "Generated VAPID keys, persisted to app_settings")
	}

	return storedPub, storedPriv, nil
}

// storeVAPIDKeys writes a freshly generated pair. halfStored reports that
// app_settings holds one half of a pair only.
func storeVAPIDKeys(ctx context.Context, dbSvc db.Service, pair map[string]string, halfStored bool) error {
	if !halfStored {
		// Nothing stored. Insert-if-absent: when another process races us,
		// whoever commits first wins and the other one keeps the winner's pair.
		if err := dbSvc.CreateAppSettingsIfAbsent(ctx, pair); err != nil {
			return fmt.Errorf("webpush: store VAPID keys: %w", err)
		}

		return nil
	}

	// Half a pair, left by the old two-statement write. It cannot sign
	// anything, so replace both halves in one statement.
	slog.InfoContext(ctx, "webpush: stored VAPID keypair is incomplete, replacing it")

	if err := dbSvc.SetAppSettings(ctx, pair); err != nil {
		return fmt.Errorf("webpush: replace VAPID keys: %w", err)
	}

	return nil
}

// readVAPIDFromDB reads both halves of the keypair from app_settings in one
// statement. Two separate reads could straddle another process's insert and
// see a public key without its private key, which would look like a broken
// pair to repair. A missing key reads as "" with no error; any other failure
// (no app_settings table, a broken connection) is returned.
func readVAPIDFromDB(ctx context.Context, dbSvc db.Service) (string, string, error) {
	values, err := dbSvc.GetAppSettings(ctx, []string{keyPublic, keyPrivate})
	if err != nil {
		return "", "", fmt.Errorf("webpush: read VAPID keys: %w", err)
	}

	return values[keyPublic], values[keyPrivate], nil
}
