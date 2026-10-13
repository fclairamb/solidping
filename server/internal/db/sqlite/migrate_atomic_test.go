package sqlite

import (
	"context"
	"database/sql"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/migrationguard"
	"github.com/fclairamb/solidping/server/internal/db/sqlitedriver"
)

// Spec 2026-10-13-01: an interrupted or failed migration is never recorded as
// applied, and an interrupted first boot leaves no database file behind.

const (
	atomicBase = "create table if not exists things (uid text primary key);"
	// atomicSecond has two statements; the second one fails. The first is
	// idempotent so the fixed version can run again over a half-applied one.
	atomicSecondBroken = "create table if not exists extras (uid text);\n" +
		"--bun:split\ninsert into no_such_table values (1);"
	atomicSecondFixed = "create table if not exists extras (uid text);\n" +
		"--bun:split\ninsert into things values ('x');"
	atomicThird = "create table third (uid text);"
)

func atomicFS(files map[string]string) fs.FS {
	fsys := fstest.MapFS{}
	for name, body := range files {
		fsys["migrations/"+name+".up.sql"] = &fstest.MapFile{Data: []byte(body)}
	}

	return fsys
}

func appliedMigrations(t *testing.T, svc *Service) []string {
	t.Helper()

	var names []string

	require.NoError(t, svc.db.NewRaw("select name from bun_migrations order by id").Scan(t.Context(), &names))

	return names
}

func tableExists(t *testing.T, db *sql.DB, name string) bool {
	t.Helper()

	var count int

	require.NoError(t, db.QueryRowContext(t.Context(),
		"select count(*) from sqlite_master where type = 'table' and name = ?", name).Scan(&count))

	return count > 0
}

func openAtomic(t *testing.T, dir string, files map[string]string) *Service {
	t.Helper()

	svc, err := newService(t.Context(), &Config{DataDir: dir}, atomicFS(files))
	require.NoError(t, err)
	t.Cleanup(func() { _ = svc.Close() })

	return svc
}

func dirNames(t *testing.T, dir string) []string {
	t.Helper()

	entries, err := os.ReadDir(dir)
	require.NoError(t, err)

	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}

	return names
}

func TestFailedMigrationIsNotRecordedAndIsRetried(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := t.Context()
	dir := t.TempDir()

	first := openAtomic(t, dir, map[string]string{"001_v1_0_0": atomicBase})
	r.NoError(first.Initialize(ctx))
	r.NoError(first.Close())

	broken := openAtomic(t, dir, map[string]string{"001_v1_0_0": atomicBase, "002_v1_1_0": atomicSecondBroken})
	err := broken.Initialize(ctx)
	r.Error(err)
	r.Contains(err.Error(), "002", "the error names the migration")
	r.Equal([]string{"001"}, appliedMigrations(t, broken),
		"a migration that failed half way must NOT be recorded as applied")
	r.True(tableExists(t, broken.db.DB, "extras"), "positive control: its first statement did run")
	r.NoError(broken.Close())

	fixed := openAtomic(t, dir, map[string]string{"001_v1_0_0": atomicBase, "002_v1_1_0": atomicSecondFixed})
	r.NoError(fixed.Initialize(ctx), "the fixed migration is retried")
	r.Equal([]string{"001", "002"}, appliedMigrations(t, fixed))

	var rows int

	r.NoError(fixed.db.QueryRowContext(ctx, "select count(*) from things").Scan(&rows))
	r.Equal(1, rows, "its second statement ran on the retry")
}

func TestFailedFirstBootLeavesNoDatabase(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	dir := t.TempDir()

	_, err := newService(t.Context(), &Config{DataDir: dir},
		atomicFS(map[string]string{"001_v1_0_0": atomicBase, "002_v1_1_0": atomicSecondBroken}))
	r.Error(err)
	r.Empty(dirNames(t, dir), "no solidping.db and no temporary file after a failed first start")

	// The same directory then starts fine with a working migration set.
	svc := openAtomic(t, dir, map[string]string{"001_v1_0_0": atomicBase, "002_v1_1_0": atomicSecondFixed})
	r.NoError(svc.Initialize(t.Context()))
	r.Equal([]string{"001", "002"}, appliedMigrations(t, svc))
}

func TestInterruptedFirstBootLeavesNoDatabase(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	dir := t.TempDir()

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	_, err := newService(ctx, &Config{DataDir: dir}, atomicFS(map[string]string{"001_v1_0_0": atomicBase}))
	r.Error(err)
	r.NoFileExists(filepath.Join(dir, "solidping.db"))
	r.Empty(dirNames(t, dir))
}

