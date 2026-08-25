package attachments

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/handlers/files"
	"github.com/fclairamb/solidping/server/internal/handlers/filestorage/localfs"
)

// pngBytes is a minimal blob whose leading bytes are a real PNG signature, so
// the mime sniff accepts it without dragging an image encoder into the tests.
func pngBytes(marker string) []byte {
	return append([]byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}, []byte(marker)...)
}

func setupAttachmentsTest(t *testing.T) (context.Context, db.Service, *Service, *models.Organization) {
	t.Helper()

	ctx := t.Context()

	dbService, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	require.NoError(t, err)
	require.NoError(t, dbService.Initialize(ctx))
	t.Cleanup(func() { _ = dbService.Close() })

	org := models.NewOrganization("acme", "Acme")
	require.NoError(t, dbService.CreateOrganization(ctx, org))

	// The local-FS backend self-registers on demand; the production wiring does
	// this once in server.go.
	localfs.Register()

	cfg := &config.Config{}
	cfg.Auth.JWTSecret = "test-secret"
	cfg.FileStorage.Type = "local"
	cfg.FileStorage.LocalRoot = t.TempDir()

	return ctx, dbService, NewService(files.NewService(dbService, cfg), dbService, cfg), org
}

// TestAttachmentRoundTrip pins the whole rail end to end on a real database:
// the topic and the details bag survive the write, the exact-topic list finds
// the row, and the response carries camelCase JSON with a signed URL.
func TestAttachmentRoundTrip(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, dbService, svc, org := setupAttachmentsTest(t)

	incidentUID := uuid.New().String()
	capturedAt := time.Now().UTC().Truncate(time.Second)

	fileUID, err := svc.PutIncidentScreenshot(ctx, org.UID, incidentUID, pngBytes("first"),
		models.JSONMap{
			DetailKeyCapturedAt: capturedAt.Format(time.RFC3339),
			DetailKeyRegion:     "eu-west",
			DetailKeyCheckUID:   "check-1",
			DetailKeyTrigger:    TriggerIncidentOpen,
		})
	r.NoError(err)
	r.NotEmpty(fileUID)

	// The row really carries the topic and the bag.
	stored, err := dbService.GetFile(ctx, org.UID, fileUID)
	r.NoError(err)
	r.NotNil(stored.Topic)
	r.Equal(IncidentScreenshotTopic(incidentUID), *stored.Topic)
	r.Equal("eu-west", stored.Details[DetailKeyRegion])
	r.Equal("image/png", stored.MimeType)

	list, err := svc.ListIncidentAttachments(ctx, org.UID, incidentUID)
	r.NoError(err)
	r.Len(list, 1)
	r.Equal(KindScreenshot, list[0].Kind)
	r.Equal("eu-west", list[0].Region)
	r.Equal("check-1", list[0].CheckUID)
	r.Equal(TriggerIncidentOpen, list[0].Trigger)
	r.NotNil(list[0].CapturedAt)
	r.Equal(capturedAt, list[0].CapturedAt.UTC())
	r.Contains(list[0].DownloadURL, "/pub/files/"+fileUID)
	r.Contains(list[0].DownloadURL, "sig=")

	// camelCase on the wire.
	raw, err := json.Marshal(list[0])
	r.NoError(err)
	r.Contains(string(raw), `"downloadUrl"`)
	r.Contains(string(raw), `"mimeType"`)
	r.Contains(string(raw), `"capturedAt"`)
	r.NotContains(string(raw), `"download_url"`)
}

// TestPutIncidentScreenshotReplaces proves the overwrite rule: a second write
// under the same incident retires the first one instead of accumulating.
func TestPutIncidentScreenshotReplaces(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, _, svc, org := setupAttachmentsTest(t)

	incidentUID := uuid.New().String()

	first, err := svc.PutIncidentScreenshot(ctx, org.UID, incidentUID, pngBytes("first"), nil)
	r.NoError(err)

	second, err := svc.PutIncidentScreenshot(ctx, org.UID, incidentUID, pngBytes("second"), nil)
	r.NoError(err)
	r.NotEqual(first, second)

	list, err := svc.ListIncidentAttachments(ctx, org.UID, incidentUID)
	r.NoError(err)
	r.Len(list, 1, "the previous capture must be retired, not accumulated")
	r.Equal(second, list[0].UID)
}

