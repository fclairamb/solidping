package sqlite

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

// TestFileAttachmentsMigrationShape pins the schema half of spec
// 2026-08-21-01: `files` grows `topic` and `details`, plus the partial index
// the attachment lookups run against.
func TestFileAttachmentsMigrationShape(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	r := require.New(t)

	svc, err := New(ctx, Config{InMemory: true})
	r.NoError(err)
	t.Cleanup(func() { _ = svc.Close() })
	r.NoError(svc.Initialize(ctx))

	type columnInfo struct {
		Name string `bun:"name"`
	}

	var columns []columnInfo
	r.NoError(svc.db.NewRaw("SELECT name FROM pragma_table_info('files')").Scan(ctx, &columns))

	names := make([]string, 0, len(columns))
	for _, c := range columns {
		names = append(names, c.Name)
	}

	// Positive control: this really is the files table and not an empty read.
	r.Contains(names, "file_uri")
	r.Contains(names, "topic")
	r.Contains(names, "details")

	var indexes []string
	r.NoError(svc.db.NewRaw(
		"SELECT name FROM sqlite_master WHERE type = 'index' AND tbl_name = 'files'",
	).Scan(ctx, &indexes))
	r.Contains(indexes, "files_org_topic_idx")
	// The pre-existing index must still be there — this migration adds, it
	// does not replace.
	r.Contains(indexes, "files_org_created_idx")
}

// TestFileAttachmentsExistingCallersUntouched proves the columns are additive:
// a file written the way the pre-existing callers write one (org logos,
// bug-report screenshots — no topic, no details) round-trips unchanged and is
// invisible to every attachment query.
func TestFileAttachmentsExistingCallersUntouched(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	r := require.New(t)

	svc, org := attachmentsFixture(t)

	plain := models.NewFile(org, "logo.png", "image/png", "file://x/org-logos/1", 10, nil)
	r.NoError(svc.CreateFile(ctx, plain))

	stored, err := svc.GetFile(ctx, org, plain.UID)
	r.NoError(err)
	r.Nil(stored.Topic)
	// An absent jsonb column decodes as an empty map, not nil — the same
	// shape incidents.Details has had all along.
	r.Empty(stored.Details)

	// It is a file, but it is not an attachment: no topic can reach it.
	found, err := svc.ListAttachmentsByTopic(ctx, org, "")
	r.NoError(err)
	r.Empty(found)

	// And the ordinary list still sees it — the additive columns did not
	// change what a plain file lookup returns.
	files, total, err := svc.ListFiles(ctx, org, models.ListFilesFilter{})
	r.NoError(err)
	r.Equal(int64(1), total)
	r.Len(files, 1)
}

