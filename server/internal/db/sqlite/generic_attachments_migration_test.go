package sqlite

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// downMigrationSection is the `.down.sql` twin of migrationSection: it slices
// one `-- SECTION: <name>` block out of the consolidated v0.17.0 teardown file
// so a test can EXECUTE just that block against an already-migrated database.
//
// One section rather than a whole-group `migrator.Rollback`: unwinding the
// entire release would also replay every other section's teardown, several of
// which are documented as lossy table rebuilds, and a failure anywhere in that
// chain would be reported as a failure of this section. Replaying one block is
// the same idiom migrationSection already established for the up direction.
func downMigrationSection(t *testing.T, name string) string {
	t.Helper()

	body, err := migrationsFS.ReadFile("migrations/014_v0_17_0.down.sql")
	require.NoError(t, err)

	marker := "-- SECTION: " + name + "\n"

	start := strings.Index(string(body), marker)
	require.GreaterOrEqual(t, start, 0, "section %q not found in the consolidated down migration", name)

	section := string(body)[start+len(marker):]
	if end := strings.Index(section, "\n-- SECTION: "); end >= 0 {
		section = section[:end]
	}

	return section
}

// execMigrationStatements runs a migration block statement by statement,
// splitting on a lone `--bun:split` LINE exactly as bun's migrator does. A
// plain substring split would slice the migrations' prose apart mid-sentence
// and hand the remainder to SQLite as SQL.
func execMigrationStatements(ctx context.Context, t *testing.T, svc *Service, block string) {
	t.Helper()

	for _, stmt := range bunSplitRE.Split(block, -1) {
		// The chunk trailing a section is comment-only (the next section's
		// banner); handing that to the driver is an empty-query error, not a
		// no-op, so comment-only chunks are skipped rather than executed.
		if !hasSQL(stmt) {
			continue
		}

		_, err := svc.DB().ExecContext(ctx, stmt)
		require.NoError(t, err, "statement failed:\n%s", stmt)
	}
}

// hasSQL reports whether a `--bun:split` chunk carries anything executable.
func hasSQL(stmt string) bool {
	for _, line := range strings.Split(stmt, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed != "" && !strings.HasPrefix(trimmed, "--") {
			return true
		}
	}

	return false
}

// filesColumns returns the column set of the `files` table.
func filesColumns(ctx context.Context, t *testing.T, svc *Service) map[string]bool {
	t.Helper()

	rows, err := svc.DB().QueryContext(ctx, `select name from pragma_table_info('files')`)
	require.NoError(t, err)

	defer func() { _ = rows.Close() }()

	out := map[string]bool{}

	for rows.Next() {
		var name string

		require.NoError(t, rows.Scan(&name))

		out[name] = true
	}

	require.NoError(t, rows.Err())
	require.NotEmpty(t, out, "pragma_table_info must have enumerated the files table")

	return out
}

// filesTopicIndexCount reports whether the partial attachment index exists.
func filesTopicIndexCount(ctx context.Context, t *testing.T, svc *Service) int {
	t.Helper()

	var out int

	require.NoError(t, svc.DB().QueryRowContext(ctx,
		`select count(*) from sqlite_master where type = 'index' and name = 'files_org_topic_idx'`,
	).Scan(&out))

	return out
}

// TestGenericAttachmentsMigrationParity pins the two halves of the
// generic-attachments section (spec 2026-08-21-01) that a functional test
// cannot reach: that the SECTION exists in both directions, and that the down
// half really unwinds everything the up half created.
//
// A section whose down half forgets one statement is silent until somebody
// migrates down — and by then the failure is a half-migrated schema, which is
// the worst kind. Reading the shipped files rather than restating the SQL is
// what keeps this from drifting.
func TestGenericAttachmentsMigrationParity(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	up := migrationSection(t, "generic-attachments")
	r.Contains(up, "alter table files add column topic")
	r.Contains(up, "alter table files add column details")
	r.Contains(up, "files_org_topic_idx")

	// The index must be PARTIAL: without the predicate it would carry every
	// non-attachment file in the table for no benefit.
	r.Contains(up, "where deleted_at is null and topic is not null")

	down, err := migrationsFS.ReadFile("migrations/014_v0_17_0.down.sql")
	r.NoError(err)

	body := string(down)

	marker := "-- SECTION: generic-attachments\n"
	start := strings.Index(body, marker)
	r.GreaterOrEqual(start, 0, "the down file must carry a generic-attachments section")

	section := body[start+len(marker):]
	if end := strings.Index(section, "\n-- SECTION: "); end >= 0 {
		section = section[:end]
	}

	r.Contains(section, "drop index if exists files_org_topic_idx")
	r.Contains(section, "alter table files drop column details")
	r.Contains(section, "alter table files drop column topic")

	// The down file unwinds in REVERSE order, so this section — last in the up
	// file — must be first here. Getting this wrong is how a down migration
	// ends up dropping a column a later section still needs.
	firstSection := strings.Index(body, "-- SECTION: ")
	r.Equal(start, firstSection,
		"generic-attachments is the last up section, so it must be the first down section")
}

