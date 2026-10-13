package sqlite

import (
	"context"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/uptrace/bun/migrate"
)

// initSuffix is appended to the database path for the database being built on
// a first start. It only becomes the real file once fully migrated.
const initSuffix = ".init"

// sidecarSuffixes are the files SQLite keeps next to a database in WAL mode.
//
//nolint:gochecknoglobals // constant list
var sidecarSuffixes = []string{"", "-wal", "-shm"}

// removeInitFiles deletes the leftovers of a first boot that was interrupted
// (killed container, host reboot): only that can leave a file under the
// temporary name.
func removeInitFiles(ctx context.Context, dbPath string) {
	for _, suffix := range sidecarSuffixes {
		path := dbPath + initSuffix + suffix

		if _, err := os.Stat(path); err != nil {
			continue
		}

		slog.WarnContext(ctx, "Removing leftover of an interrupted first start", "file", path)

		if err := os.Remove(path); err != nil {
			slog.WarnContext(ctx, "Failed to remove leftover", "file", path, "error", err)
		}
	}
}

// bootstrapDatabase builds a database that does not exist yet under a temporary
// name, runs every migration on it, checkpoints and closes it, and renames it
// to dbPath. Until the rename, dbPath does not exist, so an interrupted first
// start cannot leave a database that claims a schema it does not have.
func bootstrapDatabase(ctx context.Context, cfg *Config, dbPath string, migrations fs.FS) error {
	slog.InfoContext(ctx, "New database: building it under a temporary name", "path", dbPath)

	initPath := dbPath + initSuffix

	bunDB, err := openPool(ctx, cfg, initPath, false)
	if err != nil {
		removeInitFiles(ctx, dbPath)

		return err
	}

	tmp := &Service{db: bunDB, guardMode: cfg.GuardMode, migrations: migrations}

	err = tmp.migrate(ctx)
	if err == nil {
		// Fold the WAL into the main file so nothing is left in a sidecar.
		_, err = bunDB.ExecContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE)")
		if err != nil {
			err = fmt.Errorf("failed to checkpoint the new database: %w", err)
		}
	}

	closeErr := bunDB.Close()

	if err == nil && closeErr != nil {
		err = fmt.Errorf("failed to close the new database: %w", closeErr)
	}

	if err != nil {
		removeInitFiles(ctx, dbPath)

		return err
	}

	// After a clean close SQLite removes the sidecars itself; drop any straggler
	// (their content is already folded in by the checkpoint).
	for _, suffix := range sidecarSuffixes[1:] {
		_ = os.Remove(initPath + suffix)
	}

	if err := os.Rename(initPath, dbPath); err != nil {
		removeInitFiles(ctx, dbPath)

		return fmt.Errorf("failed to move the new database into place: %w", err)
	}

	return nil
}

// backupBeforeMigrating copies an existing database to
// <db>.pre-<first pending migration>.bak before migrations run on it, keeping
// only the latest backup. It returns the backup path, or "" when no backup was
// made (in-memory or brand new database, nothing pending, not enough free disk
// space, or the copy failed: an upgrade is never blocked by its safety net).
func (s *Service) backupBeforeMigrating(ctx context.Context, migrator *migrate.Migrator) string {
	if s.dbPath == "" {
		return ""
	}

	all, err := migrator.MigrationsWithStatus(ctx)
	if err != nil {
		return ""
	}

	pending, applied := all.Unapplied(), all.Applied()
	if len(pending) == 0 || len(applied) == 0 {
		return ""
	}

	size := fileSize(s.dbPath) + fileSize(s.dbPath+"-wal")
	if free, ok := freeDiskBytes(filepath.Dir(s.dbPath)); ok && free < 2*uint64(size) {
		slog.WarnContext(ctx, "Skipping the pre-upgrade database backup: not enough free disk space",
			"freeBytes", free, "databaseBytes", size)

		return ""
	}

	backup := fmt.Sprintf("%s.pre-%s.bak", s.dbPath, pending[0].Name)
	partial := backup + ".part"

	_ = os.Remove(partial)

	if _, err := s.db.ExecContext(ctx, "VACUUM INTO ?", partial); err != nil {
		_ = os.Remove(partial)

		slog.WarnContext(ctx, "Failed to back up the database before upgrading", "error", err)

		return ""
	}

	// Keep only the latest backup: drop the older ones, then publish this one.
	older, _ := filepath.Glob(s.dbPath + ".pre-*.bak")
	for _, path := range older {
		_ = os.Remove(path)
	}

	if err := os.Rename(partial, backup); err != nil {
		_ = os.Remove(partial)

		slog.WarnContext(ctx, "Failed to store the pre-upgrade database backup", "error", err)

		return ""
	}

	slog.InfoContext(ctx, "Backed up the database before upgrading",
		"backup", backup, "pending", len(pending), "firstPending", pending[0].Name)

	return backup
}

func fileSize(path string) int64 {
	info, err := os.Stat(path)
	if err != nil {
		return 0
	}

	return info.Size()
}
