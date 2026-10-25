package attachments

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/handlers/filestorage"
)

func stepEnvelope(t *testing.T, runUID string, payload string) []byte {
	t.Helper()

	body, err := EncodeStepState(runUID, "crawl", []byte(payload))
	require.NoError(t, err)

	return body
}

// step-state only accepts the {v, runUid, checkType, payload} envelope.
func TestStepStateRefusesNonEnvelopeJSON(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, _, svc, org := setupAttachmentsTest(t)
	checkUID := uuid.New().String()

	for _, body := range []string{
		`{"queue":[]}`,
		`{"v":2,"runUid":"r","checkType":"crawl","payload":{}}`,
		`{"v":1,"checkType":"crawl","payload":{}}`,
		`{"v":1,"runUid":"r","checkType":"crawl","payload":{},"extra":1}`,
		`not json`,
	} {
		_, err := svc.PutStepState(ctx, org.UID, checkUID, []byte(body), nil)
		r.ErrorIs(err, ErrUnsupportedMediaType, body)
	}

	_, err := svc.PutStepState(ctx, org.UID, checkUID, stepEnvelope(t, "run-1", `{"queue":[]}`), nil)
	r.NoError(err)

	_, err = svc.PutStepState(ctx, org.UID, checkUID, []byte(strings.Repeat(" ", MaxStepStateBytes+1)), nil)
	r.ErrorIs(err, ErrAttachmentTooLarge)
}

// State keeps exactly two files (the pointer decides which is current),
// crawl reports keep five, and neither lands in the screenshots group.
func TestStepStateAndCrawlReportRetention(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, dbService, svc, org := setupAttachmentsTest(t)
	checkUID := uuid.New().String()

	stateUIDs := make([]string, 0, 4)

	for i := range 4 {
		fileUID, err := svc.PutStepState(ctx, org.UID, checkUID,
			stepEnvelope(t, "run-1", `{"n":`+string(rune('0'+i))+`}`), StepStateDetails("run-1", i, nil))
		r.NoError(err)

		stateUIDs = append(stateUIDs, fileUID)

		time.Sleep(2 * time.Millisecond)
	}

	live, _, err := dbService.ListFiles(ctx, org.UID, models.ListFilesFilter{Topic: CheckStepStateTopic(checkUID)})
	r.NoError(err)
	r.Len(live, MaxStepStateFiles)

	// The previous state is still readable after a newer one was written.
	body, file, err := svc.ReadCheckFile(ctx, org.UID, checkUID, KindStepState, stateUIDs[2])
	r.NoError(err)
	r.Contains(string(body), `"n":2`)
	r.Contains(file.FileURI, string(filestorage.GroupTypeCheckState))
	r.NotContains(file.FileURI, string(filestorage.GroupTypeScreenshots))

	// A pointer cannot read another check's file.
	_, _, err = svc.ReadCheckFile(ctx, org.UID, uuid.New().String(), KindStepState, stateUIDs[3])
	r.ErrorIs(err, ErrStepFileMissing)

	for i := range MaxCrawlReports + 2 {
		_, putErr := svc.PutCrawlReport(ctx, org.UID, checkUID,
			[]byte(`{"findings":[],"pagesCrawled":`+string(rune('0'+i))+`}`), nil)
		r.NoError(putErr)

		time.Sleep(2 * time.Millisecond)
	}

	reports, err := svc.ListCrawlReports(ctx, org.UID, checkUID)
	r.NoError(err)
	r.Len(reports, MaxCrawlReports)
	r.NotEmpty(reports[0].DownloadURL)

	latest, err := svc.LatestCrawlReport(ctx, org.UID, checkUID)
	r.NoError(err)
	r.Contains(string(latest), `"pagesCrawled":6`)

	rows, _, err := dbService.ListFiles(ctx, org.UID, models.ListFilesFilter{Topic: CheckCrawlReportTopic(checkUID)})
	r.NoError(err)
	r.Contains(rows[0].FileURI, string(filestorage.GroupTypeReports))

	_, err = svc.PutCrawlReport(ctx, org.UID, checkUID, []byte(`{"pages":1}`), nil)
	r.ErrorIs(err, ErrUnsupportedMediaType, "a report must carry a findings array")

	r.NoError(svc.PurgeStepState(ctx, org.UID, checkUID))

	live, _, err = dbService.ListFiles(ctx, org.UID, models.ListFilesFilter{Topic: CheckStepStateTopic(checkUID)})
	r.NoError(err)
	r.Empty(live)
}

// Only the in-process worker writes step state and crawl reports: an agent
// upload under either kind is refused, whatever the body.
func TestAgentUploadRefusesStepStateAndCrawlReport(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	f := setupUploadTest(t)
	agent := enrollAgent(f.ctx(), t, f.db, f.org.UID, "eu-west")

	envelope := stepEnvelope(t, "run-1", `{}`)
	report, err := json.Marshal(map[string]any{"findings": []any{}})
	r.NoError(err)

	for topic, body := range map[string][]byte{
		CheckStepStateTopic(f.check.UID):   envelope,
		CheckCrawlReportTopic(f.check.UID): report,
		TaskStepStateTopic(f.check.UID):    envelope,
	} {
		rec := f.serve(t, agent.signedRequest(f.ctx(), t, topic, body))
		r.True(rec.Code == http.StatusForbidden || rec.Code == http.StatusBadRequest,
			"%s: %d %s", topic, rec.Code, rec.Body.String())

		rows, _, listErr := f.db.ListFiles(f.ctx(), f.org.UID, models.ListFilesFilter{Topic: topic})
		r.NoError(listErr)
		r.Empty(rows, "nothing stored under %s", topic)
	}
}

// Worker-task state (spec 2026-10-22-02) keeps two files per task, and a
// pointer can only read its own task's file.
func TestTaskStateRetentionAndScope(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, dbService, svc, org := setupAttachmentsTest(t)
	taskUID := uuid.New().String()

	uids := make([]string, 0, 3)

	for i := range 3 {
		fileUID, err := svc.PutTaskState(ctx, org.UID, taskUID,
			stepEnvelope(t, taskUID, `{"n":`+string(rune('0'+i))+`}`), nil)
		r.NoError(err)

		uids = append(uids, fileUID)

		time.Sleep(2 * time.Millisecond)
	}

	live, _, err := dbService.ListFiles(ctx, org.UID, models.ListFilesFilter{Topic: TaskStepStateTopic(taskUID)})
	r.NoError(err)
	r.Len(live, MaxStepStateFiles)

	body, err := svc.ReadTaskState(ctx, org.UID, taskUID, uids[2])
	r.NoError(err)
	r.Contains(string(body), `"n":2`)

	_, err = svc.ReadTaskState(ctx, org.UID, uuid.New().String(), uids[2])
	r.ErrorIs(err, ErrStepFileMissing)

	_, err = svc.PutTaskState(ctx, org.UID, taskUID, make([]byte, MaxStepStateBytes+1), nil)
	r.ErrorIs(err, ErrAttachmentTooLarge)

	r.NoError(svc.PurgeTaskState(ctx, org.UID, taskUID))

	live, _, err = dbService.ListFiles(ctx, org.UID, models.ListFilesFilter{Topic: TaskStepStateTopic(taskUID)})
	r.NoError(err)
	r.Empty(live)
}
