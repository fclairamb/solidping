package files

import (
	"io"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/handlers/filestorage"
	"github.com/fclairamb/solidping/server/internal/handlers/filestorage/localfs"
)

// newAttachmentsFixture builds a files service over an in-memory database and
// a temp-dir storage backend, plus the attachment-store adapter on top.
func newAttachmentsFixture(t *testing.T) (*Service, *AttachmentStore, uuid.UUID) {
	t.Helper()

	ctx := t.Context()
	r := require.New(t)

	localfs.Register()

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	t.Cleanup(func() { _ = dbSvc.Close() })
	r.NoError(dbSvc.Initialize(ctx))

	org := models.NewOrganization("acme", "Acme")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	cfg := &config.Config{}
	cfg.FileStorage.Type = "local"
	cfg.FileStorage.LocalRoot = t.TempDir()
	cfg.Auth.JWTSecret = "test-secret-for-signed-urls"
	cfg.Server.BaseURL = "https://fallback.acme.com"

	svc := NewService(dbSvc, cfg)
	store := NewAttachmentStore(svc, filestorage.GroupTypeScreenshots)

	orgUID, err := uuid.Parse(org.UID)
	r.NoError(err)

	return svc, store, orgUID
}

// TestCreateFileWithoutOptionsIsUnchanged is the additivity guarantee: the two
// pre-existing callers (org logos, bug reports) pass no options, and what they
// get back must look exactly as it did before the attachments rail existed.
func TestCreateFileWithoutOptionsIsUnchanged(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	svc, _, orgUID := newAttachmentsFixture(t)

	file, err := svc.CreateFile(
		t.Context(), orgUID, filestorage.GroupTypeOrgLogos,
		"logo.png", mimeTypePNG, nil, strings.NewReader("logo"), 4,
	)
	r.NoError(err)
	r.Nil(file.Topic)
	// Empty rather than nil: bun reads the row back after the insert and an
	// absent jsonb column decodes as an empty map. `omitempty` drops it from
	// the JSON either way, which is what the response-shape test below pins.
	r.Empty(file.Details)

	// And it is invisible to every attachment query.
	found, err := svc.ListAttachments(t.Context(), orgUID.String(), "anything/x/y")
	r.NoError(err)
	r.Empty(found)
}

// TestAttachmentRoundTripThroughTheStore drives the adapter the incident
// pipeline actually uses: write bytes under a topic, read them back as a view
// with a signed URL, and confirm the bytes are really in storage.
func TestAttachmentRoundTripThroughTheStore(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	svc, store, orgUID := newAttachmentsFixture(t)

	const incidentUID = "9a1eb273-0a95-4d6b-b967-9af076c1f8e8"

	topic := models.AttachmentTopic(
		models.AttachmentEntityIncidents, incidentUID, models.AttachmentKindScreenshot,
	)
	details := models.JSONMap{
		models.AttachmentDetailRegion:  "eu-west",
		models.AttachmentDetailTrigger: models.AttachmentTriggerIncidentOpen,
	}

	fileUID, err := store.CreateAttachment(
		t.Context(), orgUID, "shop-screenshot.png", mimeTypePNG, topic, details, []byte("pixels"),
	)
	r.NoError(err)
	r.NotEmpty(fileUID)

	views, err := store.ListAttachmentViews(t.Context(), orgUID.String(), topic, "https://acme.example")
	r.NoError(err)
	r.Len(views, 1)

	view := views[0]
	r.Equal(fileUID, view.UID)
	r.Equal("shop-screenshot.png", view.Name)
	// Kind is derived from the topic, so no caller has to parse one.
	r.Equal(models.AttachmentKindScreenshot, view.Kind)
	r.Equal(mimeTypePNG, view.MimeType)
	r.EqualValues(6, view.Size)
	r.Equal("eu-west", view.Details[models.AttachmentDetailRegion])

	// The URL is signed, rooted at the CALLER's host (not the config
	// fallback), and points at the public signed-file route.
	r.Contains(view.DownloadURL, "https://acme.example/pub/files/"+fileUID)
	r.Contains(view.DownloadURL, "sig=")
	r.Contains(view.DownloadURL, "exp=")

	// Positive control that the bytes are genuinely in storage.
	stored, err := svc.GetFileByUID(t.Context(), fileUID)
	r.NoError(err)

	body, err := svc.OpenContent(t.Context(), stored)
	r.NoError(err)

	defer func() { _ = body.Close() }()

	raw, err := io.ReadAll(body)
	r.NoError(err)
	r.Equal("pixels", string(raw))
}

