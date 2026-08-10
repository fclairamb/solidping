package jobtypes

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/entitlements"
)

// fakeGraphAPI stands in for graph.facebook.com. It is created per test and
// wired in through the *config* (cfg.WhatsApp.BaseURL) rather than a package
// global, so these tests can run in parallel without racing each other.
type fakeGraphAPI struct {
	mu       sync.Mutex
	requests []map[string]any
	status   int
	response string
}

func newFakeGraphAPI(t *testing.T, status int, response string) (*fakeGraphAPI, string) {
	t.Helper()

	fake := &fakeGraphAPI{status: status, response: response}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(req.Body).Decode(&body)

		fake.mu.Lock()
		fake.requests = append(fake.requests, body)
		fake.mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(fake.status)
		_, _ = w.Write([]byte(fake.response))
	}))
	t.Cleanup(srv.Close)

	return fake, srv.URL
}

func (f *fakeGraphAPI) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()

	return len(f.requests)
}

func (f *fakeGraphAPI) last() map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()

	if len(f.requests) == 0 {
		return nil
	}

	return f.requests[len(f.requests)-1]
}

// enableWhatsApp points the instance config at the fake Graph API.
func enableWhatsApp(env *phoneTestEnv, baseURL string) {
	env.jctx.AppConfig.WhatsApp = config.WhatsAppConfig{
		Enabled:          true,
		AccessToken:      "system-user-token",
		PhoneNumberID:    "PNID",
		APIVersion:       "v23.0",
		AlertTemplate:    "solidping_alert",
		TemplateLanguage: "en",
		BaseURL:          baseURL,
	}
}

func whatsAppRoute(orgUID string, verified bool) *models.UserNotificationRoute {
	contact := &models.UserContact{
		UID:             "contact-whatsapp",
		OrganizationUID: orgUID,
		UserUID:         "user-1",
		Type:            models.UserContactTypeWhatsApp,
		Value:           "+15551230000",
	}

	if verified {
		now := time.Now()
		contact.VerifiedAt = &now
	}

	return &models.UserNotificationRoute{UID: "route-whatsapp", UserUID: "user-1", Contact: contact}
}

const graphOK = `{"messaging_product":"whatsapp","messages":[{"id":"wamid.SENT"}]}`

// TestDispatch_WhatsAppFilterSendsAndSkipsEmail proves severity {whatsapp}
// messages a verified WhatsApp contact and does not email.
func TestDispatch_WhatsAppFilterSendsAndSkipsEmail(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := context.Background()

	env := setupPhoneEnv(t, false, "")
	fake, baseURL := newFakeGraphAPI(t, http.StatusOK, graphOK)
	enableWhatsApp(env, baseURL)

	run := newRun()
	filter := map[string]bool{"whatsapp": true}

	sent := run.dispatchRoute(ctx, env.jctx, slog.Default(), env.incident, whatsAppRoute(env.org.UID, true), filter)
	emailSent := run.dispatchRoute(ctx, env.jctx, slog.Default(), env.incident, emailRoute(), filter)

	r.Equal(1, sent)
	r.Equal(0, emailSent, "email must be skipped under {whatsapp}")
	r.Equal(1, fake.count())
	r.Equal(0, env.emails.sends())

	body := fake.last()
	r.Equal("template", body["type"])
	r.Equal("15551230000", body["to"])

	tmpl, ok := body["template"].(map[string]any)
	r.True(ok)
	r.Equal("solidping_alert", tmpl["name"])

	components, ok := tmpl["components"].([]any)
	r.True(ok)
	r.Len(components, 2, "body variables + the check-link button")

	bodyComp, ok := components[0].(map[string]any)
	r.True(ok)

	params, ok := bodyComp["parameters"].([]any)
	r.True(ok)
	r.Len(params, 4, "check name, state, detail, org")

	first, ok := params[0].(map[string]any)
	r.True(ok)
	r.Equal("API health", first["text"])

	state, ok := params[1].(map[string]any)
	r.True(ok)
	r.Equal("DOWN", state["text"])

	org, ok := params[3].(map[string]any)
	r.True(ok)
	r.Equal(env.org.Slug, org["text"])
}

