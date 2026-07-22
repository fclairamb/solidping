package jobtypes

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/app/services"
	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/email"
	"github.com/fclairamb/solidping/server/internal/entitlements"
	"github.com/fclairamb/solidping/server/internal/integrations/twilio"
	"github.com/fclairamb/solidping/server/internal/jobs/jobdef"
)

// fakeTwilio records every SMS/call the dispatcher posts, keyed by request path.
type fakeTwilio struct {
	mu    sync.Mutex
	forms []url.Values
	paths []string
}

func newFakeTwilio(t *testing.T) (*fakeTwilio, *httptest.Server) {
	t.Helper()

	f := &fakeTwilio{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		f.mu.Lock()
		f.forms = append(f.forms, r.PostForm)
		f.paths = append(f.paths, r.URL.Path)
		f.mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"sid":"SID_test","status":"queued"}`))
	}))
	t.Cleanup(srv.Close)

	return f, srv
}

func (f *fakeTwilio) countBySuffix(suffix string) int {
	f.mu.Lock()
	defer f.mu.Unlock()

	n := 0
	for _, p := range f.paths {
		if len(p) >= len(suffix) && p[len(p)-len(suffix):] == suffix {
			n++
		}
	}

	return n
}

func (f *fakeTwilio) smsCount() int  { return f.countBySuffix("Messages.json") }
func (f *fakeTwilio) callCount() int { return f.countBySuffix("Calls.json") }

// useFakeTwilio points the dispatch client seam at the fake server for the
// duration of the test.
func useFakeTwilio(t *testing.T, srv *httptest.Server) {
	t.Helper()

	prev := newTwilioClient
	newTwilioClient = func(accountSID, authToken string) *twilio.Client {
		return twilio.NewClientWithBaseURL(accountSID, authToken, srv.URL)
	}
	t.Cleanup(func() { newTwilioClient = prev })
}

// recordingEmail counts SendMessage calls so tests can assert email
// suppression under severity filters.
type recordingEmail struct {
	mu    sync.Mutex
	count int
}

func (m *recordingEmail) Send(_ context.Context, _ *email.Message) (*email.SendResult, error) {
	m.mu.Lock()
	m.count++
	m.mu.Unlock()

	return &email.SendResult{Sent: true, MessageID: "mock"}, nil
}

func (m *recordingEmail) sends() int {
	m.mu.Lock()
	defer m.mu.Unlock()

	return m.count
}

// phoneTestEnv seeds a SQLite DB with an org/check/incident and (optionally) a
// default Twilio integration, returning a job context wired for dispatch.
type phoneTestEnv struct {
	db       *sqlite.Service
	org      *models.Organization
	incident *models.Incident
	jctx     *jobdef.JobContext
	emails   *recordingEmail
}

func setupPhoneEnv(t *testing.T, withTwilio bool, voiceFrom string) *phoneTestEnv {
	t.Helper()

	ctx := context.Background()

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	require.NoError(t, err)
	require.NoError(t, dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	org := models.NewOrganization("test-org", "Test Org")
	require.NoError(t, dbSvc.CreateOrganization(ctx, org))

	checkName := "API health"
	check := &models.Check{UID: "check-1", OrganizationUID: org.UID, Name: &checkName, Type: "http"}
	require.NoError(t, dbSvc.CreateCheck(ctx, check))

	incident := &models.Incident{
		UID:             "incident-1",
		OrganizationUID: org.UID,
		CheckUID:        check.UID,
		State:           models.IncidentStateActive,
		StartedAt:       time.Now().Add(-5 * time.Minute),
	}
	require.NoError(t, dbSvc.CreateIncident(ctx, incident))

	if withTwilio {
		integration := models.NewIntegration(org.UID, models.ConnectionTypeTwilio, "twilio")
		integration.Enabled = true
		integration.IsDefault = true
		integration.Settings = models.JSONMap{
			"account_sid": "AC00000000000000000000000000000001",
			"auth_token":  "tok",
			"from_number": "+15559990000",
		}
		if voiceFrom != "" {
			integration.Settings["voice_from_number"] = voiceFrom
		}
		require.NoError(t, dbSvc.CreateChannel(ctx, integration))
	}

	cfg := &config.Config{}
	cfg.Auth.JWTSecret = "test-secret"
	cfg.Server.BaseURL = "https://app.example.com"

	emails := &recordingEmail{}
	jctx := &jobdef.JobContext{
		DBService: dbSvc,
		Logger:    slog.Default(),
		AppConfig: cfg,
		Services:  &services.Registry{EmailSender: emails},
	}

	return &phoneTestEnv{db: dbSvc, org: org, incident: incident, jctx: jctx, emails: emails}
}

func verifiedPhoneRoute(orgUID string) *models.UserNotificationRoute {
	now := time.Now()

	return &models.UserNotificationRoute{
		UID:     "route-phone",
		UserUID: "user-1",
		Contact: &models.UserContact{
			UID:             "contact-phone",
			OrganizationUID: orgUID,
			UserUID:         "user-1",
			Type:            models.UserContactTypePhone,
			Value:           "+15551230000",
			VerifiedAt:      &now,
		},
	}
}

func emailRoute() *models.UserNotificationRoute {
	return &models.UserNotificationRoute{
		UID:     "route-email",
		UserUID: "user-1",
		Contact: &models.UserContact{
			UID:   "contact-email",
			Type:  models.UserContactTypeEmail,
			Value: "oncall@example.com",
		},
	}
}

func newRun() *EscalationStepJobRun {
	return &EscalationStepJobRun{config: EscalationStepJobConfig{IncidentUID: "incident-1", StepUID: "step-1"}}
}

// TestDispatch_SMSFilterPagesPhoneSkipsEmail proves severity {sms} texts a
// verified phone and does NOT email.
//
//nolint:paralleltest // mutates the package-level newTwilioClient seam.
func TestDispatch_SMSFilterPagesPhoneSkipsEmail(t *testing.T) {
	r := require.New(t)
	ctx := context.Background()

	env := setupPhoneEnv(t, true, "")
	fake, srv := newFakeTwilio(t)
	useFakeTwilio(t, srv)

	run := newRun()
	filter := map[string]bool{"sms": true}

	phoneSent := run.dispatchRoute(ctx, env.jctx, slog.Default(), env.incident, verifiedPhoneRoute(env.org.UID), filter)
	emailSent := run.dispatchRoute(ctx, env.jctx, slog.Default(), env.incident, emailRoute(), filter)

	r.Equal(1, phoneSent, "phone must be paged under {sms}")
	r.Equal(0, emailSent, "email must be skipped under {sms}")
	r.Equal(1, fake.smsCount(), "exactly one SMS")
	r.Equal(0, fake.callCount(), "no call under {sms}")
	r.Equal(0, env.emails.sends(), "no email sent under {sms}")
}

// TestDispatch_EmailSlackBehavesAsToday is the backward-compat control:
// severity {email, slack} emails and never touches the phone.
//
//nolint:paralleltest // mutates the package-level newTwilioClient seam.
func TestDispatch_EmailSlackBehavesAsToday(t *testing.T) {
	r := require.New(t)
	ctx := context.Background()

	env := setupPhoneEnv(t, true, "")
	fake, srv := newFakeTwilio(t)
	useFakeTwilio(t, srv)

	run := newRun()
	filter := map[string]bool{"email": true, "slack": true}

	emailSent := run.dispatchRoute(ctx, env.jctx, slog.Default(), env.incident, emailRoute(), filter)
	phoneSent := run.dispatchRoute(ctx, env.jctx, slog.Default(), env.incident, verifiedPhoneRoute(env.org.UID), filter)

	r.Equal(1, emailSent, "email fires under {email, slack} exactly as before")
	r.Equal(1, env.emails.sends())
	r.Equal(0, phoneSent, "phone is never paged under {email, slack}")
	r.Equal(0, fake.smsCount(), "no SMS under {email, slack}")
	r.Equal(0, fake.callCount(), "no call under {email, slack}")
}

// TestDispatch_VoicePlacesCallWithoutSMS proves severity {voice} calls but
// does not text.
//
//nolint:paralleltest // mutates the package-level newTwilioClient seam.
func TestDispatch_VoicePlacesCallWithoutSMS(t *testing.T) {
	r := require.New(t)
	ctx := context.Background()

	env := setupPhoneEnv(t, true, "+15559990001")
	fake, srv := newFakeTwilio(t)
	useFakeTwilio(t, srv)

	run := newRun()
	filter := map[string]bool{"voice": true}

	sent := run.dispatchRoute(ctx, env.jctx, slog.Default(), env.incident, verifiedPhoneRoute(env.org.UID), filter)

	r.Equal(1, sent)
	r.Equal(1, fake.callCount(), "a call is placed under {voice}")
	r.Equal(0, fake.smsCount(), "no SMS under {voice}")
}

// TestDispatch_UnverifiedPhoneNeverContacted proves an unverified number is
// never texted or dialed.
//
//nolint:paralleltest // mutates the package-level newTwilioClient seam.
func TestDispatch_UnverifiedPhoneNeverContacted(t *testing.T) {
	r := require.New(t)
	ctx := context.Background()

	env := setupPhoneEnv(t, true, "+15559990001")
	fake, srv := newFakeTwilio(t)
	useFakeTwilio(t, srv)

	route := verifiedPhoneRoute(env.org.UID)
	route.Contact.VerifiedAt = nil // unverified

	run := newRun()
	sent := run.dispatchRoute(ctx, env.jctx, slog.Default(), env.incident, route, map[string]bool{"sms": true, "voice": true})

	r.Equal(0, sent)
	r.Equal(0, fake.smsCount())
	r.Equal(0, fake.callCount())
}

// TestDispatch_MissingProviderDegradesToSkip proves that with no Twilio
// connection the phone route quietly returns 0 (no panic, no error).
//
//nolint:paralleltest // mutates the package-level newTwilioClient seam.
func TestDispatch_MissingProviderDegradesToSkip(t *testing.T) {
	r := require.New(t)
	ctx := context.Background()

	env := setupPhoneEnv(t, false, "") // no twilio integration
	fake, srv := newFakeTwilio(t)
	useFakeTwilio(t, srv)

	run := newRun()
	sent := run.dispatchRoute(ctx, env.jctx, slog.Default(), env.incident, verifiedPhoneRoute(env.org.UID), map[string]bool{"sms": true})

	r.Equal(0, sent)
	r.Equal(0, fake.smsCount())
}

// TestDispatch_SMSQuotaExhaustedSkips proves an exhausted monthly SMS cap skips
// the send and records a quota-skip audit row, without failing the step.
//
//nolint:paralleltest // mutates the package-level newTwilioClient seam.
func TestDispatch_SMSQuotaExhaustedSkips(t *testing.T) {
	r := require.New(t)
	ctx := context.Background()

	env := setupPhoneEnv(t, true, "")
	fake, srv := newFakeTwilio(t)
	useFakeTwilio(t, srv)

	// Wire an entitlements service capped at 0 SMS/month.
	entSvc := entitlements.NewService(env.db, entitlements.DefaultsFor(config.DeploymentModeSelfHosted), 0)
	r.NoError(entSvc.Set(ctx, env.org.UID, entitlements.Entitlements{
		Limits: entitlements.Limits{MaxSmsPerMonth: entitlements.Int(0)},
		Source: models.EntitlementSourceAdmin,
	}, "tester", "no sms"))
	env.jctx.Services.Entitlements = entSvc

	run := newRun()
	sent := run.dispatchRoute(ctx, env.jctx, slog.Default(), env.incident, verifiedPhoneRoute(env.org.UID), map[string]bool{"sms": true})

	r.Equal(0, sent, "quota-exhausted SMS must not count as sent")
	r.Equal(0, fake.smsCount(), "no SMS is dispatched when the quota is exhausted")
}

// TestSeverityHelpers pins the person-target token semantics that guarantee
// backward compatibility for the historical email/slack/webpush routes.
func TestSeverityHelpers(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	// nil filter = everything.
	r.True(severityAllowsPersonTargets(nil))
	r.True(severityAllowsEmail(nil))
	r.True(severityAllowsWebPush(nil))
	r.True(severityAllowsSMS(nil))
	r.False(severityAllowsVoice(nil), "voice never fires on a nil filter")

	// {email, slack}: email + webpush ride email; sms/voice do not.
	es := map[string]bool{"email": true, "slack": true}
	r.True(severityAllowsPersonTargets(es))
	r.True(severityAllowsEmail(es))
	r.True(severityAllowsWebPush(es), "webpush historically rides the email token")
	r.False(severityAllowsSMS(es))
	r.False(severityAllowsVoice(es))

	// {sms}: person targets open, but email/webpush are excluded.
	sms := map[string]bool{"sms": true}
	r.True(severityAllowsPersonTargets(sms))
	r.False(severityAllowsEmail(sms))
	r.False(severityAllowsWebPush(sms))
	r.True(severityAllowsSMS(sms))
	r.False(severityAllowsVoice(sms))

	// {slack} alone excludes all person channels (email token absent).
	slackOnly := map[string]bool{"slack": true}
	r.False(severityAllowsPersonTargets(slackOnly))

	// explicit push token enables webpush.
	r.True(severityAllowsWebPush(map[string]bool{"push": true}))
	r.True(severityAllowsWebPush(map[string]bool{"critical_push": true}))
}
