package credmigrate

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/checkers/registry"
	"github.com/fclairamb/solidping/server/internal/crypto/credentials"
	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
)

// PlaintextStats reports per-resource progress of the plaintext-envelope
// migration.
type PlaintextStats struct {
	ChecksScanned, ChecksMigrated           int
	CheckJobsScanned, CheckJobsMigrated     int
	ConnectionsScanned, ConnectionsMigrated int
}

// Migrated reports whether any row was (or would be) split.
func (s PlaintextStats) Migrated() int {
	return s.ChecksMigrated + s.CheckJobsMigrated + s.ConnectionsMigrated
}

// RunPlaintext is the no-master-key counterpart to Run: it moves secrets that
// were stored in the PUBLIC column (checks.config, check_jobs.config,
// integration_connections.settings) into a *plaintext envelope* in the matching
// private column, so the public column stops leaking them through the API.
//
// It is the startup reconciliation for databases written before this fix, where
// the plaintext fallback merged secrets back into the public column and left the
// private column NULL. The invariant "the public column never carries a secret
// value" must hold in both encrypted and no-key modes; Run does the encrypted
// side, RunPlaintext does the no-key side.
//
// Idempotent: a row whose private column is already set is skipped (it was
// already split — by the current write path, an earlier run, or the encrypted
// migration). Safe to run online — every UPDATE is per-row. No encryption is
// performed and no master key is required.
func RunPlaintext(ctx context.Context, dbSvc db.Service, opts Options) (PlaintextStats, error) {
	stats := PlaintextStats{}

	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}

	orgs, err := dbSvc.ListOrganizations(ctx)
	if err != nil {
		return stats, fmt.Errorf("list organizations: %w", err)
	}

	for _, org := range orgs {
		checkStats, jobStats, err := migrateChecksPlaintext(ctx, dbSvc, org, opts, logger)
		if err != nil {
			return stats, err
		}

		stats.ChecksScanned += checkStats.scanned
		stats.ChecksMigrated += checkStats.migrated
		stats.CheckJobsScanned += jobStats.scanned
		stats.CheckJobsMigrated += jobStats.migrated

		connStats, err := migrateConnectionsPlaintext(ctx, dbSvc, org, opts, logger)
		if err != nil {
			return stats, err
		}

		stats.ConnectionsScanned += connStats.scanned
		stats.ConnectionsMigrated += connStats.migrated
	}

	return stats, nil
}

// splitPublicSecrets returns (public, private) for a config map given the
// declared secret keys, or (nil, nil) when there is nothing to move.
func splitPublicSecrets(config map[string]any, secrets []string) (map[string]any, map[string]any) {
	if len(secrets) == 0 {
		return nil, nil
	}

	public, private := credentials.SplitConfig(config, secrets)
	if len(private) == 0 {
		return nil, nil
	}

	return public, private
}

func migrateChecksPlaintext(
	ctx context.Context, dbSvc db.Service,
	org *models.Organization, opts Options, logger *slog.Logger,
) (rowStats, rowStats, error) {
	checkStats := rowStats{}
	jobStats := rowStats{}

	checks, _, err := dbSvc.ListChecks(ctx, org.UID, nil)
	if err != nil {
		return checkStats, jobStats, fmt.Errorf("list checks for org %s: %w", org.UID, err)
	}

	for _, check := range checks {
		checkStats.scanned++

		if migrated, mErr := migrateOneCheckPlaintext(ctx, dbSvc, org, check, opts, logger); mErr != nil {
			return checkStats, jobStats, mErr
		} else if migrated {
			checkStats.migrated++
		}

		// The denormalized dispatch rows can carry the same leaked secret.
		jobRows, jErr := migrateCheckJobsPlaintext(ctx, dbSvc, org, check, opts, logger)
		if jErr != nil {
			return checkStats, jobStats, jErr
		}

		jobStats.scanned += jobRows.scanned
		jobStats.migrated += jobRows.migrated
	}

	return checkStats, jobStats, nil
}

func migrateOneCheckPlaintext(
	ctx context.Context, dbSvc db.Service,
	org *models.Organization, check *models.Check, opts Options, logger *slog.Logger,
) (bool, error) {
	if check.ConfigPrivate != nil && *check.ConfigPrivate != "" {
		return false, nil
	}

	cfg, ok := registry.ParseConfig(checkerdef.CheckType(check.Type))
	if !ok {
		return false, nil
	}

	public, private := splitPublicSecrets(check.Config, credentials.SecretFieldsFor(cfg))
	if private == nil {
		return false, nil
	}

	logger.InfoContext(ctx, "plaintext-split: check has public-column secrets",
		"orgUid", org.UID, "checkUid", check.UID, "type", check.Type,
		"keys", credentials.SortedKeys(private), "dryRun", opts.DryRun)

	if opts.DryRun {
		return true, nil
	}

	envelope, keysStr, err := sealAndKeys(private)
	if err != nil {
		return false, fmt.Errorf("seal check %s: %w", check.UID, err)
	}

	publicMap := models.JSONMap(public)
	if updErr := dbSvc.UpdateCheck(ctx, check.UID, &models.CheckUpdate{
		Config:            &publicMap,
		ConfigPrivate:     &envelope,
		ConfigPrivateKeys: &keysStr,
	}); updErr != nil {
		return false, fmt.Errorf("update check %s: %w", check.UID, updErr)
	}

	return true, nil
}

