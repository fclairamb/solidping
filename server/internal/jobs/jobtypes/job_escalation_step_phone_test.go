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
// duration of the test, ignoring the resolved region base URL — the fake
// server doesn't care what region it was "meant" for.
func useFakeTwilio(t *testing.T, srv *httptest.Server) {
	t.Helper()

	prev := newTwilioClient
	newTwilioClient = func(accountSID, authToken, _ string) *twilio.Client {
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
	filter := map[string]bool{"sms": true, "voice": true}
	sent := run.dispatchRoute(ctx, env.jctx, slog.Default(), env.incident, route, filter)

	r.Equal(0, sent)
	r.Equal(0, fake.smsCount())
	r.Equal(0, fake.callCount())
}

// TestDispatch_RegionResolvesRegionalBase proves the escalation SMS and voice
// paths resolve the connection's region to the matching Twilio host. This
// pins the negative that motivated the spec: before the region fix, both
// paths silently pinned to twilio.DefaultBaseURL regardless of the
// connection's configured region — a test covering only the notifications
// sender would have passed the old, broken code for these two paths.
//
//nolint:paralleltest // mutates the package-level newTwilioClient seam.
func TestDispatch_RegionResolvesRegionalBase(t *testing.T) {
	r := require.New(t)
	ctx := context.Background()

	env := setupPhoneEnv(t, true, "+15559990001")

	channels, err := env.db.ListChannels(ctx, &models.ListIntegrationsFilter{OrganizationUID: env.org.UID})
	r.NoError(err)
	r.Len(channels, 1)
	conn := channels[0]
	conn.Settings["region"] = "ie1"
	r.NoError(env.db.UpdateChannel(ctx, conn.UID, &models.IntegrationUpdate{Settings: &conn.Settings}))

	fake, srv := newFakeTwilio(t)

	var gotBaseURLs []string
	prev := newTwilioClient
	newTwilioClient = func(accountSID, authToken, baseURL string) *twilio.Client {
		gotBaseURLs = append(gotBaseURLs, baseURL)
		// Redirect to the fake server so the send still "succeeds" — only the
		// resolved base URL argument is under test here.
		return twilio.NewClientWithBaseURL(accountSID, authToken, srv.URL)
	}
	t.Cleanup(func() { newTwilioClient = prev })

	run := newRun()
	filter := map[string]bool{"sms": true, "voice": true}

	sent := run.dispatchRoute(ctx, env.jctx, slog.Default(), env.incident, verifiedPhoneRoute(env.org.UID), filter)

	r.Equal(2, sent, "sms + voice both fire")
	r.Equal(1, fake.smsCount())
	r.Equal(1, fake.callCount())
	r.Len(gotBaseURLs, 2, "both the SMS and the voice call must resolve a base URL")

	for _, u := range gotBaseURLs {
		r.Equal("https://api.ie1.twilio.com", u,
			"the escalation dispatcher must resolve the connection's region, not the default base")
	}
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
	filter := map[string]bool{"sms": true}
	sent := run.dispatchRoute(ctx, env.jctx, slog.Default(), env.incident, verifiedPhoneRoute(env.org.UID), filter)

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
	filter := map[string]bool{"sms": true}
	sent := run.dispatchRoute(ctx, env.jctx, slog.Default(), env.incident, verifiedPhoneRoute(env.org.UID), filter)

	r.Equal(0, sent, "quota-exhausted SMS must not count as sent")
	r.Equal(0, fake.smsCount(), "no SMS is dispatched when the quota is exhausted")
}

// seedAdminWithPhoneRoute creates a user, makes them an org admin, and gives
// them a verified phone contact + enabled route in the DB.
func seedAdminWithPhoneRoute(t *testing.T, env *phoneTestEnv, number string) *models.User {
	t.Helper()
	ctx := context.Background()

	user := models.NewUser("oncall@example.com")
	require.NoError(t, env.db.CreateUser(ctx, user))
	require.NoError(t, env.db.CreateOrganizationMember(ctx,
		models.NewOrganizationMember(env.org.UID, user.UID, models.MemberRoleAdmin)))

	now := time.Now()
	contact := models.NewUserContact(user.UID, env.org.UID, models.UserContactTypePhone, number, "mobile")
	contact.VerifiedAt = &now
	require.NoError(t, env.db.UpsertUserContact(ctx, contact))

	route := models.NewUserNotificationRoute(user.UID, env.org.UID, contact.UID, 0)
	_, err := env.db.DB().NewInsert().Model(route).Exec(ctx)
	require.NoError(t, err)

	return user
}

// seedPolicyWithPhoneStep creates a severity with the given channels, a policy,
// and a single step targeting the user. Returns the persisted step.
func seedPolicyWithPhoneStep(
	t *testing.T, env *phoneTestEnv, user *models.User, channels []string,
) *models.EscalationPolicyStep {
	t.Helper()
	ctx := context.Background()

	sev := models.NewSeverity(env.org.UID, "critical", "Critical", channels, false)
	require.NoError(t, env.db.CreateSeverity(ctx, sev))

	policy := models.NewEscalationPolicy(env.org.UID, "pol")
	require.NoError(t, env.db.CreateEscalationPolicy(ctx, policy))

	step := models.NewEscalationPolicyStep(policy.UID, 0, 0)
	step.SeverityUID = &sev.UID
	target := models.NewEscalationPolicyTarget(step.UID, models.EscalationTargetUser, &user.UID, 0)
	require.NoError(t, env.db.ReplaceEscalationPolicySteps(ctx, policy.UID,
		[]*models.EscalationPolicyStep{step},
		map[int][]*models.EscalationPolicyTarget{0: {target}}))

	return step
}

// TestDispatch_DedupsSamePhoneAcrossTargets proves the coalescing invariant:
// a user reachable via BOTH a `user` target and an `all_admins` target in one
// escalation-step run is texted once and called once, not twice.
//
//nolint:paralleltest // mutates the package-level newTwilioClient seam.
func TestDispatch_DedupsSamePhoneAcrossTargets(t *testing.T) {
	r := require.New(t)
	ctx := context.Background()

	env := setupPhoneEnv(t, true, "+15559990001") // voice enabled
	fake, srv := newFakeTwilio(t)
	useFakeTwilio(t, srv)

	user := seedAdminWithPhoneRoute(t, env, "+15551230000")

	targets := []*models.EscalationPolicyTarget{
		{UID: "t-user", TargetType: models.EscalationTargetUser, TargetUID: &user.UID},
		{UID: "t-admins", TargetType: models.EscalationTargetAllAdmins},
	}

	run := newRun()
	run.fanOutWithSeverity(ctx, env.jctx, slog.Default(), env.incident, targets,
		map[string]bool{"sms": true, "voice": true})

	r.Equal(1, fake.smsCount(), "same number via user + all_admins must be texted exactly once")
	r.Equal(1, fake.callCount(), "same number via user + all_admins must be called exactly once")
}

// TestRun_UnackedIncidentPagesPhone is the positive control for the ack guard:
// through the real Run() an OPEN incident pages the on-call phone.
//
//nolint:paralleltest // mutates the package-level newTwilioClient seam.
func TestRun_UnackedIncidentPagesPhone(t *testing.T) {
	r := require.New(t)
	ctx := context.Background()

	env := setupPhoneEnv(t, true, "+15559990001")
	fake, srv := newFakeTwilio(t)
	useFakeTwilio(t, srv)

	user := seedAdminWithPhoneRoute(t, env, "+15551230000")
	step := seedPolicyWithPhoneStep(t, env, user, []string{"sms", "voice"})

	run := &EscalationStepJobRun{config: EscalationStepJobConfig{
		IncidentUID: env.incident.UID, StepUID: step.UID,
	}}
	r.NoError(run.Run(ctx, env.jctx))

	r.Equal(1, fake.smsCount(), "an open incident must page the phone via SMS")
	r.Equal(1, fake.callCount(), "an open incident must page the phone via voice")
}

// TestRun_AckedIncidentSendsNothing proves the required dispatch semantic
// through the real Run() entrypoint (not just dispatchRoute): an acknowledged
// incident sends no SMS and places no call, even under a {sms, voice} severity.
//
//nolint:paralleltest // mutates the package-level newTwilioClient seam.
func TestRun_AckedIncidentSendsNothing(t *testing.T) {
	r := require.New(t)
	ctx := context.Background()

	env := setupPhoneEnv(t, true, "+15559990001")
	fake, srv := newFakeTwilio(t)
	useFakeTwilio(t, srv)

	user := seedAdminWithPhoneRoute(t, env, "+15551230000")
	step := seedPolicyWithPhoneStep(t, env, user, []string{"sms", "voice"})

	// Acknowledge the incident — the belt-and-braces guard in Run must skip it.
	ackedAt := time.Now()
	r.NoError(env.db.UpdateIncident(ctx, env.incident.UID, &models.IncidentUpdate{AcknowledgedAt: &ackedAt}))

	run := &EscalationStepJobRun{config: EscalationStepJobConfig{
		IncidentUID: env.incident.UID, StepUID: step.UID,
	}}
	r.NoError(run.Run(ctx, env.jctx))

	r.Equal(0, fake.smsCount(), "an acknowledged incident must not send SMS")
	r.Equal(0, fake.callCount(), "an acknowledged incident must not place a call")
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
