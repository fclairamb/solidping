package credmigrate

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/fclairamb/solidping/server/internal/crypto/credentials"
	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
)

// urlReconcileKeys are the connection-settings keys that used to be encrypted
// (listed in the connection secret registry) but are now intentionally
// plaintext. A row needs reconciling when one of these still sits inside the
// encrypted private blob (recorded in settings_private_keys). Re-running once
// the key has been demoted is a no-op.
//
//nolint:gochecknoglobals // small constant lookup set
var urlReconcileKeys = map[string]struct{}{
	"url":         {}, // webhook
	"webhook_url": {}, // discord / googlechat / mattermost
}

// ReconcileStats reports how many connection rows the URL backfill touched.
type ReconcileStats struct {
	ConnectionsScanned, ConnectionsReconciled int
}

// ReconcileConnectionRegistry performs the one-shot, idempotent backfill that
// re-aligns stored connection settings with the current secret registry:
//   - it demotes URL fields (webhook `url`, discord/googlechat/mattermost
//     `webhook_url`) from the encrypted `settings_private` blob to the public
//     `settings` JSONB;
//   - it moves a key the registry declares secret but that still sits in the
//     public `settings` (ntfy's legacy `accessToken`, declared after rows were
//     written with it) into the encrypted blob.
//
// For each affected connection it:
//  1. reconstitutes the full plaintext = MergeConfig(public, DecryptForOrg(private));
//  2. re-splits via SplitConfig against the *current* ConnectionSecretFields,
//     moving the URL keys from private → public;
//  3. re-encrypts the remaining secrets (or nulls settings_private /
//     settings_private_keys when none remain) and writes the row.
//
// Idempotency: rows whose settings_private_keys no longer contains a URL key
// and whose public settings hold no declared secret are skipped, so re-running
// is a no-op. No-op as a whole when encryption is
// disabled — those rows already store the URL in plaintext public settings and
// are fixed by the registry change alone.
//
// Operates through the db.Service abstraction on existing columns: no schema
// migration and identical behavior on SQLite and Postgres.
func ReconcileConnectionRegistry(
	ctx context.Context, dbSvc db.Service, creds credentials.Service, opts Options,
) (ReconcileStats, error) {
	stats := ReconcileStats{}

	// When encryption is disabled the URL already lives in public settings;
	// the registry change is sufficient. Nothing to reconcile.
	if creds == nil || !creds.Enabled() {
		return stats, nil
	}

	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}

	orgs, err := dbSvc.ListOrganizations(ctx)
	if err != nil {
		return stats, fmt.Errorf("list organizations: %w", err)
	}

	for _, org := range orgs {
		conns, listErr := dbSvc.ListChannels(ctx, &models.ListIntegrationsFilter{
			OrganizationUID: org.UID,
		})
		if listErr != nil {
			return stats, fmt.Errorf("list connections for org %s: %w", org.UID, listErr)
		}

		for _, conn := range conns {
			stats.ConnectionsScanned++

			reconciled, recErr := reconcileConnection(ctx, dbSvc, creds, org, conn, opts, logger)
			if recErr != nil {
				return stats, recErr
			}

			if reconciled {
				stats.ConnectionsReconciled++
			}
		}
	}

	return stats, nil
}

// reconcileConnection reconciles a single connection. Returns whether the row
// was (or would be, on DryRun) reconciled.
func reconcileConnection(
	ctx context.Context, dbSvc db.Service, creds credentials.Service,
	org *models.Organization, conn *models.Integration, opts Options, logger *slog.Logger,
) (bool, error) {
	secrets := credentials.ConnectionSecretFields(conn.Type)
	hasPrivate := conn.SettingsPrivate != nil && *conn.SettingsPrivate != ""

	// Idempotency gate: act only when a URL key is still recorded as private,
	// or when a key the registry now declares secret (e.g. ntfy's legacy
	// accessToken) still sits in the public settings.
	demoteURL := hasPrivate && privateKeysContainURL(conn.SettingsPrivateKeys)
	promoteSecret := publicHoldsSecret(conn.Settings, secrets)

	if !demoteURL && !promoteSecret {
		return false, nil
	}

	private := map[string]any{}

	if hasPrivate {
		dec, decErr := creds.DecryptForOrg(ctx, conn.OrganizationUID, *conn.SettingsPrivate)
		if decErr != nil {
			return false, fmt.Errorf("decrypt connection %s: %w", conn.UID, decErr)
		}

		private = dec
	}

	full := credentials.MergeConfig(conn.Settings, private)

	// Re-split against the *current* registry: URL keys are no longer secret,
	// so they land in public; declared secrets (old and newly declared) go
	// private.
	public, newPrivate := credentials.SplitConfig(full, secrets)

	logger.InfoContext(ctx, "reconcile-registry: re-splitting connection settings against the secret registry",
		"orgUid", org.UID, "connectionUid", conn.UID, "type", conn.Type,
		"demoteURL", demoteURL, "promoteSecret", promoteSecret,
		"remainingPrivateKeys", credentials.SortedKeys(newPrivate), "dryRun", opts.DryRun)

	if opts.DryRun {
		return true, nil
	}

	if err := writeReconciledConnection(ctx, dbSvc, creds, conn, public, newPrivate); err != nil {
		return false, err
	}

	return true, nil
}

// publicHoldsSecret reports whether the public settings still carry one of the
// declared secret keys.
func publicHoldsSecret(settings models.JSONMap, secrets []string) bool {
	for _, k := range secrets {
		if _, ok := settings[k]; ok {
			return true
		}
	}

	return false
}

// writeReconciledConnection persists the re-split connection: encrypts the
// remaining private keys (or nulls the private columns when none remain).
func writeReconciledConnection(
	ctx context.Context, dbSvc db.Service, creds credentials.Service,
	conn *models.Integration, public, newPrivate map[string]any,
) error {
	publicMap := models.JSONMap(public)
	update := &models.IntegrationUpdate{Settings: &publicMap}

	if len(newPrivate) == 0 {
		update.ClearSettingsPrivate = true
	} else {
		envelope, encErr := creds.EncryptForOrg(ctx, conn.OrganizationUID, newPrivate)
		if encErr != nil {
			return fmt.Errorf("encrypt connection %s: %w", conn.UID, encErr)
		}

		keysJSON, mErr := json.Marshal(credentials.SortedKeys(newPrivate))
		if mErr != nil {
			return fmt.Errorf("marshal keys for connection %s: %w", conn.UID, mErr)
		}

		keysStr := string(keysJSON)
		update.SettingsPrivate = &envelope
		update.SettingsPrivateKeys = &keysStr
	}

	if updErr := dbSvc.UpdateChannel(ctx, conn.UID, update); updErr != nil {
		return fmt.Errorf("update connection %s: %w", conn.UID, updErr)
	}

	return nil
}

// privateKeysContainURL reports whether the JSON-encoded settings_private_keys
// list still records one of the demoted URL keys.
func privateKeysContainURL(privateKeys *string) bool {
	if privateKeys == nil || *privateKeys == "" {
		return false
	}

	var keys []string
	if err := json.Unmarshal([]byte(*privateKeys), &keys); err != nil {
		return false
	}

	for _, k := range keys {
		if _, ok := urlReconcileKeys[k]; ok {
			return true
		}
	}

	return false
}