// TestGenericAttachmentsColumnsPresent proves the section actually ran: a fresh
// SQLite database has the columns and the index after Initialize.
func TestGenericAttachmentsColumnsPresent(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := t.Context()

	svc, err := New(ctx, Config{InMemory: true})
	r.NoError(err)
	r.NoError(svc.Initialize(ctx))

	t.Cleanup(func() { _ = svc.Close() })

	columns := map[string]bool{}

	rows, err := svc.DB().QueryContext(ctx, `select name from pragma_table_info('files')`)
	r.NoError(err)

	defer func() { _ = rows.Close() }()

	for rows.Next() {
		var name string
		r.NoError(rows.Scan(&name))
		columns[name] = true
	}

	r.NoError(rows.Err())
	r.True(columns["topic"], "files.topic must exist after migration")
	r.True(columns["details"], "files.details must exist after migration")
	// Positive control: the pragma really enumerated the table.
	r.True(columns["uid"])

	var indexCount int
	r.NoError(svc.DB().QueryRowContext(ctx,
		`select count(*) from sqlite_master where type = 'index' and name = 'files_org_topic_idx'`,
	).Scan(&indexCount))
	r.Equal(1, indexCount)
}

// TestGenericAttachmentsRollback EXECUTES the teardown half against a migrated
// database (spec 2026-08-21-01, "Migration: both dialects apply/rollback").
//
// The parity test above only reads the down file as text, which proves the
// statements were written, not that they run. A `drop column` naming a column
// that does not exist, or a section left in the wrong order, is silent until
// somebody actually migrates down — and by then the symptom is a half-migrated
// schema, the worst kind of failure to debug.
//
// The load-bearing assertion is the LAST one: `files` itself must survive with
// every original column intact. A teardown that took the table with it would
// satisfy "the columns are gone" just as well.
func TestGenericAttachmentsRollback(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := t.Context()

	svc, err := New(ctx, Config{InMemory: true})
	r.NoError(err)
	r.NoError(svc.Initialize(ctx))

	t.Cleanup(func() { _ = svc.Close() })

	// Applied: the columns and the partial index are there.
	before := filesColumns(ctx, t, svc)
	r.True(before["topic"])
	r.True(before["details"])
	r.Equal(1, filesTopicIndexCount(ctx, t, svc))

	// A live attachment row, so the rollback is exercised against a table that
	// actually holds data rather than an empty one.
	topic := "incidents/11111111-1111-1111-1111-111111111111/screenshot"
	_, err = svc.DB().ExecContext(ctx,
		`insert into organizations (uid, slug, name) values (?, 'acme', 'Acme')`,
		"22222222-2222-2222-2222-222222222222")
	r.NoError(err)
	_, err = svc.DB().ExecContext(ctx,
		`insert into files (uid, organization_uid, name, mime_type, size, file_uri, topic, details)
		 values (?, ?, 'shot.png', 'image/png', 42, 'file://blob', ?, '{"region":"eu-west"}')`,
		"33333333-3333-3333-3333-333333333333", "22222222-2222-2222-2222-222222222222", topic)
	r.NoError(err)

	// Rolled back.
	execMigrationStatements(ctx, t, svc, downMigrationSection(t, "generic-attachments"))

	after := filesColumns(ctx, t, svc)
	r.False(after["topic"], "topic must be gone after rollback")
	r.False(after["details"], "details must be gone after rollback")
	r.Zero(filesTopicIndexCount(ctx, t, svc), "the partial index must be gone after rollback")

	// The table, its original columns, and the row itself all survive: this
	// teardown drops the attachment LINK, not the files.
	for _, column := range []string{
		"uid", "organization_uid", "name", "mime_type", "size",
		"file_uri", "sha256", "created_by", "created_at", "deleted_at",
	} {
		r.True(after[column], "files.%s must survive the rollback", column)
	}

	var surviving int
	r.NoError(svc.DB().QueryRowContext(ctx, `select count(*) from files`).Scan(&surviving))
	r.Equal(1, surviving, "the file row itself must survive; only its attachment link is dropped")
}