// TestDeleteAttachmentsIsPrefixScoped is the negative the prefix semantics
// stand or fall on: reaping one incident must not touch a DIFFERENT incident
// whose uid merely starts with the same characters, nor any non-attachment file.
func TestDeleteAttachmentsIsPrefixScoped(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, dbService, svc, org := setupAttachmentsTest(t)

	// Deliberately overlapping: "abc" is a strict prefix of "abcdef".
	const (
		shortUID = "abc"
		longUID  = "abcdef"
	)

	shortFile, err := svc.PutIncidentScreenshot(ctx, org.UID, shortUID, pngBytes("short"), nil)
	r.NoError(err)

	longFile, err := svc.PutIncidentScreenshot(ctx, org.UID, longUID, pngBytes("long"), nil)
	r.NoError(err)

	// A plain, unattached file: the sort of row that must never be swept.
	plain := models.NewFile(org.UID, "logo.png", "image/png", "file://x", 3, nil)
	r.NoError(dbService.CreateFile(ctx, plain))

	count, err := svc.DeleteIncidentAttachments(ctx, org.UID, shortUID)
	r.NoError(err)
	r.Equal(1, count)

	gone, err := svc.ListIncidentAttachments(ctx, org.UID, shortUID)
	r.NoError(err)
	r.Empty(gone)

	survivor, err := svc.ListIncidentAttachments(ctx, org.UID, longUID)
	r.NoError(err)
	r.Len(survivor, 1, "the trailing slash must stop abc/ from matching abcdef/")
	r.Equal(longFile, survivor[0].UID)

	_, err = dbService.GetFile(ctx, org.UID, plain.UID)
	r.NoError(err, "an unattached file is invisible to the reaper")

	_, err = dbService.GetFile(ctx, org.UID, shortFile)
	r.Error(err, "the reaped attachment is soft-deleted")
}

// TestDeleteAttachmentsIsOrgScoped proves one org's reap cannot reach another's
// attachments even when the topics are identical.
func TestDeleteAttachmentsIsOrgScoped(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, dbService, svc, org := setupAttachmentsTest(t)

	other := models.NewOrganization("acmetwo", "Acme Two")
	r.NoError(dbService.CreateOrganization(ctx, other))

	incidentUID := uuid.New().String()

	_, err := svc.PutIncidentScreenshot(ctx, org.UID, incidentUID, pngBytes("mine"), nil)
	r.NoError(err)
	_, err = svc.PutIncidentScreenshot(ctx, other.UID, incidentUID, pngBytes("theirs"), nil)
	r.NoError(err)

	count, err := svc.DeleteIncidentAttachments(ctx, org.UID, incidentUID)
	r.NoError(err)
	r.Equal(1, count)

	theirs, err := svc.ListIncidentAttachments(ctx, other.UID, incidentUID)
	r.NoError(err)
	r.Len(theirs, 1)
}

// TestPutRejectsBadBodies pins the size and mime gates, with a positive control
// so "everything is rejected" cannot pass.
func TestPutRejectsBadBodies(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, _, svc, org := setupAttachmentsTest(t)

	topic := IncidentScreenshotTopic(uuid.New().String())

	_, err := svc.Put(ctx, org.UID, topic, "x.png", nil, nil)
	r.ErrorIs(err, ErrEmptyAttachment)

	_, err = svc.Put(ctx, org.UID, topic, "x.png", []byte("GIF89a-not-a-png"), nil)
	r.ErrorIs(err, ErrUnsupportedMediaType)

	oversize := append(pngBytes(""), make([]byte, MaxAttachmentBytes)...)
	_, err = svc.Put(ctx, org.UID, topic, "x.png", oversize, nil)
	r.ErrorIs(err, ErrAttachmentTooLarge)

	// Positive control.
	uid, err := svc.Put(ctx, org.UID, topic, "x.png", pngBytes("ok"), nil)
	r.NoError(err)
	r.NotEmpty(uid)
}

// TestParseTopicRejectsHostileInput covers the strings that would be dangerous
// if a topic reached storage or a prefix reap unvalidated.
func TestParseTopicRejectsHostileInput(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	bad := []string{
		"",
		"incidents",
		"incidents/uid",
		"incidents/uid/kind/extra",
		"incidents//screenshot",
		"incidents/../screenshot",
		"incidents/uid/scre%nshot",
		"incidents/uid/scre_nshot",
		"incidents/ui d/screenshot",
	}

	for _, topic := range bad {
		_, err := ParseTopic(topic)
		r.ErrorIs(err, ErrInvalidTopic, "topic %q must be rejected", topic)
	}

	// Positive control: the real shape parses.
	parsed, err := ParseTopic("incidents/9a1eb273-0a95-4d6b-b967-9af076c1f8e8/screenshot")
	r.NoError(err)
	r.Equal(EntityIncidents, parsed.Entity)
	r.Equal(KindScreenshot, parsed.Kind)
}

