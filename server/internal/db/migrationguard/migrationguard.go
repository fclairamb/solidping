// Package migrationguard detects migrations whose content changed after they
// were applied.
//
// bun keys applied migrations on the numeric filename prefix alone. Editing an
// already-applied migration file — renumbering it, or rewriting it in place
// while a dev loop has already run the earlier draft — therefore does NOT
// re-run it: the new statements never execute, `bun_migrations` still claims
// the migration is applied, and the database silently diverges from the schema
// the code was written against. That has now bitten this repository twice
// (spec 2026-08-18-02 and the earlier migration-consolidation desync), each
// time surfacing hours later as per-query WARNs and half-working features
// rather than as a startup failure.
//
// The guard closes that gap: it records a content checksum for every applied
// migration in a side table and compares it on every boot. A mismatch fails
// startup with a message naming the migration and the repair options, so the
// drift is impossible to miss.
//
// Checksums cover the `.up.sql` half only. A `.down.sql` file never runs during
// a forward boot, so editing one cannot desync an applied schema, and treating
// it as part of the identity would trip the guard on harmless teardown edits.
package migrationguard

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"path"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/uptrace/bun"
)

// DefaultTable is bun's migrations table, which the guard reads to learn which
// migrations a database claims to have applied.
const DefaultTable = "bun_migrations"

// ChecksumTable is the side table the guard owns. It is intentionally NOT a
// column on `bun_migrations`: that table is bun's, created from bun's own
// model, and adding a column to it would be silently dropped the next time bun
// changes the model.
const ChecksumTable = "migration_checksums"

// fnameRE mirrors bun's own migration filename regexp: the numeric prefix is
// the identity, the rest is an informational comment.
var fnameRE = regexp.MustCompile(`^(\d{1,14})_([0-9a-z_\-]+)\.up\.sql$`)

// ErrChecksumMismatch is the sentinel every content-drift failure wraps, so
// callers can distinguish "this database diverged" from an I/O error.
var ErrChecksumMismatch = errors.New("migration content changed after it was applied")

// MismatchError reports one applied migration whose file content no longer
// matches what was recorded when it was applied.
type MismatchError struct {
	// Name is the numeric prefix bun keys on (e.g. "013").
	Name string
	// Comment is the informational half of the filename (e.g. "v0_16_0"),
	// empty for a migration that no longer names itself.
	Comment string
	// Recorded is the checksum stored when the migration was applied.
	Recorded string
	// Current is the checksum of the file as it exists now.
	Current string
}

func (e *MismatchError) Error() string {
	label := e.Name
	if e.Comment != "" {
		label = e.Name + "_" + e.Comment
	}

	return fmt.Sprintf(
		"migration %s %s (recorded %s, current %s). "+
			"bun keys applied migrations on the numeric prefix alone, so the edited statements "+
			"never ran and this database is missing whatever they would have created. "+
			"Repair options: (1) development — reset the database "+
			"(SP_DB_RESET=true in test/demo run mode, or delete the SQLite file); "+
			"(2) otherwise — apply the missing statements by hand, then reconcile with "+
			"`UPDATE %s SET checksum = '%s' WHERE name = '%s';`. "+
			"Never edit a migration that has already been applied: add a new one instead",
		label, ErrChecksumMismatch, short(e.Recorded), short(e.Current),
		ChecksumTable, e.Current, e.Name,
	)
}

func (e *MismatchError) Unwrap() error { return ErrChecksumMismatch }

func short(sum string) string {
	if len(sum) <= 12 {
		return sum
	}

	return sum[:12]
}

// checksumRow is the side table's model.
type checksumRow struct {
	bun.BaseModel `bun:"table:migration_checksums"`

	Name       string    `bun:"name,pk"`
	Comment    string    `bun:"comment"`
	Checksum   string    `bun:"checksum,notnull"`
	RecordedAt time.Time `bun:"recorded_at,nullzero,notnull,default:current_timestamp"`
}

// Migration is one migration's identity plus the checksum of its content.
type Migration struct {
	Name     string
	Comment  string
	Checksum string
}

// Checksums walks dir inside fsys and returns one entry per `NNN_name.up.sql`
// file, keyed by the numeric prefix.
func Checksums(fsys fs.FS, dir string) (map[string]Migration, error) {
	entries, err := fs.ReadDir(fsys, dir)
	if err != nil {
		return nil, fmt.Errorf("read migrations dir %q: %w", dir, err)
	}

	out := make(map[string]Migration, len(entries))

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}

		matches := fnameRE.FindStringSubmatch(entry.Name())
		if matches == nil {
			continue
		}

		body, readErr := fs.ReadFile(fsys, path.Join(dir, entry.Name()))
		if readErr != nil {
			return nil, fmt.Errorf("read migration %q: %w", entry.Name(), readErr)
		}

		sum := sha256.Sum256(body)
		out[matches[1]] = Migration{
			Name:     matches[1],
			Comment:  matches[2],
			Checksum: hex.EncodeToString(sum[:]),
		}
	}

	return out, nil
}

