package credmigrate

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/fclairamb/solidping/server/internal/crypto/credentials"
	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/notifications"
)

// PushoverStats reports how many Pushover integrations the key backfill touched.
type PushoverStats struct {
	Scanned, Normalized, Skipped int
}

// NormalizePushoverSettings is the one-shot, idempotent backfill of spec
// 2026-10-08-03. The dashboard used to write a Pushover integration's
// credentials as `user` / `token`, which the sender (reading `userKey` /
// `apiToken`) never found and the secret registry (listing `user_key` /
// `api_token`) never encrypted. For every `pushover` integration it:
//  1. opens the effective settings (public plus decrypted private);
//  2. renames `user`/`userKey` to `user_key` and `token`/`apiToken` to
//     `api_token` (a canonical value already present wins) and drops the
//     legacy keys;
//  3. re-splits with SplitConfig, so both secrets land in settings_private:
//     encrypted under the org key when a credentials service is enabled, a
//     plaintext envelope otherwise (the documented no-master-key fallback).
//
// A row with no legacy key and no secret left in the public settings is
// skipped, so a second run is a no-op. A sealed row that cannot be opened (no
// usable key) is logged and skipped: the sender cannot read it either, and
// failing the whole boot over one row would be worse.
func NormalizePushoverSettings(
	ctx context.Context, dbSvc db.Service, creds credentials.Service, opts Options,
) (PushoverStats, error) {
	stats := PushoverStats{}

	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}

	orgs, err := dbSvc.ListOrganizations(ctx)
	if err != nil {
		return stats, fmt.Errorf("list organizations: %w", err)
	}

	pushoverType := models.ConnectionTypePushover

	for _, org := range orgs {
		conns, listErr := dbSvc.ListChannels(ctx, &models.ListIntegrationsFilter{
			OrganizationUID: org.UID,
			Type:            &pushoverType,
		})
		if listErr != nil {
			return stats, fmt.Errorf("list pushover integrations for org %s: %w", org.UID, listErr)
		}

		for _, conn := range conns {
			stats.Scanned++

			done, normErr := normalizePushoverConnection(ctx, dbSvc, creds, conn, opts, logger)
			if normErr != nil {
				return stats, normErr
			}

			switch done {
			case pushoverNormalized:
				stats.Normalized++
			case pushoverSkipped:
				stats.Skipped++
			case pushoverUnchanged:
			}
		}
	}

	if stats.Normalized > 0 || stats.Skipped > 0 {
		logger.InfoContext(ctx, "pushover settings backfill done",
			"scanned", stats.Scanned, "normalized", stats.Normalized,
			"skipped", stats.Skipped, "dryRun", opts.DryRun)
	}

	return stats, nil
}

type pushoverOutcome int

const (
	pushoverUnchanged pushoverOutcome = iota
	pushoverNormalized
	pushoverSkipped
)

func normalizePushoverConnection(
	ctx context.Context, dbSvc db.Service, creds credentials.Service,
	conn *models.Integration, opts Options, logger *slog.Logger,
) (pushoverOutcome, error) {
	if !pushoverNeedsNormalizing(conn) {
		return pushoverUnchanged, nil
	}

	full, err := credentials.OpenConnectionSettings(ctx, creds, conn)
	if err != nil {
		logger.WarnContext(ctx, "pushover settings backfill: cannot open settings, skipping",
			"connectionUid", conn.UID, "error", err)

		return pushoverSkipped, nil
	}

	renamePushoverKeys(full)

	public, private := credentials.SplitConfig(full, credentials.ConnectionSecretFields(conn.Type))

	logger.InfoContext(ctx, "pushover settings backfill: normalizing keys",
		"orgUid", conn.OrganizationUID, "connectionUid", conn.UID,
		"privateKeys", credentials.SortedKeys(private), "dryRun", opts.DryRun)

	if opts.DryRun {
		return pushoverNormalized, nil
	}

	publicMap := models.JSONMap(public)
	update := &models.IntegrationUpdate{Settings: &publicMap}

	if len(private) == 0 {
		update.ClearSettingsPrivate = true
	} else {
		envelope, sealErr := sealPushoverPrivate(ctx, creds, conn.OrganizationUID, private)
		if sealErr != nil {
			return pushoverUnchanged, fmt.Errorf("seal pushover integration %s: %w", conn.UID, sealErr)
		}

		keysJSON, mErr := json.Marshal(credentials.SortedKeys(private))
		if mErr != nil {
			return pushoverUnchanged, fmt.Errorf("marshal keys for pushover integration %s: %w", conn.UID, mErr)
		}

		keysStr := string(keysJSON)
		update.SettingsPrivate = &envelope
		update.SettingsPrivateKeys = &keysStr
	}

	if updErr := dbSvc.UpdateChannel(ctx, conn.UID, update); updErr != nil {
		return pushoverUnchanged, fmt.Errorf("update pushover integration %s: %w", conn.UID, updErr)
	}

	return pushoverNormalized, nil
}

// pushoverNeedsNormalizing reports whether a row still carries a legacy key
// (public settings or the recorded private keys) or a canonical secret in its
// public settings. Neither: the row is already canonical and is left alone.
func pushoverNeedsNormalizing(conn *models.Integration) bool {
	privateKeys := map[string]bool{}

	if conn.SettingsPrivateKeys != nil && *conn.SettingsPrivateKeys != "" {
		var keys []string
		if err := json.Unmarshal([]byte(*conn.SettingsPrivateKeys), &keys); err == nil {
			for _, k := range keys {
				privateKeys[k] = true
			}
		}
	}

	for _, legacy := range pushoverLegacyKeys() {
		if _, ok := conn.Settings[legacy]; ok || privateKeys[legacy] {
			return true
		}
	}

	for _, secret := range credentials.ConnectionSecretFields(models.ConnectionTypePushover) {
		if _, ok := conn.Settings[secret]; ok {
			return true
		}
	}

	return false
}

func pushoverLegacyKeys() []string {
	keys := make([]string, 0, len(notifications.PushoverLegacyUserKeys)+len(notifications.PushoverLegacyAPITokenKeys))
	keys = append(keys, notifications.PushoverLegacyUserKeys...)

	return append(keys, notifications.PushoverLegacyAPITokenKeys...)
}

// renamePushoverKeys moves legacy values onto the canonical keys in place. A
// non-empty canonical value is kept; the legacy keys are always removed.
func renamePushoverKeys(settings map[string]any) {
	moveFirst(settings, notifications.PushoverSettingUserKey, notifications.PushoverLegacyUserKeys)
	moveFirst(settings, notifications.PushoverSettingAPIToken, notifications.PushoverLegacyAPITokenKeys)
}

func moveFirst(settings map[string]any, canonical string, legacy []string) {
	current, _ := settings[canonical].(string)

	for _, key := range legacy {
		if v, ok := settings[key].(string); ok && v != "" && current == "" {
			current = v
		}

		delete(settings, key)
	}

	if current != "" {
		settings[canonical] = current
	}
}

// sealPushoverPrivate encrypts under the org key when encryption is on, and
// otherwise writes the plaintext envelope a keyless deployment uses.
func sealPushoverPrivate(
	ctx context.Context, creds credentials.Service, orgUID string, private map[string]any,
) (string, error) {
	if creds != nil && creds.Enabled() {
		return creds.EncryptForOrg(ctx, orgUID, private)
	}

	return credentials.SealPlaintext(private)
}
