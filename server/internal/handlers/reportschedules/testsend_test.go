package reportschedules

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/handlers/base"
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

// failingDB fails the chosen lookups, the way a database outage would.
type failingDB struct {
	db.Service

	failGetSchedule bool
	failSuppressed  bool
}

var errDBDown = errors.New("db down")

func (f *failingDB) GetReportSchedule(ctx context.Context, orgUID, uid string) (*models.ReportSchedule, error) {
	if f.failGetSchedule {
		return nil, errDBDown
	}

	return f.Service.GetReportSchedule(ctx, orgUID, uid)
}

func (f *failingDB) IsEmailSuppressed(ctx context.Context, orgUID, email, scope string) (bool, error) {
	if f.failSuppressed {
		return false, errDBDown
	}

	return f.Service.IsEmailSuppressed(ctx, orgUID, email, scope)
}

func TestTestSend_RejectedRequests(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		slug    func(env *testSendEnv) string
		uid     func(schedule Response) string
		address string
		// svc builds the service under test; nil keeps the env's.
		svc     func(env *testSendEnv) *Service
		wantErr error
	}{
		{
			name:    "unknown organization",
			slug:    func(*testSendEnv) string { return "nope" },
			uid:     func(s Response) string { return s.UID },
			address: "me@example.com",
			wantErr: ErrOrganizationNotFound,
		},
		{
			name:    "unknown schedule",
			slug:    func(e *testSendEnv) string { return e.org.Slug },
			uid:     func(Response) string { return "nope" },
			address: "me@example.com",
			wantErr: ErrReportScheduleNotFound,
		},
		{
			name:    "blank address",
			slug:    func(e *testSendEnv) string { return e.org.Slug },
			uid:     func(s Response) string { return s.UID },
			address: "   ",
			wantErr: ErrNoTestRecipient,
		},
		{
			name:    "malformed address",
			slug:    func(e *testSendEnv) string { return e.org.Slug },
			uid:     func(s Response) string { return s.UID },
			address: "not an address",
			wantErr: ErrInvalidRecipient,
		},
		{
			name:    "no delivery configured",
			slug:    func(e *testSendEnv) string { return e.org.Slug },
			uid:     func(s Response) string { return s.UID },
			address: "me@example.com",
			svc:     func(e *testSendEnv) *Service { return NewService(e.svc.db, &config.Config{}, nil) },
			wantErr: ErrNoTestRecipient,
		},
		{
			name:    "schedule lookup outage",
			slug:    func(e *testSendEnv) string { return e.org.Slug },
			uid:     func(s Response) string { return s.UID },
			address: "me@example.com",
			svc: func(e *testSendEnv) *Service {
				return NewService(&failingDB{Service: e.svc.db, failGetSchedule: true}, &config.Config{}, e.enqueuer)
			},
			wantErr: errDBDown,
		},
		{
			name:    "suppression lookup outage",
			slug:    func(e *testSendEnv) string { return e.org.Slug },
			uid:     func(s Response) string { return s.UID },
			address: "me@example.com",
			svc: func(e *testSendEnv) *Service {
				return NewService(&failingDB{Service: e.svc.db, failSuppressed: true}, &config.Config{}, e.enqueuer)
			},
			wantErr: errDBDown,
		},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			env := setupTestSendEnv(t)
			schedule := env.createSchedule(t)

			svc := env.svc
			if testCase.svc != nil {
				svc = testCase.svc(env)
			}

			_, err := svc.TestSend(
				context.Background(), testCase.slug(env), testCase.uid(schedule),
				testCase.address, recipient.Hint{}, time.Now(),
			)

			require.ErrorIs(t, err, testCase.wantErr)
			require.Empty(t, env.enqueuer.queued(), "a rejected request must queue nothing")
		})
	}
}

// failingEnqueuer is a queue that refuses the job.
type failingEnqueuer struct{}

func (failingEnqueuer) EnqueueTestSend(
	context.Context, string, string, string, recipient.Hint, time.Time,
) (string, error) {
	return "", errDBDown
}

func TestTestSend_EnqueueFailureIsReturned(t *testing.T) {
	t.Parallel()

	env := setupTestSendEnv(t)
	schedule := env.createSchedule(t)
	svc := NewService(env.svc.db, &config.Config{}, failingEnqueuer{})

	jobUID, err := svc.TestSend(
		context.Background(), env.org.Slug, schedule.UID, "me@example.com", recipient.Hint{}, time.Now(),
	)

	require.ErrorIs(t, err, errDBDown)
	require.Empty(t, jobUID)
}

// TestTestSendHandler_DefaultsToTheCaller pins the safety rule of the endpoint:
// with no body, the test goes to the authenticated caller, in the caller's
// language, never to the schedule's recipients. A body recipient wins over the
// caller's address but the caller's language still applies.
func TestTestSendHandler_DefaultsToTheCaller(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		body        string
		wantAddress string
	}{
		{name: "no body", body: "", wantAddress: "caller@example.com"},
		{name: "unparseable body", body: "{", wantAddress: "caller@example.com"},
		{name: "override recipient", body: `{"recipient":"other@example.com"}`, wantAddress: "other@example.com"},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			env := setupTestSendEnv(t)
			schedule := env.createSchedule(t)
			handler := NewHandler(env.svc, &config.Config{})

			router := httpx.New()
			router.POST("/orgs/:org/report-schedules/:uid/test", handler.TestSend)

			caller := &models.User{
				Email:       "caller@example.com",
				Preferences: &models.UserPreferences{Locale: "fr", Timezone: "Europe/Paris"},
			}
			ctx := context.WithValue(context.Background(), base.ContextKeyUser, caller)

			req := httptest.NewRequestWithContext(
				ctx, http.MethodPost,
				"/orgs/"+env.org.Slug+"/report-schedules/"+schedule.UID+"/test",
				strings.NewReader(testCase.body),
			)
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)

			require.Equal(t, http.StatusAccepted, rec.Code)
			require.Equal(t, []enqueuedTest{{
				env.org.UID, schedule.UID, testCase.wantAddress,
				recipient.Hint{Locale: "fr", Timezone: "Europe/Paris"},
			}}, env.enqueuer.queued())
		})
	}
}

func TestTestSendHandler_NoAddressIsRejected(t *testing.T) {
	t.Parallel()

	env := setupTestSendEnv(t)
	schedule := env.createSchedule(t)
	handler := NewHandler(env.svc, &config.Config{})

	router := httpx.New()
	router.POST("/orgs/:org/report-schedules/:uid/test", handler.TestSend)

	// No caller in the context and no body: nothing to send to.
	req := httptest.NewRequestWithContext(
		context.Background(), http.MethodPost,
		"/orgs/"+env.org.Slug+"/report-schedules/"+schedule.UID+"/test", nil,
	)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusUnprocessableEntity, rec.Code)
	require.Empty(t, env.enqueuer.queued())
}