// Guard verifies applied migrations against the checksums of the migration
// sources this binary carries.
type Guard struct {
	db       *bun.DB
	expected map[string]Migration
	table    string
}

// New builds a guard over the given expected migrations. Additional entries
// (Go migrations, which have no file to hash) are merged in on top of the
// file-derived set.
func New(db *bun.DB, expected map[string]Migration, extra ...Migration) *Guard {
	merged := make(map[string]Migration, len(expected)+len(extra))
	for k := range expected {
		merged[k] = expected[k]
	}

	for i := range extra {
		merged[extra[i].Name] = extra[i]
	}

	return &Guard{db: db, expected: merged, table: DefaultTable}
}

// Reconcile creates the checksum table if needed, then for every migration the
// database claims to have applied either records its checksum (first boot on a
// database that predates the guard) or compares it and fails on drift.
//
// It is safe — and intended — to call this both before and after running
// migrations: the call before verifies (and backfills) what was already
// applied, the call after records what was just applied.
func (g *Guard) Reconcile(ctx context.Context) error {
	if err := g.ensureTable(ctx); err != nil {
		return err
	}

	applied, err := g.appliedNames(ctx)
	if err != nil {
		return err
	}

	recorded, err := g.recordedChecksums(ctx)
	if err != nil {
		return err
	}

	var (
		mismatches []*MismatchError
		backfilled []checksumRow
	)

	for _, name := range applied {
		want, known := g.expected[name]
		if !known {
			// A migration this binary no longer ships (rolled back release,
			// or a database from a newer build). Nothing to compare against;
			// bun already tolerates it, so the guard does too.
			continue
		}

		got, seen := recorded[name]
		if !seen {
			backfilled = append(backfilled, checksumRow{
				Name: want.Name, Comment: want.Comment, Checksum: want.Checksum,
			})

			continue
		}

		if got != want.Checksum {
			mismatches = append(mismatches, &MismatchError{
				Name: want.Name, Comment: want.Comment,
				Recorded: got, Current: want.Checksum,
			})
		}
	}

	// Report drift before writing anything: a database that is already
	// diverged must not have its record quietly updated.
	if len(mismatches) > 0 {
		return joinMismatches(mismatches)
	}

	if len(backfilled) == 0 {
		return nil
	}

	if _, err := g.db.NewInsert().Model(&backfilled).Exec(ctx); err != nil {
		return fmt.Errorf("record migration checksums: %w", err)
	}

	slog.DebugContext(ctx, "Recorded migration checksums", "count", len(backfilled))

	return nil
}

func joinMismatches(mismatches []*MismatchError) error {
	if len(mismatches) == 1 {
		return mismatches[0]
	}

	errs := make([]error, 0, len(mismatches))
	for _, m := range mismatches {
		errs = append(errs, m)
	}

	return errors.Join(errs...)
}

func (g *Guard) ensureTable(ctx context.Context) error {
	_, err := g.db.NewCreateTable().
		Model((*checksumRow)(nil)).
		IfNotExists().
		Exec(ctx)
	if err != nil {
		return fmt.Errorf("create %s table: %w", ChecksumTable, err)
	}

	return nil
}

// appliedNames returns the sorted set of migration names bun records as
// applied. A database that has never run a migration simply yields none.
func (g *Guard) appliedNames(ctx context.Context) ([]string, error) {
	var names []string

	err := g.db.NewSelect().
		ColumnExpr("name").
		TableExpr(g.table).
		Scan(ctx, &names)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}

		return nil, fmt.Errorf("read %s: %w", g.table, err)
	}

	// Deduplicate: bun's table has no unique index on name by default, and a
	// hand-reconciled database can carry a duplicate row.
	seen := make(map[string]struct{}, len(names))
	out := make([]string, 0, len(names))

	for _, n := range names {
		n = strings.TrimSpace(n)
		if n == "" {
			continue
		}

		if _, dup := seen[n]; dup {
			continue
		}

		seen[n] = struct{}{}

		out = append(out, n)
	}

	sort.Strings(out)

	return out, nil
}

func (g *Guard) recordedChecksums(ctx context.Context) (map[string]string, error) {
	var rows []checksumRow

	if err := g.db.NewSelect().Model(&rows).Scan(ctx); err != nil {
		return nil, fmt.Errorf("read %s: %w", ChecksumTable, err)
	}

	out := make(map[string]string, len(rows))
	for i := range rows {
		out[rows[i].Name] = rows[i].Checksum
	}

	return out, nil
}