// TestDispatch_WhatsAppNotSentWithoutExplicitToken is the safety control: a nil
// (severity-less) filter and an sms/email-only severity must never message
// WhatsApp, mirroring the voice rule.
func TestDispatch_WhatsAppNotSentWithoutExplicitToken(t *testing.T) {
	t.Parallel()

	for name, filter := range map[string]map[string]bool{
		"nil filter":      nil,
		"email and sms":   {"email": true, "sms": true},
		"voice only":      {"voice": true},
		"critical push":   {"critical_push": true},
		"unrelated token": {"slack": true},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			r := require.New(t)
			ctx := context.Background()

			env := setupPhoneEnv(t, false, "")
			fake, baseURL := newFakeGraphAPI(t, http.StatusOK, graphOK)
			enableWhatsApp(env, baseURL)

			sent := newRun().dispatchRoute(
				ctx, env.jctx, slog.Default(), env.incident, whatsAppRoute(env.org.UID, true), filter,
			)

			r.Equal(0, sent)
			r.Equal(0, fake.count(), "no WhatsApp message may be sent without an explicit token")
		})
	}
}

// TestDispatch_WhatsAppUnverifiedNeverMessaged proves the opt-in gate: an
// unverified contact is never messaged, because verification IS Meta's
// required record of consent.
func TestDispatch_WhatsAppUnverifiedNeverMessaged(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := context.Background()

	env := setupPhoneEnv(t, false, "")
	fake, baseURL := newFakeGraphAPI(t, http.StatusOK, graphOK)
	enableWhatsApp(env, baseURL)

	sent := newRun().dispatchRoute(
		ctx, env.jctx, slog.Default(), env.incident,
		whatsAppRoute(env.org.UID, false), map[string]bool{"whatsapp": true},
	)

	r.Equal(0, sent)
	r.Equal(0, fake.count())
}

// TestDispatch_WhatsAppDisabledInstanceSkips proves an instance with WhatsApp
// off degrades to a skip rather than failing the escalation step.
func TestDispatch_WhatsAppDisabledInstanceSkips(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := context.Background()

	env := setupPhoneEnv(t, false, "")
	// WhatsApp deliberately left unconfigured.
	sent := newRun().dispatchRoute(
		ctx, env.jctx, slog.Default(), env.incident,
		whatsAppRoute(env.org.UID, true), map[string]bool{"whatsapp": true},
	)

	r.Equal(0, sent)
}

// TestDispatch_WhatsAppSendFailureFallsThrough proves every typed failure class
// returns 0 (so the escalation step advances to the next step) rather than
// panicking or reporting a phantom delivery.
func TestDispatch_WhatsAppSendFailureFallsThrough(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		status   int
		response string
	}{
		{"template paused", http.StatusBadRequest, `{"error":{"message":"Template is paused","code":132015}}`},
		{"template missing", http.StatusBadRequest, `{"error":{"message":"no such template","code":132001}}`},
		{"recipient not on whatsapp", http.StatusBadRequest, `{"error":{"message":"undeliverable","code":131026}}`},
		{"tier cap", http.StatusBadRequest, `{"error":{"message":"rate limit","code":130429}}`},
		{"token expired", http.StatusUnauthorized, `{"error":{"message":"bad token","code":190}}`},
		{"server error", http.StatusInternalServerError, `{"error":{"message":"boom","code":1}}`},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			r := require.New(t)
			ctx := context.Background()

			env := setupPhoneEnv(t, false, "")
			fake, baseURL := newFakeGraphAPI(t, tc.status, tc.response)
			enableWhatsApp(env, baseURL)

			sent := newRun().dispatchRoute(
				ctx, env.jctx, slog.Default(), env.incident,
				whatsAppRoute(env.org.UID, true), map[string]bool{"whatsapp": true},
			)

			r.Equal(0, sent, "a failed send must not be reported as delivered")
			r.Equal(1, fake.count(), "the send was attempted")
		})
	}
}

