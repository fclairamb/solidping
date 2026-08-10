package jobtypes

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/entitlements"
	"github.com/fclairamb/solidping/server/internal/integrations/telegram"
)

// Bot API error payloads, hoisted so the table rows stay readable.
const (
	botErrBlocked      = `{"ok":false,"error_code":403,"description":"Forbidden: bot was blocked by the user"}`
	botErrDeactivated  = `{"ok":false,"error_code":403,"description":"Forbidden: user is deactivated"}`
	botErrChatNotFound = `{"ok":false,"error_code":400,"description":"Bad Request: chat not found"}`
	botErrUnauthorized = `{"ok":false,"error_code":401,"description":"Unauthorized"}`
	botErrRateLimited  = `{"ok":false,"error_code":429,"description":"Too Many",` +
		`"parameters":{"retry_after":1}}`
	botErrServer      = `{"ok":false,"error_code":500,"description":"Internal Server Error"}`
	botErrReplyGone   = `{"ok":false,"error_code":400,"description":"Bad Request: message to be replied not found"}`
	botErrEditMissing = `{"ok":false,"error_code":400,"description":"Bad Request: message to edit not found"}`
)

// botCall is one captured Bot API request.
type botCall struct {
	method string
	body   map[string]any
}

// botReply is one scripted response. The fake pops replies in order and falls
// back to the last one, so a test can script "first call fails, then succeed".
type botReply struct {
	status int
	body   string
}

// fakeBotAPI stands in for api.telegram.org. It is created per test and wired
// in through the *config* (cfg.Telegram.BaseURL) rather than a package global,
// so these tests can run in parallel without racing each other.
type fakeBotAPI struct {
	mu      sync.Mutex
	calls   []botCall
	replies []botReply
	next    int
}

func newFakeBotAPI(t *testing.T, replies ...botReply) (*fakeBotAPI, string) {
	t.Helper()

	if len(replies) == 0 {
		replies = []botReply{{http.StatusOK, botOK(1)}}
	}

	fake := &fakeBotAPI{replies: replies}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(req.Body).Decode(&body)

		// The path is /bot<token>/<method>.
		parts := strings.Split(strings.Trim(req.URL.Path, "/"), "/")
		method := parts[len(parts)-1]

		fake.mu.Lock()
		fake.calls = append(fake.calls, botCall{method: method, body: body})

		reply := fake.replies[min(fake.next, len(fake.replies)-1)]
		fake.next++
		fake.mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(reply.status)
		_, _ = w.Write([]byte(reply.body))
	}))
	t.Cleanup(srv.Close)

	return fake, srv.URL
}

func botOK(messageID int) string {
	return `{"ok":true,"result":{"message_id":` + strconv.Itoa(messageID) + `}}`
}

func (f *fakeBotAPI) sendCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()

	count := 0

	for _, call := range f.calls {
		if call.method == "sendMessage" {
			count++
		}
	}

	return count
}

func (f *fakeBotAPI) callsFor(method string) []botCall {
	f.mu.Lock()
	defer f.mu.Unlock()

	out := make([]botCall, 0, len(f.calls))

	for _, call := range f.calls {
		if call.method == method {
			out = append(out, call)
		}
	}

	return out
}

// enableTelegram points the instance config at the fake Bot API.
func enableTelegram(env *phoneTestEnv, baseURL string) {
	env.jctx.AppConfig.Telegram = config.TelegramConfig{
		Enabled:       true,
		BotToken:      "123456789:AAtest",
		BotUsername:   "solidping_bot",
		WebhookSecret: "secret",
		BaseURL:       baseURL,
	}
}

const testTelegramChat = "987654321"

func telegramRoute(orgUID string, verified bool) *models.UserNotificationRoute {
	contact := &models.UserContact{
		UID:             "contact-telegram",
		OrganizationUID: orgUID,
		UserUID:         "user-1",
		Type:            models.UserContactTypeTelegram,
		Value:           testTelegramChat,
	}

	if verified {
		now := time.Now()
		contact.VerifiedAt = &now
	}

	return &models.UserNotificationRoute{UID: "route-telegram", UserUID: "user-1", Contact: contact}
}

