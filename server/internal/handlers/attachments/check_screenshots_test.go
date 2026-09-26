package attachments

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/handlers/filestorage"
)

// blobPath is where the local-FS backend keeps a file row's bytes.
func blobPath(t *testing.T, svc *Service, file *models.File) string {
	t.Helper()

	_, rest, err := filestorage.SchemeFromURI(file.FileURI)
	require.NoError(t, err)

	return filepath.Join(svc.cfg.FileStorage.LocalRoot, rest)
}

// TestCheckScreenshotsKeepLastFive is spec 2026-09-25-34's retention decision:
// six check-scoped captures leave exactly five, and the one removed is the
// OLDEST — row and blob. Without the blob assertion the cap would bound the
// rows and not the storage it exists to bound.
func TestCheckScreenshotsKeepLastFive(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, dbService, svc, org := setupAttachmentsTest(t)

	checkUID := uuid.New().String()
	written := make([]*models.File, 0, MaxCheckScreenshots+1)

	for i := range MaxCheckScreenshots + 1 {
		fileUID, err := svc.PutCheckScreenshot(ctx, org.UID, checkUID, pngBytes("shot-"+string(rune('a'+i))),
			models.JSONMap{DetailKeyCheckUID: checkUID, DetailKeyTrigger: TriggerCheckFailure})
		r.NoError(err)

		row, err := dbService.GetFile(ctx, org.UID, fileUID)
		r.NoError(err)

		written = append(written, row)

		// Distinct created_at values, so "oldest" is a fact and not a tie.
		time.Sleep(2 * time.Millisecond)
	}

	live, _, err := dbService.ListFiles(ctx, org.UID, models.ListFilesFilter{Topic: CheckScreenshotTopic(checkUID)})
	r.NoError(err)
	r.Len(live, MaxCheckScreenshots, "six writes must leave exactly five")

	oldest := written[0]

	_, err = dbService.GetFile(ctx, org.UID, oldest.UID)
	r.ErrorIs(err, sql.ErrNoRows, "the OLDEST capture is the one retired")

	_, statErr := os.Stat(blobPath(t, svc, oldest))
	r.ErrorIs(statErr, os.ErrNotExist, "the retired capture's blob is removed from storage")

	for _, kept := range written[1:] {
		_, err := dbService.GetFile(ctx, org.UID, kept.UID)
		r.NoError(err, "the five newest survive")

		_, statErr := os.Stat(blobPath(t, svc, kept))
		r.NoError(statErr, "a surviving capture keeps its blob")
	}
}

// TestIncidentScreenshotStillReplacesAlongsideCheckTopic is the control for the
// exception above: the append-and-prune rule belongs to the check topic ONLY.
// An incident topic written twice still keeps one row.
func TestIncidentScreenshotStillReplacesAlongsideCheckTopic(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, dbService, svc, org := setupAttachmentsTest(t)

	incidentUID := uuid.New().String()

	for _, marker := range []string{"one", "two"} {
		_, err := svc.PutIncidentScreenshot(ctx, org.UID, incidentUID, pngBytes(marker), nil)
		r.NoError(err)
	}

	live, _, err := dbService.ListFiles(ctx, org.UID, models.ListFilesFilter{
		Topic: IncidentScreenshotTopic(incidentUID),
	})
	r.NoError(err)
	r.Len(live, 1)
}

