// Package credmigrate implements the one-shot migration that walks every
// secret-bearing row whose private column is NULL but whose public column
// still has plaintext secrets, splits them, and encrypts the secret side
// under the org DEK.
//
// Idempotent: re-running on an already-encrypted database is a no-op
// (rows whose private column is non-NULL are skipped). Safe to run while
// the server is online — every UPDATE is per-row.
package credmigrate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/checkers/registry"
	"github.com/fclairamb/solidping/server/internal/crypto/credentials"
	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
)

// ErrDisabled is returned when the credentials service has no master key
// configured. The migration sweep needs encryption to be enabled.
var ErrDisabled = errors.New("credentials service disabled — set SP_ENCRYPTION_MASTER_KEY first")

// Stats reports per-resource progress so the caller (CLI or auto-migrate
// at startup) can print a human-readable summary.
type Stats struct {
	ChecksScanned, ChecksMigrated           int
	ConnectionsScanned, ConnectionsMigrated int
}

// Options controls migration behavior.
type Options struct {
	// DryRun reports what would be encrypted without touching the database.
	DryRun bool
	// Logger is used for per-row progress; defaults to slog.Default().
	Logger *slog.Logger
}

// Run iterates checks and integration connections across every org and
// encrypts any plaintext secret keys it finds. Returns a Stats summary
// and an error on the first irrecoverable failure.
func Run(ctx context.Context, dbSvc db.Service, creds credentials.Service, opts Options) (Stats, error) {
	stats := Stats{}

	if creds == nil || !creds.Enabled() {
		return stats, ErrDisabled
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
		checkStats, err := migrateChecks(ctx, dbSvc, creds, org, opts, logger)
		if err != nil {
			return stats, err
		}

		stats.ChecksScanned += checkStats.scanned
		stats.ChecksMigrated += checkStats.migrated

		connStats, err := migrateConnections(ctx, dbSvc, creds, org, opts, logger)
		if err != nil {
			return stats, err
		}

		stats.ConnectionsScanned += connStats.scanned
		stats.ConnectionsMigrated += connStats.migrated
	}

	return stats, nil
}

type rowStats struct{ scanned, migrated int }

func migrateChecks(
	ctx context.Context, dbSvc db.Service, creds credentials.Service,
	org *models.Organization, opts Options, logger *slog.Logger,
) (rowStats, error) {
	stats := rowStats{}

	checks, _, err := dbSvc.ListChecks(ctx, org.UID, nil)
	if err != nil {
		return stats, fmt.Errorf("list checks for org %s: %w", org.UID, err)
	}

	for _, check := range checks {
		stats.scanned++

		if check.ConfigPrivate != nil && *check.ConfigPrivate != "" {
			continue
		}

		cfg, ok := registry.ParseConfig(checkerdef.CheckType(check.Type))
		if !ok {
			continue
		}

		secrets := credentials.SecretFieldsFor(cfg)
		if len(secrets) == 0 {
			continue
		}

		public, private := credentials.SplitConfig(check.Config, secrets)
		if len(private) == 0 {
			continue
		}

		stats.migrated++
		logger.InfoContext(ctx, "encrypt-credentials: check has plaintext secrets",
			"orgUid", org.UID, "checkUid", check.UID, "type", check.Type,
			"keys", credentials.SortedKeys(private), "dryRun", opts.DryRun)

		if opts.DryRun {
			continue
		}

		envelope, encErr := creds.EncryptForOrg(ctx, org.UID, private)
		if encErr != nil {
			return stats, fmt.Errorf("encrypt check %s: %w", check.UID, encErr)
		}

		keysJSON, mErr := json.Marshal(credentials.SortedKeys(private))
		if mErr != nil {
			return stats, fmt.Errorf("marshal keys for check %s: %w", check.UID, mErr)
		}

		keysStr := string(keysJSON)
		publicMap := models.JSONMap(public)
		update := &models.CheckUpdate{
			Config:            &publicMap,
			ConfigPrivate:     &envelope,
			ConfigPrivateKeys: &keysStr,
		}

		if updErr := dbSvc.UpdateCheck(ctx, check.UID, update); updErr != nil {
			return stats, fmt.Errorf("update check %s: %w", check.UID, updErr)
		}
	}

	return stats, nil
}

func migrateConnections(
	ctx context.Context, dbSvc db.Service, creds credentials.Service,
	org *models.Organization, opts Options, logger *slog.Logger,
) (rowStats, error) {
	stats := rowStats{}

	conns, err := dbSvc.ListChannels(ctx, &models.ListChannelsFilter{
		OrganizationUID: org.UID,
	})
	if err != nil {
		return stats, fmt.Errorf("list connections for org %s: %w", org.UID, err)
	}

	for _, conn := range conns {
		stats.scanned++

		if conn.SettingsPrivate != nil && *conn.SettingsPrivate != "" {
			continue
		}

		secrets := credentials.ConnectionSecretFields(conn.Type)
		if len(secrets) == 0 {
			continue
		}

		public, private := credentials.SplitConfig(conn.Settings, secrets)
		if len(private) == 0 {
			continue
		}

		stats.migrated++
		logger.InfoContext(ctx, "encrypt-credentials: connection has plaintext secrets",
			"orgUid", org.UID, "connectionUid", conn.UID, "type", conn.Type,
			"keys", credentials.SortedKeys(private), "dryRun", opts.DryRun)

		if opts.DryRun {
			continue
		}

		envelope, encErr := creds.EncryptForOrg(ctx, org.UID, private)
		if encErr != nil {
			return stats, fmt.Errorf("encrypt connection %s: %w", conn.UID, encErr)
		}

		keysJSON, mErr := json.Marshal(credentials.SortedKeys(private))
		if mErr != nil {
			return stats, fmt.Errorf("marshal keys for connection %s: %w", conn.UID, mErr)
		}

		keysStr := string(keysJSON)
		publicMap := models.JSONMap(public)
		update := &models.ChannelUpdate{
			Settings:            &publicMap,
			SettingsPrivate:     &envelope,
			SettingsPrivateKeys: &keysStr,
		}

		if updErr := dbSvc.UpdateChannel(ctx, conn.UID, update); updErr != nil {
			return stats, fmt.Errorf("update connection %s: %w", conn.UID, updErr)
		}
	}

	return stats, nil
}
