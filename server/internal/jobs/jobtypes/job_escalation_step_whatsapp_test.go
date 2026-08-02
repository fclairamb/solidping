package jobtypes

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
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
	r.Len(components, 1)

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