// persistTelegramContact stores a verified telegram contact so tests can assert
// on what the dispatcher writes back to it.
func persistTelegramContact(t *testing.T, env *phoneTestEnv) *models.UserContact {
	t.Helper()

	ctx := context.Background()
	now := time.Now()

	// The contact row has a FK to users, so the owner has to actually exist.
	user := models.NewUser("oncall@example.com")
	require.NoError(t, env.db.CreateUser(ctx, user))

	contact := models.NewUserContact(
		user.UID, env.org.UID, models.UserContactTypeTelegram, testTelegramChat, "@flo",
	)
	contact.VerifiedAt = &now
	require.NoError(t, env.db.UpsertUserContact(ctx, contact))

	stored, err := env.db.ListUserContactsByTypeValue(ctx, models.UserContactTypeTelegram, testTelegramChat)
	require.NoError(t, err)
	require.Len(t, stored, 1)

	return stored[0]
}

// --- Happy path -------------------------------------------------------------

func TestDispatch_TelegramFilterSendsAndSkipsEmail(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := context.Background()

	env := setupPhoneEnv(t, false, "")
	fake, baseURL := newFakeBotAPI(t)
	enableTelegram(env, baseURL)

	run := newRun()
	filter := map[string]bool{"telegram": true}

	sent := run.dispatchRoute(ctx, env.jctx, slog.Default(), env.incident, telegramRoute(env.org.UID, true), filter)
	emailSent := run.dispatchRoute(ctx, env.jctx, slog.Default(), env.incident, emailRoute(), filter)

	r.Equal(1, sent)
	r.Equal(0, emailSent, "email must be skipped under {telegram}")
	r.Equal(1, fake.sendCount())
	r.Equal(0, env.emails.sends())

	body := fake.callsFor("sendMessage")[0].body
	r.Equal(testTelegramChat, body["chat_id"])
	r.Equal("HTML", body["parse_mode"])

	text, ok := body["text"].(string)
	r.True(ok)
	r.Contains(text, "🔴 Incident — API health")
	r.Contains(text, "<b>Status:</b> DOWN")
	r.Contains(text, "<b>Org:</b> "+env.org.Slug)
	r.Contains(text, "https://app.example.com/dash0/orgs/"+env.org.Slug+"/incidents/incident-1")

	// The first alert of an incident is standalone.
	_, hasReply := body["reply_to_message_id"]
	r.False(hasReply)
}

// A hostile check name must still produce a message Telegram accepts — the
// end-to-end control for the escaping unit test.
func TestDispatch_TelegramEscapesCheckName(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := context.Background()

	env := setupPhoneEnv(t, false, "")
	fake, baseURL := newFakeBotAPI(t)
	enableTelegram(env, baseURL)

	hostile := "A & B <script>"
	r.NoError(env.db.UpdateCheck(ctx, "check-1", &models.CheckUpdate{Name: &hostile}))

	sent := newRun().dispatchRoute(
		ctx, env.jctx, slog.Default(), env.incident,
		telegramRoute(env.org.UID, true), map[string]bool{"telegram": true},
	)
	r.Equal(1, sent)

	text, ok := fake.callsFor("sendMessage")[0].body["text"].(string)
	r.True(ok)
	r.Contains(text, "A &amp; B &lt;script&gt;")
	r.NotContains(text, "<script>")
}

// --- Severity gating --------------------------------------------------------

// A severity that names channels but NOT telegram must not page telegram —
// otherwise a severity carefully scoped to "email only" would still buzz a
// phone.
func TestDispatch_TelegramNotSentWhenSeverityExcludesIt(t *testing.T) {
	t.Parallel()

	for name, filter := range map[string]map[string]bool{
		"email and sms":   {"email": true, "sms": true},
		"whatsapp only":   {"whatsapp": true},
		"critical push":   {"critical_push": true},
		"unrelated token": {"slack": true},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			r := require.New(t)
			ctx := context.Background()

			env := setupPhoneEnv(t, false, "")
			fake, baseURL := newFakeBotAPI(t)
			enableTelegram(env, baseURL)

			sent := newRun().dispatchRoute(
				ctx, env.jctx, slog.Default(), env.incident, telegramRoute(env.org.UID, true), filter,
			)

			r.Equal(0, sent)
			r.Equal(0, fake.sendCount(),
				"a severity that lists channels without telegram must not page telegram")
		})
	}
}