func TestLeftoverFromKilledFirstBootIsDiscarded(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	dir := t.TempDir()

	// What a process killed mid-migration leaves: a partially built database
	// under the temporary name, plus its WAL sidecars.
	for _, name := range []string{"solidping.db.init", "solidping.db.init-wal", "solidping.db.init-shm"} {
		r.NoError(os.WriteFile(filepath.Join(dir, name), []byte("partial"), 0o600))
	}

	svc := openAtomic(t, dir, map[string]string{"001_v1_0_0": atomicBase})
	r.NoError(svc.Initialize(t.Context()))

	for _, name := range dirNames(t, dir) {
		r.NotContains(name, ".init", "leftover must be removed")
	}

	r.True(tableExists(t, svc.db.DB, "things"))
}

func TestFirstBootProducesOneCleanDatabase(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := t.Context()
	dir := t.TempDir()

	svc, err := New(ctx, Config{DataDir: dir})
	r.NoError(err)

	// The real embedded migrations ran during New; the server's own queries work.
	var orgs int

	r.NoError(svc.db.QueryRowContext(ctx, "select count(*) from organizations").Scan(&orgs))
	r.NoError(svc.Initialize(ctx), "Initialize after the first boot is a no-op that the guard accepts")
	r.NoError(svc.Close())

	for _, name := range dirNames(t, dir) {
		r.NotContains(name, ".init", "no temporary file left")
	}

	r.FileExists(filepath.Join(dir, "solidping.db"))

	// A second start reconciles cleanly in strict mode.
	again, err := New(ctx, Config{DataDir: dir, GuardMode: migrationguard.ModeStrict})
	r.NoError(err)
	t.Cleanup(func() { _ = again.Close() })
	r.NoError(again.Initialize(ctx))
}

func backups(t *testing.T, dir string) []string {
	t.Helper()

	matches, err := filepath.Glob(filepath.Join(dir, "solidping.db.pre-*.bak"))
	require.NoError(t, err)

	return matches
}

func TestBackupBeforePendingMigration(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := t.Context()
	dir := t.TempDir()

	one := map[string]string{"001_v1_0_0": atomicBase}
	two := map[string]string{"001_v1_0_0": atomicBase, "002_v1_1_0": atomicSecondFixed}
	three := map[string]string{"001_v1_0_0": atomicBase, "002_v1_1_0": atomicSecondFixed, "003_v1_2_0": atomicThird}

	svc := openAtomic(t, dir, one)
	r.NoError(svc.Initialize(ctx))
	r.Empty(backups(t, dir), "a brand new database has nothing to back up")
	r.NoError(svc.Close())

	svc = openAtomic(t, dir, one)
	r.NoError(svc.Initialize(ctx))
	r.Empty(backups(t, dir), "nothing pending, no backup")
	r.NoError(svc.Close())

	svc = openAtomic(t, dir, two)
	r.NoError(svc.Initialize(ctx))
	r.NoError(svc.Close())

	found := backups(t, dir)
	r.Equal([]string{filepath.Join(dir, "solidping.db.pre-002.bak")}, found)

	backup, err := sql.Open(sqlitedriver.Name, found[0])
	r.NoError(err)
	t.Cleanup(func() { _ = backup.Close() })
	r.True(tableExists(t, backup, "things"), "the backup opens and holds the schema")
	r.False(tableExists(t, backup, "extras"), "the backup is the pre-migration schema")

	svc = openAtomic(t, dir, three)
	r.NoError(svc.Initialize(ctx))
	r.Equal([]string{filepath.Join(dir, "solidping.db.pre-003.bak")}, backups(t, dir),
		"a second upgrade replaces the previous backup")
	r.Empty(dirNames2(t, dir, ".part"), "no partial backup left")
}

func dirNames2(t *testing.T, dir, contains string) []string {
	t.Helper()

	var out []string

	for _, name := range dirNames(t, dir) {
		if strings.Contains(name, contains) {
			out = append(out, name)
		}
	}

	return out
}

func TestFailedUpgradeErrorNamesTheBackup(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	dir := t.TempDir()

	svc := openAtomic(t, dir, map[string]string{"001_v1_0_0": atomicBase})
	r.NoError(svc.Initialize(t.Context()))
	r.NoError(svc.Close())

	broken := openAtomic(t, dir, map[string]string{"001_v1_0_0": atomicBase, "002_v1_1_0": atomicSecondBroken})
	err := broken.Initialize(t.Context())
	r.Error(err)
	r.Contains(err.Error(), "solidping.db.pre-002.bak")
}