// TestListCheckScreenshots pins the listing's contract on SQLite (the Postgres
// twin lives in internal/db/postgres): captures from several incidents and the
// check-scoped topic, newest first, `limit` respected, other checks' captures
// and non-screenshot kinds excluded, an empty list for a check with none.
func TestListCheckScreenshots(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, _, svc, org := setupAttachmentsTest(t)

	checkUID := uuid.New().String()
	otherCheckUID := uuid.New().String()
	firstIncident := uuid.New().String()
	secondIncident := uuid.New().String()
	capturedAt := time.Date(2026, 9, 25, 10, 0, 0, 0, time.UTC)

	step := func() { time.Sleep(2 * time.Millisecond) }

	oldest, err := svc.PutIncidentScreenshot(ctx, org.UID, firstIncident, pngBytes("first"), models.JSONMap{
		DetailKeyCheckUID: checkUID, DetailKeyRegion: "eu-west", DetailKeyTrigger: TriggerIncidentOpen,
		DetailKeyCapturedAt: capturedAt.Format(time.RFC3339),
	})
	r.NoError(err)
	step()

	// Another check's capture, and a path capture of this check: neither may
	// come back.
	_, err = svc.PutIncidentScreenshot(ctx, org.UID, uuid.New().String(), pngBytes("other"),
		models.JSONMap{DetailKeyCheckUID: otherCheckUID})
	r.NoError(err)
	step()

	middle, err := svc.PutCheckScreenshot(ctx, org.UID, checkUID, pngBytes("blip"), models.JSONMap{
		DetailKeyCheckUID: checkUID, DetailKeyTrigger: TriggerCheckFailure,
	})
	r.NoError(err)
	step()

	newest, err := svc.PutIncidentScreenshot(ctx, org.UID, secondIncident, pngBytes("second"), models.JSONMap{
		DetailKeyCheckUID: checkUID, DetailKeyRegion: "us-east", DetailKeyTrigger: TriggerIncidentReopen,
	})
	r.NoError(err)

	list, err := svc.ListCheckScreenshots(ctx, org.UID, checkUID, 10)
	r.NoError(err)
	r.Len(list, 3)
	r.Equal([]string{newest, middle, oldest}, []string{list[0].UID, list[1].UID, list[2].UID},
		"newest first")

	r.Equal(secondIncident, list[0].IncidentUID)
	r.Equal("us-east", list[0].Region)
	r.Equal(TriggerIncidentReopen, list[0].Trigger)
	r.False(list[0].CapturedAt.IsZero(), "no capturedAt in the bag falls back to the stored time")

	r.Empty(list[1].IncidentUID, "a check-scoped capture names no incident")
	r.Equal(TriggerCheckFailure, list[1].Trigger)

	r.Equal(firstIncident, list[2].IncidentUID)
	r.Equal(capturedAt, list[2].CapturedAt.UTC())
	r.True(strings.HasPrefix(list[2].DownloadURL, "/pub/files/"+oldest+"?"))
	r.Contains(list[2].DownloadURL, "sig=")
	r.Equal("image/png", list[2].MimeType)

	limited, err := svc.ListCheckScreenshots(ctx, org.UID, checkUID, 2)
	r.NoError(err)
	r.Len(limited, 2)
	r.Equal(newest, limited[0].UID)

	empty, err := svc.ListCheckScreenshots(ctx, org.UID, uuid.New().String(), 5)
	r.NoError(err)
	r.NotNil(empty, "an empty listing is [] on the wire, never null")
	r.Empty(empty)

	raw, err := json.Marshal(list[1])
	r.NoError(err)
	r.NotContains(string(raw), "incidentUid", "a check-scoped capture omits incidentUid")
	r.Contains(string(raw), `"capturedAt"`)
}

// TestCheckAuthorizer walks the check topic authorizer's refusals, each backed
// by the positive control of a legitimate agent getting through.
func TestCheckAuthorizer(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, dbService, _, org := setupAttachmentsTest(t)

	check := models.NewCheck(org.UID, "web", "browser")
	check.Regions = []string{"eu-west"}
	r.NoError(dbService.CreateCheck(ctx, check))

	other := models.NewOrganization("globex", "Globex")
	r.NoError(dbService.CreateOrganization(ctx, other))

	authorizer := NewCheckAuthorizer(dbService)
	topic := ParsedTopic{Entity: EntityChecks, EntityUID: check.UID, Kind: KindScreenshot}

	orgUID, err := authorizer.Authorize(ctx, topic, UploaderIdentity{AgentOrgUID: org.UID, AgentRegion: "eu-west"})
	r.NoError(err, "an org agent serving the check's region may write")
	r.Equal(org.UID, orgUID, "the org comes from the check row")

	orgUID, err = authorizer.Authorize(ctx, topic, UploaderIdentity{AgentRegion: "eu-west"})
	r.NoError(err, "a system agent serving the region may write")
	r.Equal(org.UID, orgUID)

	_, err = authorizer.Authorize(ctx, topic, UploaderIdentity{AgentOrgUID: other.UID, AgentRegion: "eu-west"})
	r.ErrorIs(err, ErrTopicForbidden, "another org's agent is refused")

	_, err = authorizer.Authorize(ctx, topic, UploaderIdentity{AgentOrgUID: org.UID, AgentRegion: "us-east"})
	r.ErrorIs(err, ErrTopicForbidden, "an agent whose region does not serve the check is refused")

	_, err = authorizer.Authorize(ctx,
		ParsedTopic{Entity: EntityChecks, EntityUID: uuid.New().String(), Kind: KindScreenshot},
		UploaderIdentity{AgentRegion: "eu-west"})
	r.ErrorIs(err, ErrTopicForbidden, "an unknown check is refused")

	r.NoError(dbService.DeleteCheck(ctx, check.UID))

	_, err = authorizer.Authorize(ctx, topic, UploaderIdentity{AgentOrgUID: org.UID, AgentRegion: "eu-west"})
	r.ErrorIs(err, ErrTopicForbidden, "a deleted check is refused")
}