// TestDispatch_WhatsAppQuotaBlocksAtCap proves the monthly entitlement cap
// stops the send before it reaches Meta, and that a positive control with a
// non-zero cap does send and increments the persistent counter.
func TestDispatch_WhatsAppQuotaBlocksAtCap(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := context.Background()

	env := setupPhoneEnv(t, false, "")
	fake, baseURL := newFakeGraphAPI(t, http.StatusOK, graphOK)
	enableWhatsApp(env, baseURL)

	entSvc := entitlements.NewService(env.db, entitlements.DefaultsFor(config.DeploymentModeSelfHosted), 0)
	r.NoError(entSvc.Set(ctx, env.org.UID, entitlements.Entitlements{
		Limits: entitlements.Limits{MaxWhatsappPerMonth: entitlements.Int(0)},
		Source: models.EntitlementSourceAdmin,
	}, "tester", "no whatsapp"))
	env.jctx.Services.Entitlements = entSvc

	sent := newRun().dispatchRoute(
		ctx, env.jctx, slog.Default(), env.incident,
		whatsAppRoute(env.org.UID, true), map[string]bool{"whatsapp": true},
	)

	r.Equal(0, sent, "quota-exhausted WhatsApp must not count as sent")
	r.Equal(0, fake.count(), "no request may reach Meta once the quota is exhausted")
}

// TestDispatch_WhatsAppQuotaIncrementsCounter is the positive control for the
// test above: with a cap of 2, two sends succeed, the third is refused, and the
// persistent monthly counter reflects exactly two.
func TestDispatch_WhatsAppQuotaIncrementsCounter(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := context.Background()

	env := setupPhoneEnv(t, false, "")
	fake, baseURL := newFakeGraphAPI(t, http.StatusOK, graphOK)
	enableWhatsApp(env, baseURL)

	entSvc := entitlements.NewService(env.db, entitlements.DefaultsFor(config.DeploymentModeSelfHosted), 0)
	r.NoError(entSvc.Set(ctx, env.org.UID, entitlements.Entitlements{
		Limits: entitlements.Limits{MaxWhatsappPerMonth: entitlements.Int(2)},
		Source: models.EntitlementSourceAdmin,
	}, "tester", "two per month"))
	env.jctx.Services.Entitlements = entSvc

	filter := map[string]bool{"whatsapp": true}

	// Each dispatch needs its own run: within one run the per-number dedupe
	// would collapse the repeats.
	total := 0
	for range 3 {
		total += newRun().dispatchRoute(
			ctx, env.jctx, slog.Default(), env.incident, whatsAppRoute(env.org.UID, true), filter,
		)
	}

	r.Equal(2, total, "exactly the capped number of sends succeed")
	r.Equal(2, fake.count())

	usage, err := entSvc.Usage(ctx, env.org.UID)
	r.NoError(err)
	r.Equal(2, usage.WhatsappThisMonth, "the persistent monthly counter tracks sends")
}

// TestDispatch_WhatsAppDedupesWithinRun proves a user matched twice in one job
// run (e.g. via both `user` and `all_admins`) is messaged once.
func TestDispatch_WhatsAppDedupesWithinRun(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := context.Background()

	env := setupPhoneEnv(t, false, "")
	fake, baseURL := newFakeGraphAPI(t, http.StatusOK, graphOK)
	enableWhatsApp(env, baseURL)

	run := newRun()
	filter := map[string]bool{"whatsapp": true}

	first := run.dispatchRoute(ctx, env.jctx, slog.Default(), env.incident, whatsAppRoute(env.org.UID, true), filter)
	second := run.dispatchRoute(ctx, env.jctx, slog.Default(), env.incident, whatsAppRoute(env.org.UID, true), filter)

	r.Equal(1, first)
	r.Equal(0, second)
	r.Equal(1, fake.count())
}

// TestWhatsAppStateLabel proves one template covers all three states via the
// state variable.
func TestWhatsAppStateLabel(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	now := time.Now()

	r.Equal("DOWN", whatsAppStateLabel(&models.Incident{State: models.IncidentStateActive}))
	r.Equal("ESCALATED", whatsAppStateLabel(&models.Incident{
		State: models.IncidentStateActive, EscalatedAt: &now,
	}))
	r.Equal("RESOLVED", whatsAppStateLabel(&models.Incident{
		State: models.IncidentStateResolved, EscalatedAt: &now,
	}))
}