func migrateCheckJobsPlaintext(
	ctx context.Context, dbSvc db.Service,
	org *models.Organization, check *models.Check, opts Options, logger *slog.Logger,
) (rowStats, error) {
	stats := rowStats{}

	jobs, err := dbSvc.ListCheckJobsByCheckUID(ctx, check.UID)
	if err != nil {
		return stats, fmt.Errorf("list check jobs for check %s: %w", check.UID, err)
	}

	for _, job := range jobs {
		stats.scanned++

		if job.ConfigPrivate != nil && *job.ConfigPrivate != "" {
			continue
		}

		cfg, ok := registry.ParseConfig(checkerdef.CheckType(job.Type))
		if !ok {
			continue
		}

		public, private := splitPublicSecrets(job.Config, credentials.SecretFieldsFor(cfg))
		if private == nil {
			continue
		}

		stats.migrated++
		logger.InfoContext(ctx, "plaintext-split: check job has public-column secrets",
			"orgUid", org.UID, "checkJobUid", job.UID, "type", job.Type,
			"keys", credentials.SortedKeys(private), "dryRun", opts.DryRun)

		if opts.DryRun {
			continue
		}

		if updErr := updateJobPlaintext(ctx, dbSvc, job.UID, public, private); updErr != nil {
			return stats, updErr
		}
	}

	return stats, nil
}

// updateJobPlaintext writes the split public config plus the plaintext envelope
// onto a check_jobs row. check_jobs has no db.Service update helper, so it goes
// through the shared bun handle (identical on SQLite and Postgres).
func updateJobPlaintext(
	ctx context.Context, dbSvc db.Service, jobUID string, public, private map[string]any,
) error {
	envelope, keysStr, err := sealAndKeys(private)
	if err != nil {
		return fmt.Errorf("seal check job %s: %w", jobUID, err)
	}

	if _, err := dbSvc.DB().NewUpdate().
		Model((*models.CheckJob)(nil)).
		Set("config = ?", models.JSONMap(public)).
		Set("config_private = ?", envelope).
		Set("config_private_keys = ?", keysStr).
		Set("encrypted = ?", true).
		Set("updated_at = ?", time.Now()).
		Where("uid = ?", jobUID).
		Exec(ctx); err != nil {
		return fmt.Errorf("update check job %s: %w", jobUID, err)
	}

	return nil
}

func migrateConnectionsPlaintext(
	ctx context.Context, dbSvc db.Service,
	org *models.Organization, opts Options, logger *slog.Logger,
) (rowStats, error) {
	stats := rowStats{}

	conns, err := dbSvc.ListChannels(ctx, &models.ListIntegrationsFilter{
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

		public, private := splitPublicSecrets(conn.Settings, credentials.ConnectionSecretFields(conn.Type))
		if private == nil {
			continue
		}

		stats.migrated++
		logger.InfoContext(ctx, "plaintext-split: connection has public-column secrets",
			"orgUid", org.UID, "connectionUid", conn.UID, "type", conn.Type,
			"keys", credentials.SortedKeys(private), "dryRun", opts.DryRun)

		if opts.DryRun {
			continue
		}

		envelope, keysStr, sErr := sealAndKeys(private)
		if sErr != nil {
			return stats, fmt.Errorf("seal connection %s: %w", conn.UID, sErr)
		}

		publicMap := models.JSONMap(public)
		if updErr := dbSvc.UpdateChannel(ctx, conn.UID, &models.IntegrationUpdate{
			Settings:            &publicMap,
			SettingsPrivate:     &envelope,
			SettingsPrivateKeys: &keysStr,
		}); updErr != nil {
			return stats, fmt.Errorf("update connection %s: %w", conn.UID, updErr)
		}
	}

	return stats, nil
}

// sealAndKeys builds the plaintext envelope and the sorted JSON key list for a
// private map.
func sealAndKeys(private map[string]any) (string, string, error) {
	envelope, err := credentials.SealPlaintext(private)
	if err != nil {
		return "", "", err
	}

	keysJSON, err := json.Marshal(credentials.SortedKeys(private))
	if err != nil {
		return "", "", fmt.Errorf("marshal private keys: %w", err)
	}

	return envelope, string(keysJSON), nil
}