// TestAgentUploadToCheckTopic covers the agent half of the check topic: the
// upload lands under the check with its checkUid stamped from the TOPIC, and an
// agent upload of an incident screenshot now carries its check too (it used to
// carry none, which hid it from the check page).
func TestAgentUploadToCheckTopic(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	f := setupUploadTest(t)

	agent := enrollAgent(f.ctx(), t, f.db, f.org.UID, "eu-west")

	rec := f.serve(t, agent.signedRequest(f.ctx(), t, CheckScreenshotTopic(f.check.UID), pngBytes("cap")))
	r.Equal(http.StatusCreated, rec.Code, rec.Body.String())

	var resp UploadResponse
	r.NoError(json.Unmarshal(rec.Body.Bytes(), &resp))

	stored, err := f.db.GetFile(f.ctx(), f.org.UID, resp.FileUID)
	r.NoError(err)
	r.Equal(CheckScreenshotTopic(f.check.UID), *stored.Topic)
	r.Equal(f.check.UID, stored.Details[DetailKeyCheckUID])
	r.Equal("eu-west", stored.Details[DetailKeyRegion])

	rec = f.serve(t, agent.signedRequest(f.ctx(), t, IncidentScreenshotTopic(f.incident.UID), pngBytes("inc")))
	r.Equal(http.StatusCreated, rec.Code, rec.Body.String())
	r.NoError(json.Unmarshal(rec.Body.Bytes(), &resp))

	stored, err = f.db.GetFile(f.ctx(), f.org.UID, resp.FileUID)
	r.NoError(err)
	r.Equal(f.check.UID, stored.Details[DetailKeyCheckUID], "stamped from the incident row")

	list, err := f.svc.ListCheckScreenshots(f.ctx(), f.org.UID, f.check.UID, 5)
	r.NoError(err)
	r.Len(list, 2, "both agent uploads show up on the check's listing")
}

// TestUploadBudgetIsPerEntity pins that check-scoped uploads cannot spend the
// budget an incident's onset upload needs: an agent that exhausted its check
// budget still gets its incident upload through.
func TestUploadBudgetIsPerEntity(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	f := setupUploadTest(t)

	agent := enrollAgent(f.ctx(), t, f.db, f.org.UID, "eu-west")

	for range uploadRateBurst {
		rec := f.serve(t, agent.signedRequest(f.ctx(), t, CheckScreenshotTopic(f.check.UID), pngBytes("c")))
		r.Equal(http.StatusCreated, rec.Code, rec.Body.String())
	}

	rec := f.serve(t, agent.signedRequest(f.ctx(), t, CheckScreenshotTopic(f.check.UID), pngBytes("c")))
	r.Equal(http.StatusTooManyRequests, rec.Code, "the check budget is exhausted")

	rec = f.serve(t, agent.signedRequest(f.ctx(), t, IncidentScreenshotTopic(f.incident.UID), pngBytes("i")))
	r.Equal(http.StatusCreated, rec.Code, "the incident budget is untouched: %s", rec.Body.String())
}
