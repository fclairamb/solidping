package postgres

import (
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

// portGenericAttachments is distinct from every other _postgres_test.go
// embedded port in the repo (see the port-numbering note in
// postgres_headroom_postgres_test.go).
const portGenericAttachments = 15484

// portGenericAttachmentsRollback is its sibling for the teardown test, which
// destroys schema and therefore must not share a database with the test above.
const portGenericAttachmentsRollback = 15485

// detailRegionKey is the details-bag key these fixtures write. Named rather
// than repeated so goconst stays quiet about a three-times literal.
const detailRegionKey = "region"

// TestGenericAttachmentsMigration_Postgres proves the generic-attachments
// section of the consolidated v0.17.0 migration (spec 2026-08-21-01) really
// lands on Postgres, and that the two queries the whole feature stands on
// behave on this dialect.
//
// Worth a dedicated Postgres test even though the SQLite side is covered by
// the in-memory service tests: the two migrations are NOT the same SQL
// (`jsonb` vs `text`, `ADD COLUMN IF NOT EXISTS` vs plain `ADD COLUMN`) and the
// prefix reap goes through `LIKE ... ESCAPE '\'`, whose escaping rules are
// exactly the sort of thing that works on one engine and not the other.
//
//nolint:paralleltest // shares dev-machine embedded-postgres resources with its siblings
func TestGenericAttachmentsMigration_Postgres(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping embedded-postgres test in -short mode")
	}

	ctx := t.Context()
	r := require.New(t)

	svc, err := New(ctx, &Config{Embedded: true, Port: portGenericAttachments, RunMode: runModeTest})
	if err != nil {
		t.Skipf("embedded postgres unavailable: %v", err)
	}

	t.Cleanup(func() { _ = svc.Close() })

	if initErr := svc.Initialize(ctx); initErr != nil {
		t.Skipf("embedded postgres init failed: %v", initErr)
	}

	count := func(query string, args ...any) int {
		t.Helper()

		var out int
		r.NoError(svc.DB().QueryRowContext(ctx, query, args...).Scan(&out))

		return out
	}

	// The columns exist, and `details` really is jsonb — a text column would
	// still round-trip through models.JSONMap and hide the mistake until
	// somebody tried to index into it.
	r.Equal(1, count(`select count(*) from information_schema.columns
	                  where table_name = 'files' and column_name = 'topic' and data_type = 'text'`))
	r.Equal(1, count(`select count(*) from information_schema.columns
	                  where table_name = 'files' and column_name = 'details' and data_type = 'jsonb'`))

	// The index is PARTIAL: a plain index over a table where almost every row
	// has a NULL topic would be mostly dead weight, so its predicate is part of
	// the contract, not an implementation detail.
	var indexDef string
	r.NoError(svc.DB().QueryRowContext(ctx,
		`select indexdef from pg_indexes where indexname = 'files_org_topic_idx'`).Scan(&indexDef))
	r.Contains(indexDef, "organization_uid")
	r.Contains(indexDef, "topic")
	r.Contains(indexDef, "WHERE")

	org := "11111111-1111-1111-1111-111111111111"
	_, err = svc.DB().ExecContext(ctx,
		`insert into organizations (uid, slug, name) values (?, 'acme', 'Acme')`, org)
	r.NoError(err)

	write := func(topic string) *models.File {
		t.Helper()

		file := models.NewFile(org, "shot.png", "image/png", "file://blob", 42, nil)
		if topic != "" {
			file.Topic = &topic
		}

		file.Details = models.JSONMap{detailRegionKey: "eu-west", "trigger": "incident-open"}
		r.NoError(svc.CreateFile(ctx, file))

		return file
	}

	// Deliberately overlapping uids: "abc" is a strict prefix of "abcdef".
	shortAttachment := write("incidents/abc/screenshot")
	longAttachment := write("incidents/abcdef/screenshot")
	plain := write("")

	// The details bag survives the jsonb round trip.
	stored, err := svc.GetFile(ctx, org, shortAttachment.UID)
	r.NoError(err)
	r.Equal("eu-west", stored.Details[detailRegionKey])

	// Exact-topic list.
	rows, _, err := svc.ListFiles(ctx, org, models.ListFilesFilter{Topic: "incidents/abc/screenshot"})
	r.NoError(err)
	r.Len(rows, 1)
	r.Equal(shortAttachment.UID, rows[0].UID)

	// Prefix reap, and the two negatives that make it safe.
	reaped, err := svc.DeleteFilesByTopicPrefix(ctx, org, "incidents/abc/")
	r.NoError(err)
	r.Equal(1, reaped)

	_, err = svc.GetFile(ctx, org, longAttachment.UID)
	r.NoError(err, "the trailing slash must stop abc/ from matching abcdef/")
	_, err = svc.GetFile(ctx, org, plain.UID)
	r.NoError(err, "a file with no topic is invisible to the reaper")
	_, err = svc.GetFile(ctx, org, shortAttachment.UID)
	r.Error(err, "the reaped attachment is soft-deleted")

	// A LIKE wildcard smuggled into a prefix must be treated as a literal — an
	// unescaped `%` would turn a one-entity reap into a table-wide one.
	wildcardReaped, err := svc.DeleteFilesByTopicPrefix(ctx, org, "incidents/%/")
	r.NoError(err)
	r.Zero(wildcardReaped, "`%` in a prefix is a literal, never a wildcard")

	_, err = svc.GetFile(ctx, org, longAttachment.UID)
	r.NoError(err, "the wildcard reap must not have taken anything")

	// The GC sweep's query: attachments only, older than the cutoff only.
	swept, err := svc.ListAttachmentsByTopicPrefix(ctx, "incidents/", time.Now().Add(time.Hour), 100)
	r.NoError(err)
	r.Len(swept, 1)
	r.Equal(longAttachment.UID, swept[0].UID)

	fresh, err := svc.ListAttachmentsByTopicPrefix(ctx, "incidents/", time.Now().Add(-time.Hour), 100)
	r.NoError(err)
	r.Empty(fresh, "rows newer than the cutoff are outside the sweep")
}