// The counterpart: an escalation with NO severity attached pages every channel
// the user connected, telegram included. Connecting the chat was the opt-in;
// unlike voice or WhatsApp there is no cost and no interruption class that
// would justify holding it back.
func TestDispatch_TelegramSentWithNilFilter(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := context.Background()

	env := setupPhoneEnv(t, false, "")
	fake, baseURL := newFakeBotAPI(t)
	enableTelegram(env, baseURL)

	sent := newRun().dispatchRoute(
		ctx, env.jctx, slog.Default(), env.incident, telegramRoute(env.org.UID, true), nil,
	)

	r.Equal(1, sent, "a severity-less escalation must reach a connected telegram chat")
	r.Equal(1, fake.sendCount())
}

// fanOutWithSeverity gates person targets BEFORE dispatchRoute is reached, so a
// {telegram}-only severity that failed this gate would page nobody no matter
// how correct pageTelegram is.
func TestSeverityAllowsPersonTargets_Telegram(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	r.True(severityAllowsPersonTargets(map[string]bool{"telegram": true}),
		"a telegram-only severity must open the person-target gate")
	r.False(severityAllowsPersonTargets(map[string]bool{"slack": true}))

	// All three branches of the per-route gate stay pinned:
	// explicit token, no severity at all, and a severity that omits telegram.
	r.True(severityAllowsTelegram(map[string]bool{"telegram": true}))
	r.True(severityAllowsTelegram(nil),
		"no severity means every connected channel fires, telegram included")
	r.False(severityAllowsTelegram(map[string]bool{"email": true, "whatsapp": true}))
	r.False(severityAllowsTelegram(map[string]bool{}),
		"an empty (but non-nil) channel set is an explicit 'nothing', not 'everything'")
}

// --- Skips ------------------------------------------------------------------

func TestDispatch_TelegramUnverifiedNeverMessaged(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := context.Background()

	env := setupPhoneEnv(t, false, "")
	fake, baseURL := newFakeBotAPI(t)
	enableTelegram(env, baseURL)

	sent := newRun().dispatchRoute(
		ctx, env.jctx, slog.Default(), env.incident,
		telegramRoute(env.org.UID, false), map[string]bool{"telegram": true},
	)

	r.Equal(0, sent)
	r.Equal(0, fake.sendCount())
}

func TestDispatch_TelegramDisabledInstanceSkips(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := context.Background()

	env := setupPhoneEnv(t, false, "")
	// Telegram deliberately left unconfigured.
	sent := newRun().dispatchRoute(
		ctx, env.jctx, slog.Default(), env.incident,
		telegramRoute(env.org.UID, true), map[string]bool{"telegram": true},
	)

	r.Equal(0, sent)
}

// TestDispatch_TelegramTokenOnlyStillDispatches pins the Configured() vs
// Active() split from the dispatch side: an instance holding only a bot token
// (no @username yet — it is derived at boot) must still page an already
// verified chat. The username is needed to BUILD a connect link, never to use
// a chat that is already connected.
func TestDispatch_TelegramTokenOnlyStillDispatches(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := context.Background()

	env := setupPhoneEnv(t, false, "")
	fake, baseURL := newFakeBotAPI(t)
	env.jctx.AppConfig.Telegram = config.TelegramConfig{
		Enabled:  true,
		BotToken: "123456789:AAtest",
		BaseURL:  baseURL,
		// BotUsername deliberately empty: Active() is false, Configured() true.
	}
	r.True(env.jctx.AppConfig.Telegram.Configured())
	r.False(env.jctx.AppConfig.Telegram.Active())

	sent := newRun().dispatchRoute(
		ctx, env.jctx, slog.Default(), env.incident,
		telegramRoute(env.org.UID, true), map[string]bool{"telegram": true},
	)

	r.Equal(1, sent)
	r.Equal(1, fake.sendCount())
}

// --- Failure handling -------------------------------------------------------

