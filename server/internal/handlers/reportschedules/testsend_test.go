package reportschedules

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/httpx"
	"github.com/fclairamb/solidping/server/internal/i18n/recipient"
)

type enqueuedTest struct {
	orgUID, scheduleUID, address string
	hint                         recipient.Hint
}

// recordingEnqueuer captures every EnqueueTestSend call so tests can assert
// exactly what was queued, without a real queue.
type recordingEnqueuer struct {
	mu   sync.Mutex
	jobs []enqueuedTest
}

func (e *recordingEnqueuer) EnqueueTestSend(
	_ context.Context, orgUID, scheduleUID, address string, hint recipient.Hint, _ time.Time,
) (string, error) {
	e.mu.Lock()
	defer e.mu.Unlock()

	e.jobs = append(e.jobs, enqueuedTest{orgUID, scheduleUID, address, hint})

	return "job-1", nil
}

func (e *recordingEnqueuer) queued() []enqueuedTest {
	e.mu.Lock()
	defer e.mu.Unlock()

	return append([]enqueuedTest(nil), e.jobs...)
}

// testSendEnv builds a Service wired to a fresh in-memory SQLite and a
// recordingEnqueuer. No report builder exists in this package any more: the
// build runs in the job, so the request path cannot block on it.
type testSendEnv struct {
	svc      *Service
	enqueuer *recordingEnqueuer
	org      *models.Organization
}

func setupTestSendEnv(t *testing.T) *testSendEnv {
	t.Helper()

	ctx := context.Background()

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	require.NoError(t, err)
	require.NoError(t, dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	org := models.NewOrganization("test-send-org", "Test Send Org")
	require.NoError(t, dbSvc.CreateOrganization(ctx, org))

	cfg := &config.Config{}
	enqueuer := &recordingEnqueuer{}

	return &testSendEnv{
		svc:      NewService(dbSvc, cfg, enqueuer),
		enqueuer: enqueuer,
		org:      org,
	}
}

// createSchedule creates a bare org-wide monthly schedule to test-send against.
func (e *testSendEnv) createSchedule(t *testing.T) Response {
	t.Helper()

	ctx := context.Background()

	row, err := e.svc.Create(ctx, e.org.Slug, &CreateRequest{
		Name:      "Test schedule",
		Frequency: models.ReportFrequencyMonthly,
	})
	require.NoError(t, err)

	return row
}

// TestTestSend_SuppressedRecipient covers the secondary defect fixed alongside
// the 202-empty-body client bug: a suppressed recipient must be reported, not
// silently swallowed as if the mail had been queued.
func TestTestSend_SuppressedRecipient(t *testing.T) {
	t.Parallel()

	env := setupTestSendEnv(t)
	schedule := env.createSchedule(t)

	const address = "unsubscribed@example.com"

	ctx := context.Background()
	sup := models.NewEmailSuppression(env.org.UID, address, nil, models.EmailSuppressionSourceLink)
	require.NoError(t, env.svc.db.CreateEmailSuppression(ctx, sup))

	_, err := env.svc.TestSend(ctx, env.org.Slug, schedule.UID, address, recipient.Hint{}, time.Now())

	require.ErrorIs(t, err, ErrRecipientSuppressed,
		"suppressed recipient must surface ErrRecipientSuppressed, not a silent nil success")
	require.Empty(t, env.enqueuer.queued(), "a suppressed recipient must never be handed to the mailer")
}

// TestTestSend_NonSuppressedRecipient is the positive control for the test
// above: a normal recipient still enqueues exactly one mail, proving the
// suppression check didn't just start rejecting everything.
func TestTestSend_NonSuppressedRecipient(t *testing.T) {
	t.Parallel()

	env := setupTestSendEnv(t)
	schedule := env.createSchedule(t)

	const address = "operator@example.com"

	// The caller's language reaches the mailer, for an address with no account.
	caller := recipient.Hint{Locale: "fr", Timezone: "Europe/Paris"}

	jobUID, err := env.svc.TestSend(context.Background(), env.org.Slug, schedule.UID, address, caller, time.Now())

	require.NoError(t, err)
	require.Equal(t, "job-1", jobUID)
	require.Equal(t, []enqueuedTest{{env.org.UID, schedule.UID, address, caller}}, env.enqueuer.queued())
}

// TestTestSendHandler_AnswersWithJobUID pins the endpoint contract: 202 with
// the job uid, the work deferred to the job (the only collaborator is the
// enqueuer, so nothing can build the report inside the request).
func TestTestSendHandler_AnswersWithJobUID(t *testing.T) {
	t.Parallel()

	env := setupTestSendEnv(t)
	schedule := env.createSchedule(t)
	handler := NewHandler(env.svc, &config.Config{})

	router := httpx.New()
	router.POST("/orgs/:org/report-schedules/:uid/test", handler.TestSend)

	body := `{"recipient":"operator@example.com"}`
	req := httptest.NewRequestWithContext(
		context.Background(), http.MethodPost,
		"/orgs/"+env.org.Slug+"/report-schedules/"+schedule.UID+"/test",
		strings.NewReader(body),
	)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusAccepted, rec.Code)

	var resp map[string]string
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.Equal(t, "job-1", resp["jobUid"])
	require.Len(t, env.enqueuer.queued(), 1)

	// Failure path: an unknown schedule is rejected synchronously, nothing queued.
	req = httptest.NewRequestWithContext(
		context.Background(), http.MethodPost,
		"/orgs/"+env.org.Slug+"/report-schedules/nope/test", strings.NewReader(body),
	)
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusNotFound, rec.Code)
	require.Len(t, env.enqueuer.queued(), 1)
}
