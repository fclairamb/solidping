package sqlite

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

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