func TestDispatch_TelegramSendFailureFallsThrough(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		status   int
		response string
		// attempts is how many sends the dispatcher should make. A rate limit
		// carrying a short retry_after is waited out and retried once; every
		// other class is attempted exactly once.
		attempts int
	}{
		{"blocked", http.StatusForbidden, botErrBlocked, 1},
		{"chat gone", http.StatusBadRequest, botErrChatNotFound, 1},
		{"bad token", http.StatusUnauthorized, botErrUnauthorized, 1},
		{"rate limited", http.StatusTooManyRequests, botErrRateLimited, 2},
		{"server error", http.StatusInternalServerError, botErrServer, 1},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			r := require.New(t)
			ctx := context.Background()

			env := setupPhoneEnv(t, false, "")
			fake, baseURL := newFakeBotAPI(t, botReply{tc.status, tc.response})
			enableTelegram(env, baseURL)

			sent := newRun().dispatchRoute(
				ctx, env.jctx, slog.Default(), env.incident,
				telegramRoute(env.org.UID, true), map[string]bool{"telegram": true},
			)

			r.Equal(0, sent, "a failed send must not be reported as delivered")
			r.Equal(tc.attempts, fake.sendCount(), "the send was attempted")
		})
	}
}

// A blocked contact that stayed "verified" would silently swallow every future
// page — the worst failure mode a paging channel has.
func TestDispatch_TelegramBlockedClearsVerifiedAt(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		status   int
		response string
	}{
		{"blocked", http.StatusForbidden, botErrBlocked},
		{"deactivated", http.StatusForbidden, botErrDeactivated},
		{"chat not found", http.StatusBadRequest, botErrChatNotFound},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			r := require.New(t)
			ctx := context.Background()

			env := setupPhoneEnv(t, false, "")
			_, baseURL := newFakeBotAPI(t, botReply{tc.status, tc.response})
			enableTelegram(env, baseURL)

			contact := persistTelegramContact(t, env)
			r.NotNil(contact.VerifiedAt)

			route := telegramRoute(env.org.UID, true)
			route.Contact = contact

			sent := newRun().dispatchRoute(
				ctx, env.jctx, slog.Default(), env.incident, route, map[string]bool{"telegram": true},
			)
			r.Equal(0, sent)

			after, err := env.db.ListUserContactsByTypeValue(
				ctx, models.UserContactTypeTelegram, testTelegramChat,
			)
			r.NoError(err)
			r.Len(after, 1)
			r.Nil(after[0].VerifiedAt, "an unreachable contact must be un-verified")
		})
	}
}

// The positive control for the test above: a TRANSIENT failure must leave the
// contact verified, or one flaky minute would silently disable a live route.
func TestDispatch_TelegramTransientFailureKeepsVerifiedAt(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := context.Background()

	env := setupPhoneEnv(t, false, "")
	_, baseURL := newFakeBotAPI(t, botReply{http.StatusTooManyRequests, botErrRateLimited})
	enableTelegram(env, baseURL)

	contact := persistTelegramContact(t, env)
	route := telegramRoute(env.org.UID, true)
	route.Contact = contact

	sent := newRun().dispatchRoute(
		ctx, env.jctx, slog.Default(), env.incident, route, map[string]bool{"telegram": true},
	)
	r.Equal(0, sent)

	after, err := env.db.ListUserContactsByTypeValue(ctx, models.UserContactTypeTelegram, testTelegramChat)
	r.NoError(err)
	r.NotNil(after[0].VerifiedAt, "a transient failure must never disable a live contact")
}

// --- Threading --------------------------------------------------------------

func TestDispatch_TelegramSecondAlertRepliesToTheFirst(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := context.Background()

	env := setupPhoneEnv(t, false, "")
	fake, baseURL := newFakeBotAPI(t, botReply{http.StatusOK, botOK(100)}, botReply{http.StatusOK, botOK(101)})
	enableTelegram(env, baseURL)

	filter := map[string]bool{"telegram": true}

	// Two separate runs: the per-chat dedup collapses repeats within one run.
	r.Equal(1, newRun().dispatchRoute(
		ctx, env.jctx, slog.Default(), env.incident, telegramRoute(env.org.UID, true), filter))
	r.Equal(1, newRun().dispatchRoute(
		ctx, env.jctx, slog.Default(), env.incident, telegramRoute(env.org.UID, true), filter))

	sends := fake.callsFor("sendMessage")
	r.Len(sends, 2)

	_, firstHasReply := sends[0].body["reply_to_message_id"]
	r.False(firstHasReply, "the first alert anchors the thread")

	r.InDelta(float64(100), sends[1].body["reply_to_message_id"], 0,
		"the second alert must reply to the first message")
}