// TestSignedURLFallsBackToConfiguredBaseURL: a caller with no host of its own
// still gets a usable link rather than a relative one.
func TestSignedURLFallsBackToConfiguredBaseURL(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	svc, store, orgUID := newAttachmentsFixture(t)

	topic := models.AttachmentTopic(models.AttachmentEntityIncidents, "inc-1", models.AttachmentKindScreenshot)

	_, err := store.CreateAttachment(
		t.Context(), orgUID, "s.png", mimeTypePNG, topic, nil, []byte("x"))
	r.NoError(err)

	views, err := store.ListAttachmentViews(t.Context(), orgUID.String(), topic, "")
	r.NoError(err)
	r.Len(views, 1)
	r.Contains(views[0].DownloadURL, "https://fallback.acme.com/pub/files/")

	// And a nil file mints nothing rather than a URL to nowhere.
	r.Empty(svc.SignedURL(nil, "", time.Hour))
}

// TestDeleteAttachmentsRefusesABroadPrefix is the guard that stops a typo (or
// an empty uid variable) from wiping every attachment an org has.
func TestDeleteAttachmentsRefusesABroadPrefix(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	svc, store, orgUID := newAttachmentsFixture(t)

	topic := models.AttachmentTopic(models.AttachmentEntityIncidents, "inc-1", models.AttachmentKindScreenshot)

	_, err := store.CreateAttachment(
		t.Context(), orgUID, "s.png", mimeTypePNG, topic, nil, []byte("x"))
	r.NoError(err)

	for _, prefix := range []string{"", "/", "incidents", "incidents/", "incidents//"} {
		_, delErr := svc.DeleteAttachments(t.Context(), orgUID.String(), prefix)
		r.ErrorIs(delErr, ErrTopicPrefixTooBroad, "prefix %q must be refused", prefix)
	}

	// Positive control: the attachment is still there, so the refusals above
	// really did refuse rather than silently succeeding on nothing.
	views, err := store.ListAttachmentViews(t.Context(), orgUID.String(), topic, "")
	r.NoError(err)
	r.Len(views, 1)

	// And a properly-scoped prefix does work.
	deleted, err := svc.DeleteAttachments(
		t.Context(), orgUID.String(),
		models.AttachmentTopicPrefix(models.AttachmentEntityIncidents, "inc-1"),
	)
	r.NoError(err)
	r.Equal(int64(1), deleted)
}

// TestFileResponseOmitsAttachmentFieldsForPlainFiles pins the JSON shape: a
// non-attachment file's response must not grow empty `topic`/`details` keys.
func TestFileResponseOmitsAttachmentFieldsForPlainFiles(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	plain := toResponse(&models.File{UID: "f1", Name: "logo.png", MimeType: mimeTypePNG})
	r.Nil(plain.Topic)
	r.Nil(plain.Details)

	topic := "incidents/inc-1/screenshot"
	attached := toResponse(&models.File{
		UID: "f2", Name: "s.png", MimeType: mimeTypePNG,
		Topic:   &topic,
		Details: models.JSONMap{models.AttachmentDetailRegion: "eu"},
	})
	r.NotNil(attached.Topic)
	r.Equal(topic, *attached.Topic)
	r.Equal("eu", attached.Details[models.AttachmentDetailRegion])
}