// downMigrationSection is the `.down.sql` twin of migrationSection: it slices
// one `-- SECTION: <name>` block out of the teardown file that carries it, so
// a test can EXECUTE just that block against an already-migrated database.
//
// One section rather than a whole-group `migrator.Rollback`: unwinding an
// entire release would also replay every other section's teardown, several of
// which are documented as lossy table rebuilds, and a failure anywhere in that
// chain would be reported as a failure of this section. Replaying one block is
// the same idiom migrationSection already established for the up direction.
func downMigrationSection(t *testing.T, name string) string {
	t.Helper()

	return findMigrationSection(t, "down", name)
}

// pgBunSplitRE matches bun's statement separator: a line containing nothing but
// `--bun:split`. The migrations mention the marker inside ordinary prose, so a
// substring split would slice a comment apart and feed the remainder to
// Postgres as SQL.
var pgBunSplitRE = regexp.MustCompile(`(?m)^[ \t]*--bun:split[ \t]*$`)

// hasSQL reports whether a `--bun:split` chunk carries anything executable.
// The chunk that trails a section is comment-only (the next section's banner),
// and handing that to the driver is an empty-query error rather than a no-op.
func hasSQL(stmt string) bool {
	for _, line := range strings.Split(stmt, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed != "" && !strings.HasPrefix(trimmed, "--") {
			return true
		}
	}

	return false
}

// TestGenericAttachmentsRollback_Postgres EXECUTES the teardown half against a
// migrated database (spec 2026-08-21-01, "Migration: both dialects
// apply/rollback").
//
// The load-bearing assertion is the last one: `files` itself must survive with
// every original column and its rows intact. A teardown that took the table
// with it would satisfy "the columns are gone" just as well.
//
//nolint:paralleltest // shares dev-machine embedded-postgres resources with its siblings
func TestGenericAttachmentsRollback_Postgres(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping embedded-postgres test in -short mode")
	}

	ctx := t.Context()
	r := require.New(t)

	svc, err := New(ctx, &Config{Embedded: true, Port: portGenericAttachmentsRollback, RunMode: runModeTest})
	if err != nil {
		t.Skipf("embedded postgres unavailable: %v", err)
	}

	t.Cleanup(func() { _ = svc.Close() })

	if initErr := svc.Initialize(ctx); initErr != nil {
		t.Skipf("embedded postgres init failed: %v", initErr)
	}

	count := func(query string, args ...any) int {
		t.Helper()

		var out int
		r.NoError(svc.DB().QueryRowContext(ctx, query, args...).Scan(&out))

		return out
	}

	filesColumn := func(name string) int {
		t.Helper()

		return count(`select count(*) from information_schema.columns
		              where table_name = 'files' and column_name = ?`, name)
	}

	// Applied.
	r.Equal(1, filesColumn("topic"))
	r.Equal(1, filesColumn("details"))
	r.Equal(1, count(`select count(*) from pg_indexes where indexname = 'files_org_topic_idx'`))

	// A live attachment row, so the rollback runs against a populated table.
	org := "44444444-4444-4444-4444-444444444444"
	_, err = svc.DB().ExecContext(ctx,
		`insert into organizations (uid, slug, name) values (?, 'acmeroll', 'Acme Rollback')`, org)
	r.NoError(err)

	file := models.NewFile(org, "shot.png", "image/png", "file://blob", 42, nil)
	topic := "incidents/55555555-5555-5555-5555-555555555555/screenshot"
	file.Topic = &topic
	file.Details = models.JSONMap{detailRegionKey: "eu-west"}
	r.NoError(svc.CreateFile(ctx, file))

	// Rolled back, statement by statement, exactly as bun would run it.
	for _, stmt := range pgBunSplitRE.Split(downMigrationSection(t, "generic-attachments"), -1) {
		if !hasSQL(stmt) {
			continue
		}

		_, execErr := svc.DB().ExecContext(ctx, stmt)
		r.NoError(execErr, "statement failed:\n%s", stmt)
	}

	r.Zero(filesColumn("topic"), "topic must be gone after rollback")
	r.Zero(filesColumn("details"), "details must be gone after rollback")
	r.Zero(count(`select count(*) from pg_indexes where indexname = 'files_org_topic_idx'`),
		"the partial index must be gone after rollback")

	// The table, its original columns, and the row all survive: this teardown
	// drops the attachment LINK, not the files.
	for _, column := range []string{
		"uid", "organization_uid", "name", "mime_type", "size",
		"file_uri", "sha256", "created_by", "created_at", "deleted_at",
	} {
		r.Equal(1, filesColumn(column), "files.%s must survive the rollback", column)
	}

	r.Equal(1, count(`select count(*) from files where uid = ?`, file.UID),
		"the file row itself must survive; only its attachment link is dropped")
}