// Threading is a nicety; it may never cost a delivery. A reply id Telegram
// rejects must degrade to a standalone message that still gets through.
func TestDispatch_TelegramRejectedReplyStillDelivers(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := context.Background()

	env := setupPhoneEnv(t, false, "")
	fake, baseURL := newFakeBotAPI(t,
		botReply{http.StatusOK, botOK(100)},
		botReply{http.StatusBadRequest, botErrReplyGone},
		botReply{http.StatusOK, botOK(102)},
	)
	enableTelegram(env, baseURL)

	filter := map[string]bool{"telegram": true}

	r.Equal(1, newRun().dispatchRoute(
		ctx, env.jctx, slog.Default(), env.incident, telegramRoute(env.org.UID, true), filter))

	// Second alert: the threaded attempt is rejected, the retry is standalone.
	r.Equal(1, newRun().dispatchRoute(
		ctx, env.jctx, slog.Default(), env.incident, telegramRoute(env.org.UID, true), filter),
		"a rejected reply target must not cost the delivery")

	sends := fake.callsFor("sendMessage")
	r.Len(sends, 3, "the rejected threaded send is retried standalone")

	r.InDelta(float64(100), sends[1].body["reply_to_message_id"], 0)

	_, retryHasReply := sends[2].body["reply_to_message_id"]
	r.False(retryHasReply, "the retry must carry no reply id")

	// The stale anchor is dropped and replaced by the message that actually
	// landed, so the next alert threads under a live message instead of
	// repeating the rejected round-trip.
	orgUID := env.org.UID
	entry, err := env.db.GetStateEntry(ctx, &orgUID, telegramThreadKey("incident-1", testTelegramChat))
	r.NoError(err)
	r.NotNil(entry)
	r.InDelta(float64(102), (*entry.Value)["messageId"], 0)
}

// A missing thread anchor (expired, or never stored) must simply send a plain
// standalone message.
func TestDispatch_TelegramMissingAnchorSendsStandalone(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := context.Background()

	env := setupPhoneEnv(t, false, "")
	fake, baseURL := newFakeBotAPI(t, botReply{http.StatusOK, botOK(100)})
	enableTelegram(env, baseURL)

	filter := map[string]bool{"telegram": true}
	r.Equal(1, newRun().dispatchRoute(
		ctx, env.jctx, slog.Default(), env.incident, telegramRoute(env.org.UID, true), filter))

	// Expire the anchor the way a week-old incident would.
	orgUID := env.org.UID
	_, err := env.db.DeleteStateEntry(ctx, &orgUID, telegramThreadKey("incident-1", testTelegramChat))
	r.NoError(err)

	r.Equal(1, newRun().dispatchRoute(
		ctx, env.jctx, slog.Default(), env.incident, telegramRoute(env.org.UID, true), filter))

	sends := fake.callsFor("sendMessage")
	r.Len(sends, 2)

	_, hasReply := sends[1].body["reply_to_message_id"]
	r.False(hasReply, "a missing anchor must degrade to a standalone message")
}

// On resolution the original alert is rewritten in place, so someone scrolling
// back does not read a stale red alert as live.
func TestDispatch_TelegramResolutionEditsTheOriginal(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := context.Background()

	env := setupPhoneEnv(t, false, "")
	fake, baseURL := newFakeBotAPI(t,
		botReply{http.StatusOK, botOK(100)},
		botReply{http.StatusOK, botOK(101)},
		botReply{http.StatusOK, `{"ok":true,"result":{"message_id":100}}`},
	)
	enableTelegram(env, baseURL)

	filter := map[string]bool{"telegram": true}
	r.Equal(1, newRun().dispatchRoute(
		ctx, env.jctx, slog.Default(), env.incident, telegramRoute(env.org.UID, true), filter))

	// The incident resolves.
	resolved := *env.incident
	resolved.State = models.IncidentStateResolved

	r.Equal(1, newRun().dispatchRoute(
		ctx, env.jctx, slog.Default(), &resolved, telegramRoute(env.org.UID, true), filter))

	edits := fake.callsFor("editMessageText")
	r.Len(edits, 1, "the original alert must be rewritten on resolution")
	r.InDelta(float64(100), edits[0].body["message_id"], 0)

	text, ok := edits[0].body["text"].(string)
	r.True(ok)
	r.Contains(text, "✅")
	r.Contains(text, "RESOLVED")
}