// TestAttachmentTopicRoundTripAndPrefixDelete covers the three service-level
// behaviors the rail depends on: topic/details survive a write-read cycle,
// an exact-topic list returns newest first, and a prefix delete reaps exactly
// the entity it names — never a neighbor whose uid merely shares a prefix.
func TestAttachmentTopicRoundTripAndPrefixDelete(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	r := require.New(t)

	svc, org := attachmentsFixture(t)

	const (
		incidentA = "aaaaaaaa-0000-0000-0000-000000000001"
		// Deliberately a uid that has incidentA as a STRING PREFIX. A reaper
		// that anchored on `incidents/<uid>` without the trailing separator
		// would delete this one too.
		incidentAPrefixed = "aaaaaaaa-0000-0000-0000-0000000000012"
	)

	topicA := models.AttachmentTopic(models.AttachmentEntityIncidents, incidentA, models.AttachmentKindScreenshot)
	topicNeighbour := models.AttachmentTopic(
		models.AttachmentEntityIncidents, incidentAPrefixed, models.AttachmentKindScreenshot,
	)

	older := models.NewFile(org, "old.png", "image/png", "file://x/screenshots/1", 11, nil)
	older.Topic = &topicA
	older.Details = models.JSONMap{models.AttachmentDetailRegion: "eu-west"}
	older.CreatedAt = time.Now().Add(-time.Hour)
	r.NoError(svc.CreateFile(ctx, older))

	newer := models.NewFile(org, "new.png", "image/png", "file://x/screenshots/2", 22, nil)
	newer.Topic = &topicA
	newer.Details = models.JSONMap{models.AttachmentDetailTrigger: models.AttachmentTriggerIncidentReopen}
	r.NoError(svc.CreateFile(ctx, newer))

	neighbor := models.NewFile(org, "other.png", "image/png", "file://x/screenshots/3", 33, nil)
	neighbor.Topic = &topicNeighbour
	r.NoError(svc.CreateFile(ctx, neighbor))

	listed, err := svc.ListAttachmentsByTopic(ctx, org, topicA)
	r.NoError(err)
	r.Len(listed, 2)
	r.Equal(newer.UID, listed[0].UID, "newest first")
	r.Equal(older.UID, listed[1].UID)

	// details round-trips as a map, not as an opaque blob.
	r.Equal("eu-west", listed[1].Details[models.AttachmentDetailRegion])

	deleted, err := svc.DeleteAttachmentsByTopicPrefix(
		ctx, org, models.AttachmentTopicPrefix(models.AttachmentEntityIncidents, incidentA),
	)
	r.NoError(err)
	r.Equal(int64(2), deleted)

	gone, err := svc.ListAttachmentsByTopic(ctx, org, topicA)
	r.NoError(err)
	r.Empty(gone)

	// The prefix-sharing neighbor survived: the trailing '/' in the prefix is
	// load-bearing, not cosmetic.
	survivor, err := svc.ListAttachmentsByTopic(ctx, org, topicNeighbour)
	r.NoError(err)
	r.Len(survivor, 1)
}

// TestAttachmentPrefixDeleteIsOrgScoped proves one tenant's reap cannot reach
// another's attachments, even under a byte-identical topic.
func TestAttachmentPrefixDeleteIsOrgScoped(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	r := require.New(t)

	svc, orgA := attachmentsFixture(t)

	orgB := "org-b"
	_, err := svc.DB().ExecContext(ctx,
		`insert into organizations (uid, slug, name) values (?, 'other', 'Other')`, orgB)
	r.NoError(err)

	const incidentUID = "bbbbbbbb-0000-0000-0000-000000000001"
	topic := models.AttachmentTopic(models.AttachmentEntityIncidents, incidentUID, models.AttachmentKindScreenshot)

	for _, org := range []string{orgA, orgB} {
		file := models.NewFile(org, "shot.png", "image/png", "file://x/screenshots/"+org, 10, nil)
		file.Topic = &topic
		r.NoError(svc.CreateFile(ctx, file))
	}

	deleted, err := svc.DeleteAttachmentsByTopicPrefix(
		ctx, orgA, models.AttachmentTopicPrefix(models.AttachmentEntityIncidents, incidentUID),
	)
	r.NoError(err)
	r.Equal(int64(1), deleted, "only the reaping org's row")

	remaining, err := svc.ListAttachmentsByTopic(ctx, orgB, topic)
	r.NoError(err)
	r.Len(remaining, 1, "the other tenant's attachment is untouched")
}