// TestWhatsAppDetail proves the detail variable is single-line and capped —
// Meta rejects oversized or multi-line template parameters.
func TestWhatsAppDetail(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	run := newRun()

	title := "  connection\nrefused\tafter 3 tries  "
	detail := run.whatsAppDetail(context.Background(), nil, &models.Incident{
		Title: &title, StartedAt: time.Now(),
	})
	r.Equal("connection refused after 3 tries", detail)

	long := strings.Repeat("abcdefghij", 40)

	detail = run.whatsAppDetail(context.Background(), nil, &models.Incident{
		Title: &long, StartedAt: time.Now(),
	})
	r.LessOrEqual(len(detail), whatsAppDetailCap+3) // +3 for the multi-byte ellipsis

	// No title falls back to an age summary rather than an empty variable
	// (Meta rejects empty template parameters).
	detail = run.whatsAppDetail(context.Background(), nil, &models.Incident{
		StartedAt: time.Now().Add(-5 * time.Minute),
	})
	r.NotEmpty(detail)
	r.Contains(detail, "open for")
}

// whatsAppButtonComponent returns the button component of the last recorded
// Graph request, or nil when the payload carries no button at all.
func whatsAppButtonComponent(t *testing.T, body map[string]any) map[string]any {
	t.Helper()

	tmpl, ok := body["template"].(map[string]any)
	require.True(t, ok)

	components, ok := tmpl["components"].([]any)
	require.True(t, ok)

	for _, raw := range components {
		comp, isMap := raw.(map[string]any)
		if isMap && comp["type"] == "button" {
			return comp
		}
	}

	return nil
}

// TestDispatch_WhatsAppAlertCarriesCheckLinkButton proves the alert is no longer
// a dead end: the send carries the URL-button variable resolving to the check's
// dashboard page, alongside the four body variables.
func TestDispatch_WhatsAppAlertCarriesCheckLinkButton(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := context.Background()

	env := setupPhoneEnv(t, false, "")
	fake, baseURL := newFakeGraphAPI(t, http.StatusOK, graphOK)
	enableWhatsApp(env, baseURL)

	sent := newRun().dispatchRoute(
		ctx, env.jctx, slog.Default(), env.incident,
		whatsAppRoute(env.org.UID, true), map[string]bool{"whatsapp": true},
	)
	r.Equal(1, sent)
	r.Equal(1, fake.count())

	button := whatsAppButtonComponent(t, fake.last())
	r.NotNil(button, "the alert must carry a URL button")
	r.Equal("url", button["sub_type"])
	r.Equal("0", button["index"])

	params, ok := button["parameters"].([]any)
	r.True(ok)
	r.Len(params, 1)

	param, ok := params[0].(map[string]any)
	r.True(ok)
	r.Equal("text", param["type"])
	// Appended by Meta to the template's static `{baseURL}/dash0/` prefix, so
	// this resolves to /dash0/orgs/test-org/checks/check-1.
	r.Equal("orgs/"+env.org.Slug+"/checks/"+env.incident.CheckUID, param["text"])
}

// TestDispatch_WhatsAppSendsWithoutButtonWhenSlugUnresolvable proves the
// fallback: an org whose slug cannot be loaded still gets paged, with NO button
// component at all. A link to the wrong place is worse than no link.
func TestDispatch_WhatsAppSendsWithoutButtonWhenSlugUnresolvable(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := context.Background()

	env := setupPhoneEnv(t, false, "")
	fake, baseURL := newFakeGraphAPI(t, http.StatusOK, graphOK)
	enableWhatsApp(env, baseURL)

	orphan := *env.incident
	orphan.OrganizationUID = "org-does-not-exist"

	sent := newRun().dispatchRoute(
		ctx, env.jctx, slog.Default(), &orphan,
		whatsAppRoute(orphan.OrganizationUID, true), map[string]bool{"whatsapp": true},
	)

	r.Equal(1, sent, "the page must still go out")
	r.Equal(1, fake.count())
	r.Nil(whatsAppButtonComponent(t, fake.last()),
		"no button component may be emitted when the link cannot be built")
}

