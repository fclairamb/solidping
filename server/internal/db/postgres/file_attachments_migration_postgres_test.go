package postgres

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

// portFileAttachments is distinct from every other _postgres_test.go embedded
// port in the repo (see the port-numbering note in
// postgres_headroom_postgres_test.go).
const portFileAttachments = 15484

// TestFileAttachments_Postgres is the PostgreSQL twin of the SQLite
// attachments test. Worth running on both dialects even though the intent is
// identical, because the two implementations are NOT the same SQL: the prefix
// reaper here is `LIKE ... ESCAPE '\'` while SQLite uses a byte-exact key
// range (its LIKE is ASCII-case-insensitive), and the orphan query casts
// `incidents.uid` from uuid to text. Each has to be proven separately.
//
//nolint:paralleltest // shares dev-machine embedded-postgres resources with its siblings
func TestFileAttachments_Postgres(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping embedded-postgres test in -short mode")
	}

	ctx := t.Context()
	r := require.New(t)

	svc, err := New(ctx, &Config{Embedded: true, Port: portFileAttachments, RunMode: runModeTest})
	if err != nil {
		t.Skipf("embedded postgres unavailable: %v", err)
	}

	t.Cleanup(func() { _ = svc.Close() })

	if initErr := svc.Initialize(ctx); initErr != nil {
		t.Skipf("embedded postgres init failed: %v", initErr)
	}

	org := models.NewOrganization("acme-attach", "Acme")
	r.NoError(svc.CreateOrganization(ctx, org))

	check := models.NewCheck(org.UID, "shop", "browser")
	r.NoError(svc.CreateCheck(ctx, check))

	live := models.NewIncident(org.UID, check.UID, time.Now(), "live")
	r.NoError(svc.CreateIncident(ctx, live))

	topicFor := func(incidentUID string) string {
		return models.AttachmentTopic(
			models.AttachmentEntityIncidents, incidentUID, models.AttachmentKindScreenshot,
		)
	}

	attach := func(name, topic string) *models.File {
		file := models.NewFile(org.UID, name, "image/png", "file://x/screenshots/"+name, 6, nil)
		if topic != "" {
			file.Topic = &topic
		}

		file.Details = models.JSONMap{models.AttachmentDetailRegion: "eu-west"}
		r.NoError(svc.CreateFile(ctx, file))

		return file
	}

	liveTopic := topicFor(live.UID)
	kept := attach("live.png", liveTopic)
	orphan := attach("orphan.png", topicFor("11111111-2222-3333-4444-555555555555"))
	plain := attach("logo.png", "")

	// topic + details round-trip, and the exact-topic list finds the row.
	listed, err := svc.ListAttachmentsByTopic(ctx, org.UID, liveTopic)
	r.NoError(err)
	r.Len(listed, 1)
	r.Equal(kept.UID, listed[0].UID)
	r.Equal("eu-west", listed[0].Details[models.AttachmentDetailRegion])

	// The orphan query is the GC sweep's: an attachment of a nonexistent
	// incident is a candidate, a live one and a plain file are not.
	orphans, err := svc.ListOrphanIncidentAttachments(ctx, 100)
	r.NoError(err)

	uids := make(map[string]bool, len(orphans))
	for _, file := range orphans {
		uids[file.UID] = true
	}

	r.True(uids[orphan.UID])
	r.False(uids[kept.UID])
	r.False(uids[plain.UID])

	// The prefix reaper deletes the named incident's attachments and nothing
	// else — the trailing '/' is what keeps a uid-prefix neighbor safe.
	deleted, err := svc.DeleteAttachmentsByTopicPrefix(
		ctx, org.UID, models.AttachmentTopicPrefix(models.AttachmentEntityIncidents, live.UID),
	)
	r.NoError(err)
	r.Equal(int64(1), deleted)

	gone, err := svc.ListAttachmentsByTopic(ctx, org.UID, liveTopic)
	r.NoError(err)
	r.Empty(gone)

	stillThere, err := svc.GetFile(ctx, org.UID, plain.UID)
	r.NoError(err)
	r.NotNil(stillThere, "a non-attachment file is never reaped by a topic prefix")

	// A LIKE metacharacter in a prefix must be escaped, not interpreted: a
	// wildcard prefix must match nothing rather than everything.
	wild, err := svc.DeleteAttachmentsByTopicPrefix(ctx, org.UID, "incidents/%/")
	r.NoError(err)
	r.Zero(wild, "'%' is a literal in a topic prefix, never a wildcard")

	// Positive control for the line above: the orphan is still live, so the
	// wildcard really did match nothing rather than there being nothing left.
	remaining, err := svc.GetFile(ctx, org.UID, orphan.UID)
	r.NoError(err)
	r.NotNil(remaining)
}