// TestListOrphanIncidentAttachments covers the GC sweep's query: an
// attachment whose incident is gone (or soft-deleted) is a candidate; one
// whose incident is alive is not, and neither is a non-attachment file.
func TestListOrphanIncidentAttachments(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	r := require.New(t)

	svc, org := attachmentsFixture(t)

	check := models.NewCheck(org, "api", "browser")
	r.NoError(svc.CreateCheck(ctx, check))

	live := models.NewIncident(org, check.UID, time.Now(), "live")
	r.NoError(svc.CreateIncident(ctx, live))

	soft := models.NewIncident(org, check.UID, time.Now(), "soft-deleted")
	r.NoError(svc.CreateIncident(ctx, soft))
	_, err := svc.DB().ExecContext(ctx,
		`update incidents set deleted_at = datetime('now') where uid = ?`, soft.UID)
	r.NoError(err)

	attach := func(name, incidentUID string) *models.File {
		topic := models.AttachmentTopic(models.AttachmentEntityIncidents, incidentUID, models.AttachmentKindScreenshot)
		file := models.NewFile(org, name, "image/png", "file://x/screenshots/"+name, 10, nil)
		file.Topic = &topic
		r.NoError(svc.CreateFile(ctx, file))

		return file
	}

	keptLive := attach("live.png", live.UID)
	orphanSoft := attach("soft.png", soft.UID)
	orphanGone := attach("gone.png", "cccccccc-0000-0000-0000-000000000009")

	// A plain non-attachment file must never be a GC candidate.
	plain := models.NewFile(org, "logo.png", "image/png", "file://x/org-logos/1", 10, nil)
	r.NoError(svc.CreateFile(ctx, plain))

	orphans, err := svc.ListOrphanIncidentAttachments(ctx, 100)
	r.NoError(err)

	uids := make(map[string]bool, len(orphans))
	for _, file := range orphans {
		uids[file.UID] = true
	}

	r.True(uids[orphanSoft.UID], "an attachment of a soft-deleted incident is an orphan")
	r.True(uids[orphanGone.UID], "an attachment of a nonexistent incident is an orphan")
	r.False(uids[keptLive.UID], "an attachment of a live incident is not an orphan")
	r.False(uids[plain.UID], "a non-attachment file is never an orphan")
}

// TestPurgeableDeletedFilesAndPurge covers the second GC pass: only files
// soft-deleted before the cutoff are offered, and PurgeFile removes the row
// for good.
func TestPurgeableDeletedFilesAndPurge(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	r := require.New(t)

	svc, org := attachmentsFixture(t)

	old := models.NewFile(org, "old.png", "image/png", "file://x/screenshots/old", 10, nil)
	r.NoError(svc.CreateFile(ctx, old))
	r.NoError(svc.DeleteFile(ctx, org, old.UID))
	_, err := svc.DB().ExecContext(ctx,
		`update files set deleted_at = datetime('now', '-30 days') where uid = ?`, old.UID)
	r.NoError(err)

	recent := models.NewFile(org, "recent.png", "image/png", "file://x/screenshots/recent", 10, nil)
	r.NoError(svc.CreateFile(ctx, recent))
	r.NoError(svc.DeleteFile(ctx, org, recent.UID))

	alive := models.NewFile(org, "alive.png", "image/png", "file://x/screenshots/alive", 10, nil)
	r.NoError(svc.CreateFile(ctx, alive))

	purgeable, err := svc.ListPurgeableDeletedFiles(ctx, time.Now().Add(-7*24*time.Hour), 100)
	r.NoError(err)
	r.Len(purgeable, 1)
	r.Equal(old.UID, purgeable[0].UID)

	r.NoError(svc.PurgeFile(ctx, old.UID))

	after, err := svc.ListPurgeableDeletedFiles(ctx, time.Now().Add(-7*24*time.Hour), 100)
	r.NoError(err)
	r.Empty(after, "a purged row is gone for good")
}

// attachmentsFixture builds an in-memory database with one organization and
// returns its UID.
func attachmentsFixture(t *testing.T) (*Service, string) {
	t.Helper()

	ctx := t.Context()
	r := require.New(t)

	svc, err := New(ctx, Config{InMemory: true})
	r.NoError(err)
	t.Cleanup(func() { _ = svc.Close() })
	r.NoError(svc.Initialize(ctx))

	const orgUID = "org-1"
	_, err = svc.DB().ExecContext(ctx,
		`insert into organizations (uid, slug, name) values (?, 'acme', 'Acme')`, orgUID)
	r.NoError(err)

	return svc, orgUID
}