// TestWhatsAppCheckButtonParam covers suffix construction directly: the happy
// path, both unresolvable inputs, defensive escaping, and the length cap.
func TestWhatsAppCheckButtonParam(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	r.Equal("orgs/acme/checks/check-1", whatsAppCheckButtonParam("acme", "check-1"))

	// Unresolvable inputs drop the button rather than produce a broken link.
	r.Empty(whatsAppCheckButtonParam("", "check-1"))
	r.Empty(whatsAppCheckButtonParam("acme", ""))
	r.Empty(whatsAppCheckButtonParam("  ", "check-1"))

	// Defensive escaping: a slug or UID that smuggled a separator would
	// otherwise silently retarget the button.
	escaped := whatsAppCheckButtonParam("ac me/../evil", "check-1")
	r.NotContains(strings.TrimPrefix(escaped, "orgs/"), "/../")
	r.Equal("orgs/ac%20me%2F..%2Fevil/checks/check-1", escaped)

	// The cap bounds the parameter so Meta can never reject the whole send.
	capped := whatsAppCheckButtonParam(strings.Repeat("a", 300), strings.Repeat("b", 300))
	r.LessOrEqual(len(capped), whatsAppButtonParamCap)
	r.NotEmpty(capped)

	// Truncation never leaves a dangling percent-escape behind.
	dangling := whatsAppCheckButtonParam(strings.Repeat("é", 200), "check-1")
	r.LessOrEqual(len(dangling), whatsAppButtonParamCap)
	unescaped, err := url.PathUnescape(dangling)
	r.NoError(err, "a truncated suffix must stay a valid URL path")
	r.NotEmpty(unescaped)
}

// TestDispatch_WhatsAppButtonSendRecordsSentAudit proves the audit trail
// survives the new button: the delivery history still shows status=sent with
// Meta's wamid, which is what inbound delivery webhooks key on.
func TestDispatch_WhatsAppButtonSendRecordsSentAudit(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := context.Background()

	env := setupPhoneEnv(t, false, "")
	fake, baseURL := newFakeGraphAPI(t, http.StatusOK, graphOK)
	enableWhatsApp(env, baseURL)

	// The audit row carries real foreign keys (user + escalation step), so both
	// must exist for the insert to land at all.
	user := models.NewUser("oncall@example.com")
	r.NoError(env.db.CreateUser(ctx, user))

	policy := models.NewEscalationPolicy(env.org.UID, "primary")
	r.NoError(env.db.CreateEscalationPolicy(ctx, policy))

	step := models.NewEscalationPolicyStep(policy.UID, 0, 0)
	r.NoError(env.db.ReplaceEscalationPolicySteps(ctx, policy.UID,
		[]*models.EscalationPolicyStep{step}, nil))

	route := whatsAppRoute(env.org.UID, true)
	route.UserUID = user.UID
	route.Contact.UserUID = user.UID

	run := &EscalationStepJobRun{config: EscalationStepJobConfig{
		IncidentUID: env.incident.UID, StepUID: step.UID,
	}}

	sent := run.dispatchRoute(
		ctx, env.jctx, slog.Default(), env.incident, route, map[string]bool{"whatsapp": true},
	)
	r.Equal(1, sent)
	r.NotNil(whatsAppButtonComponent(t, fake.last()), "this run must be the button-carrying one")

	rows, err := env.db.ListIncidentNotifications(ctx, env.org.UID, db.ListIncidentNotificationsFilter{
		IncidentUID: env.incident.UID,
	})
	r.NoError(err)
	r.Len(rows, 1)

	row := rows[0]
	r.Equal(models.IncidentNotificationStatusSent, row.Status)
	r.Equal(models.UsageCounterKindWhatsApp, row.ChannelType)
	r.NotNil(row.MessageID)
	r.Equal("wamid.SENT", *row.MessageID)
	r.NotNil(row.SentAt)
}

// TestSeverityAllowsPersonTargets_WhatsApp closes the gap that would make every
// other WhatsApp test vacuous: fanOutWithSeverity gates user / schedule /
// all_admins targets on severityAllowsPersonTargets BEFORE dispatchRoute is
// ever reached. A `{whatsapp}`-only severity that failed this gate would page
// nobody, no matter how correct pageWhatsApp is.
func TestSeverityAllowsPersonTargets_WhatsApp(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	r.True(severityAllowsPersonTargets(map[string]bool{"whatsapp": true}),
		"a whatsapp-only severity must open the person-target gate")

	// Negative control: a severity with no person-deliverable token stays shut.
	r.False(severityAllowsPersonTargets(map[string]bool{"slack": true}))

	// And the per-route gate itself is explicit-token-only.
	r.True(severityAllowsWhatsApp(map[string]bool{"whatsapp": true}))
	r.False(severityAllowsWhatsApp(nil))
	r.False(severityAllowsWhatsApp(map[string]bool{"email": true, "sms": true}))
}