// A failed edit is cosmetic: the resolution message itself already went out, so
// the delivery must still count.
func TestDispatch_TelegramFailedEditStillCountsTheDelivery(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := context.Background()

	env := setupPhoneEnv(t, false, "")
	fake, baseURL := newFakeBotAPI(t,
		botReply{http.StatusOK, botOK(100)},
		botReply{http.StatusOK, botOK(101)},
		botReply{http.StatusBadRequest, botErrEditMissing},
	)
	enableTelegram(env, baseURL)

	filter := map[string]bool{"telegram": true}
	r.Equal(1, newRun().dispatchRoute(
		ctx, env.jctx, slog.Default(), env.incident, telegramRoute(env.org.UID, true), filter))

	resolved := *env.incident
	resolved.State = models.IncidentStateResolved

	r.Equal(1, newRun().dispatchRoute(
		ctx, env.jctx, slog.Default(), &resolved, telegramRoute(env.org.UID, true), filter),
		"a failed cosmetic edit must not undo a successful delivery")
	r.Len(fake.callsFor("editMessageText"), 1)
}

// --- Runaway guard ----------------------------------------------------------

func TestDispatch_TelegramRunawayGuardStopsTheFlood(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := context.Background()

	env := setupPhoneEnv(t, false, "")
	fake, baseURL := newFakeBotAPI(t)
	enableTelegram(env, baseURL)

	// A cap of 2 per hour: the third send in the same hour is refused.
	env.jctx.Services.Entitlements = entitlements.NewService(
		env.db, entitlements.DefaultsFor(config.DeploymentModeSelfHosted), 0,
		entitlements.WithTelegramRunawayCap(2),
	)

	filter := map[string]bool{"telegram": true}

	total := 0
	for range 4 {
		total += newRun().dispatchRoute(
			ctx, env.jctx, slog.Default(), env.incident, telegramRoute(env.org.UID, true), filter,
		)
	}

	r.Equal(2, total, "the hourly runaway guard bounds a dispatch loop")
	r.Equal(2, fake.sendCount())
}

// Telegram is free: there is deliberately NO monthly quota and no usage
// counter. A zero WhatsApp/SMS cap must not block a Telegram send.
func TestDispatch_TelegramHasNoMonthlyQuota(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := context.Background()

	env := setupPhoneEnv(t, false, "")
	fake, baseURL := newFakeBotAPI(t)
	enableTelegram(env, baseURL)

	entSvc := entitlements.NewService(env.db, entitlements.DefaultsFor(config.DeploymentModeSelfHosted), 0)
	r.NoError(entSvc.Set(ctx, env.org.UID, entitlements.Entitlements{
		Limits: entitlements.Limits{
			MaxWhatsappPerMonth: entitlements.Int(0),
			MaxSmsPerMonth:      entitlements.Int(0),
		},
		Source: models.EntitlementSourceAdmin,
	}, "tester", "no paid channels"))
	env.jctx.Services.Entitlements = entSvc

	sent := newRun().dispatchRoute(
		ctx, env.jctx, slog.Default(), env.incident,
		telegramRoute(env.org.UID, true), map[string]bool{"telegram": true},
	)

	r.Equal(1, sent, "a free channel must not be gated by a paid channel's quota")
	r.Equal(1, fake.sendCount())

	usage, err := entSvc.Usage(ctx, env.org.UID)
	r.NoError(err)
	r.Equal(0, usage.WhatsappThisMonth, "a Telegram send must not touch any usage counter")
}