// TestAuthorizerRegistryFailsClosed pins that an entity nobody registered is
// refused rather than waved through.
func TestAuthorizerRegistryFailsClosed(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	registry := NewAuthorizerRegistry()
	registry.Register(EntityIncidents, AuthorizerFunc(func(
		context.Context, ParsedTopic, UploaderIdentity,
	) (string, error) {
		return "org-1", nil
	}))

	_, err := registry.Authorize(t.Context(),
		ParsedTopic{Entity: "checks", EntityUID: "c1", Kind: "screenshot"}, UploaderIdentity{})
	r.ErrorIs(err, ErrUnknownTopicEntity)

	// Positive control: the registered entity resolves.
	orgUID, err := registry.Authorize(t.Context(),
		ParsedTopic{Entity: EntityIncidents, EntityUID: "i1", Kind: KindScreenshot}, UploaderIdentity{})
	r.NoError(err)
	r.Equal("org-1", orgUID)
}

// TestIncidentAuthorizer walks the refusal chain: unknown incident, foreign
// org, and a region that does not serve the incident's check — each rejected,
// with the legitimate case accepted as the positive control.
func TestIncidentAuthorizer(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, dbService, _, org := setupAttachmentsTest(t)

	other := models.NewOrganization("acmetwo", "Acme Two")
	r.NoError(dbService.CreateOrganization(ctx, other))

	check := models.NewCheck(org.UID, "api", "browser")
	check.Regions = []string{"eu-west"}
	r.NoError(dbService.CreateCheck(ctx, check))

	incident := models.NewIncident(org.UID, check.UID, time.Now(), "api is down")
	r.NoError(dbService.CreateIncident(ctx, incident))

	authorizer := NewIncidentAuthorizer(dbService)
	topic := ParsedTopic{Entity: EntityIncidents, EntityUID: incident.UID, Kind: KindScreenshot}

	// Positive control FIRST, so the refusals below are known to be about the
	// specific condition and not about an authorizer that refuses everything.
	orgUID, err := authorizer.Authorize(ctx, topic, UploaderIdentity{
		AgentUID: "a1", AgentOrgUID: org.UID, AgentRegion: "eu-west",
	})
	r.NoError(err)
	r.Equal(org.UID, orgUID, "the org comes from the incident, never from the request")

	_, err = authorizer.Authorize(ctx,
		ParsedTopic{Entity: EntityIncidents, EntityUID: uuid.New().String(), Kind: KindScreenshot},
		UploaderIdentity{AgentUID: "a1", AgentOrgUID: org.UID, AgentRegion: "eu-west"})
	r.ErrorIs(err, ErrTopicForbidden, "an unknown incident is refused")

	_, err = authorizer.Authorize(ctx, topic, UploaderIdentity{
		AgentUID: "a2", AgentOrgUID: other.UID, AgentRegion: "eu-west",
	})
	r.ErrorIs(err, ErrTopicForbidden, "another org's agent is refused")

	_, err = authorizer.Authorize(ctx, topic, UploaderIdentity{
		AgentUID: "a3", AgentOrgUID: org.UID, AgentRegion: "us-east",
	})
	r.ErrorIs(err, ErrTopicForbidden, "an agent whose region does not serve the check is refused")

	_, err = authorizer.Authorize(ctx, topic, UploaderIdentity{
		AgentUID: "a4", AgentOrgUID: org.UID, AgentRegion: "",
	})
	r.ErrorIs(err, ErrTopicForbidden, "an agent with no region could not have run anything")

	// A SYSTEM agent (no org binding) serving the right region is allowed —
	// otherwise cloud-region captures could never be uploaded at all.
	orgUID, err = authorizer.Authorize(ctx, topic, UploaderIdentity{
		AgentUID: "sys", AgentOrgUID: "", AgentRegion: "eu-west",
	})
	r.NoError(err)
	r.Equal(org.UID, orgUID)
}

// TestRegionServesCheckUnpinnedCheck pins the "no regions means any region"
// rule that makes an unpinned check's capture uploadable.
func TestRegionServesCheckUnpinnedCheck(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	r.True(regionServesCheck("eu-west", nil))
	r.True(regionServesCheck("eu-west", []string{}))
	r.True(regionServesCheck("eu-west", []string{"us-east", "eu-west"}))
	r.False(regionServesCheck("eu-west", []string{"us-east"}))
	r.False(regionServesCheck("", nil))
}
