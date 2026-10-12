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

var errIncompletePair = errors.New("webpush: stored VAPID keypair is incomplete")

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

	// 2. Read from the database. Any read error other than "not found" is
	// returned: a pair is never generated against a database we cannot read.
	pub, priv, err := readStoredPair(ctx, dbSvc)
	if err != nil {
		return "", "", err
	}

	if (pub == "") != (priv == "") {
		// The two reads are not atomic: a concurrent creator may have committed
		// between them. Re-read once before treating this as a partial state.
		pub, priv, err = readStoredPair(ctx, dbSvc)
		if err != nil {
			return "", "", err
		}
	}

	if pub != "" && priv != "" {
		return pub, priv, nil
	}

	// 3. Generate a new keypair and persist it atomically.
	generatedPriv, generatedPub, err := webpushgo.GenerateVAPIDKeys()
	if err != nil {
		return "", "", fmt.Errorf("webpush: generate VAPID keys: %w", err)
	}

	if err = persistPair(ctx, dbSvc, generatedPub, generatedPriv, pub != "" || priv != ""); err != nil {
		return "", "", err
	}

	// Re-read what is stored: if another process won the race, use its pair.
	pub, priv, err = readStoredPair(ctx, dbSvc)
	if err != nil {
		return "", "", err
	}

	if pub == "" || priv == "" {
		return "", "", errIncompletePair
	}

	if pub == generatedPub {
		slog.InfoContext(ctx, "Generated VAPID keys, persisted to app_settings")
	}

	return pub, priv, nil
}

// persistPair stores a freshly generated pair. A partial pre-existing state
// is replaced; otherwise the insert leaves an already-stored pair untouched.
func persistPair(ctx context.Context, dbSvc db.Service, pub, priv string, repair bool) error {
	pair := map[string]string{keyPublic: pub, keyPrivate: priv}

	var err error
	if repair {
		slog.InfoContext(ctx, "webpush: repairing incomplete VAPID keypair in app_settings")

		err = dbSvc.SetAppSettings(ctx, pair)
	} else {
		err = dbSvc.CreateAppSettingsIfAbsent(ctx, pair)
	}

	if err != nil {
		return fmt.Errorf("webpush: persist VAPID keys: %w", err)
	}

	return nil
}

// readStoredPair reads both keys; a missing key yields "" without error.
func readStoredPair(ctx context.Context, dbSvc db.Service) (string, string, error) {
	pub, err := readSetting(ctx, dbSvc, keyPublic)
	if err != nil {
		return "", "", err
	}

	priv, err := readSetting(ctx, dbSvc, keyPrivate)
	if err != nil {
		return "", "", err
	}

	return pub, priv, nil
}

func readSetting(ctx context.Context, dbSvc db.Service, key string) (string, error) {
	val, err := dbSvc.GetAppSetting(ctx, key)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", nil
		}

		return "", fmt.Errorf("webpush: read %s: %w", key, err)
	}

	return val, nil
}