func TestDispatch_TelegramDedupesWithinRun(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := context.Background()

	env := setupPhoneEnv(t, false, "")
	fake, baseURL := newFakeBotAPI(t)
	enableTelegram(env, baseURL)

	run := newRun()
	filter := map[string]bool{"telegram": true}

	first := run.dispatchRoute(ctx, env.jctx, slog.Default(), env.incident, telegramRoute(env.org.UID, true), filter)
	second := run.dispatchRoute(ctx, env.jctx, slog.Default(), env.incident, telegramRoute(env.org.UID, true), filter)

	r.Equal(1, first)
	r.Equal(0, second)
	r.Equal(1, fake.sendCount())
}

// --- Helpers ----------------------------------------------------------------

func TestTelegramStateLabel(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	now := time.Now()

	r.Equal(telegram.StateDown, telegramStateLabel(&models.Incident{State: models.IncidentStateActive}))
	r.Equal(telegram.StateEscalated, telegramStateLabel(&models.Incident{
		State: models.IncidentStateActive, EscalatedAt: &now,
	}))
	r.Equal(telegram.StateResolved, telegramStateLabel(&models.Incident{
		State: models.IncidentStateResolved, EscalatedAt: &now,
	}))
}

func TestTelegramDetail(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	title := "  connection\nrefused\tafter 3 tries  "
	r.Equal("connection refused after 3 tries", telegramDetail(&models.Incident{
		Title: &title, StartedAt: time.Now(),
	}))

	long := strings.Repeat("abcdefghij", 40)
	r.LessOrEqual(len(telegramDetail(&models.Incident{Title: &long, StartedAt: time.Now()})),
		telegramDetailCap+3) // +3 for the multi-byte ellipsis

	detail := telegramDetail(&models.Incident{StartedAt: time.Now().Add(-5 * time.Minute)})
	r.Contains(detail, "open for")
}

func TestTelegramIncidentURL(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	r.Equal("https://app.example.com/dash0/orgs/acme/incidents/abc",
		telegramIncidentURL("https://app.example.com/", "acme", "abc"))
	// No base URL / no org slug simply omits the link rather than emitting a
	// broken one.
	r.Empty(telegramIncidentURL("", "acme", "abc"))
	r.Empty(telegramIncidentURL("https://app.example.com", "", "abc"))
}

// Telegram's per-chat burst limit is ~1 message/second, and it tells us exactly
// how long to wait. Honoring that number turns a would-be lost page into a
// delivered one; a generic backoff would either give up or stall the worker.
func TestDispatch_TelegramHonorsRetryAfterAndDelivers(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := context.Background()

	env := setupPhoneEnv(t, false, "")
	fake, baseURL := newFakeBotAPI(t,
		botReply{
			http.StatusTooManyRequests,
			`{"ok":false,"error_code":429,"description":"Too Many Requests","parameters":{"retry_after":1}}`,
		},
		botReply{http.StatusOK, botOK(300)},
	)
	enableTelegram(env, baseURL)

	start := time.Now()
	sent := newRun().dispatchRoute(
		ctx, env.jctx, slog.Default(), env.incident,
		telegramRoute(env.org.UID, true), map[string]bool{"telegram": true},
	)

	r.Equal(1, sent, "a throttled send must be retried after Telegram's own cooldown")
	r.Equal(2, fake.sendCount())
	r.GreaterOrEqual(time.Since(start), time.Second, "the retry must wait the requested cooldown")
}

// A cooldown longer than we are willing to hold a worker for is NOT waited out:
// the step falls through so the next escalation repeat can try instead.
func TestDispatch_TelegramDoesNotWaitOutALongCooldown(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := context.Background()

	env := setupPhoneEnv(t, false, "")
	fake, baseURL := newFakeBotAPI(t, botReply{
		http.StatusTooManyRequests,
		`{"ok":false,"error_code":429,"description":"Too Many Requests","parameters":{"retry_after":3600}}`,
	})
	enableTelegram(env, baseURL)

	start := time.Now()
	sent := newRun().dispatchRoute(
		ctx, env.jctx, slog.Default(), env.incident,
		telegramRoute(env.org.UID, true), map[string]bool{"telegram": true},
	)

	r.Equal(0, sent)
	r.Equal(1, fake.sendCount(), "an hour-long cooldown must not be retried inline")
	r.Less(time.Since(start), 10*time.Second)
}
