package postgres

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

// portGenericAttachments is distinct from every other _postgres_test.go
// embedded port in the repo (see the port-numbering note in
// postgres_headroom_postgres_test.go).
const portGenericAttachments = 15484

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

		file.Details = models.JSONMap{"region": "eu-west", "trigger": "incident-open"}
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
	r.Equal("eu-west", stored.Details["region"])

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
