package app

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/integrations/telegram"
)

// fakeTelegramAPI stands in for api.telegram.org. It records every Bot API
// method it was asked for, in order, together with the payload — which is the
// only way to assert on setWebhook's `secret_token`, since Telegram's own
// getWebhookInfo never returns it.
type fakeTelegramAPI struct {
	server *httptest.Server

	mu       sync.Mutex
	calls    []string
	payloads map[string]map[string]any

	// registeredURL is what getWebhookInfo reports back.
	registeredURL string
	// lastError is echoed as getWebhookInfo's last_error_message.
	lastError string
	// failGetWebhookInfo makes getWebhookInfo answer a Bot API error.
	failGetWebhookInfo bool
	// getMeUsername is what getMe answers with.
	getMeUsername string
	// failGetMe makes getMe answer a Bot API error, standing in for an
	// unreachable Telegram on a first boot.
	failGetMe bool
}

func newFakeTelegramAPI(t *testing.T) *fakeTelegramAPI {
	t.Helper()

	fake := &fakeTelegramAPI{
		payloads:      map[string]map[string]any{},
		getMeUsername: "solidping_bot",
	}

	fake.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)

		// Path shape is /bot<token>/<method>.
		parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/"), "/")
		method := parts[len(parts)-1]

		fake.mu.Lock()
		fake.calls = append(fake.calls, method)

		payload := map[string]any{}
		_ = json.Unmarshal(body, &payload)
		fake.payloads[method] = payload

		registeredURL, lastError := fake.registeredURL, fake.lastError
		failInfo, username := fake.failGetWebhookInfo, fake.getMeUsername
		failMe := fake.failGetMe
		fake.mu.Unlock()

		w.Header().Set("Content-Type", "application/json")

		switch method {
		case "getMe":
			if failMe {
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = w.Write([]byte(`{"ok":false,"error_code":500,"description":"boom"}`))

				return
			}

			_, _ = w.Write([]byte(`{"ok":true,"result":{"id":1,"is_bot":true,"username":"` + username + `"}}`))
		case "getWebhookInfo":
			if failInfo {
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = w.Write([]byte(`{"ok":false,"error_code":500,"description":"boom"}`))

				return
			}

			info := map[string]any{
				"url":                  registeredURL,
				"pending_update_count": 3,
				"last_error_message":   lastError,
			}
			out, _ := json.Marshal(map[string]any{"ok": true, "result": info})
			_, _ = w.Write(out)
		case "setWebhook":
			_, _ = w.Write([]byte(`{"ok":true,"result":true}`))
		default:
			_, _ = w.Write([]byte(`{"ok":true,"result":{}}`))
		}
	}))
	t.Cleanup(fake.server.Close)

	return fake
}

func (f *fakeTelegramAPI) callCount(method string) int {
	f.mu.Lock()
	defer f.mu.Unlock()

	n := 0

	for _, c := range f.calls {
		if c == method {
			n++
		}
	}

	return n
}

func (f *fakeTelegramAPI) payload(method string) map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()

	return f.payloads[method]
}

// telegramTestConfig builds a fully-configured instance pointed at the fake.
func telegramTestConfig(fake *fakeTelegramAPI, secret string) *config.Config {
	cfg := &config.Config{}
	cfg.Server.BaseURL = "https://solidping.test"
	cfg.Telegram = config.TelegramConfig{
		Enabled:       config.BoolPtr(true),
		BotToken:      "123456789:AAtest-token",
		BotUsername:   "solidping_bot",
		WebhookSecret: secret,
		BaseURL:       fake.server.URL,
	}

	return cfg
}

// TestEnsureTelegramWebhook_RegistersEvenWhenURLMatches is the regression test
// for the shipped bug: setWebhook used to be skipped whenever getWebhookInfo
// already reported our URL. Since the Bot API never returns the registered
// secret_token, that early return made a secret rotation at a constant URL
// invisible — Telegram kept echoing the OLD secret and the handler 403'd every
// update.
func TestEnsureTelegramWebhook_RegistersEvenWhenURLMatches(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	fake := newFakeTelegramAPI(t)
	cfg := telegramTestConfig(fake, "rotated-secret-value")
	// Telegram already holds EXACTLY the URL we want.
	fake.registeredURL = "https://solidping.test" + TelegramWebhookPath

	client, err := telegram.NewClientFromConfig(&cfg.Telegram)
	r.NoError(err)

	ensureTelegramWebhook(context.Background(), client, cfg)

	r.Equal(1, fake.callCount("setWebhook"),
		"setWebhook must be called even when the registered URL already matches")

	payload := fake.payload("setWebhook")
	r.Equal("https://solidping.test"+TelegramWebhookPath, payload["url"])
	r.Equal("rotated-secret-value", payload["secret_token"],
		"the CURRENT secret must reach Telegram; it can never be read back")
	// callback_query is what carries the Acknowledge button press; without it in
	// the subscription Telegram never delivers one.
	r.Equal([]any{"message", "callback_query", "my_chat_member"}, payload["allowed_updates"])
}

// TestEnsureTelegramWebhook_RegistersWhenInfoUnreadable proves the diagnostics
// call no longer gates registration: a getWebhookInfo failure must not stop the
// webhook from being (re-)registered.
func TestEnsureTelegramWebhook_RegistersWhenInfoUnreadable(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	fake := newFakeTelegramAPI(t)
	fake.failGetWebhookInfo = true
	cfg := telegramTestConfig(fake, "some-secret")

	client, err := telegram.NewClientFromConfig(&cfg.Telegram)
	r.NoError(err)

	ensureTelegramWebhook(context.Background(), client, cfg)

	r.Equal(1, fake.callCount("setWebhook"))
	r.Equal("some-secret", fake.payload("setWebhook")["secret_token"])
}

// TestEnsureTelegramWebhook_SkipsWithoutBaseURL keeps the one legitimate skip:
// there is nothing to register when the instance has no public URL.
func TestEnsureTelegramWebhook_SkipsWithoutBaseURL(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	fake := newFakeTelegramAPI(t)
	cfg := telegramTestConfig(fake, "some-secret")
	cfg.Server.BaseURL = ""

	client, err := telegram.NewClientFromConfig(&cfg.Telegram)
	r.NoError(err)

	ensureTelegramWebhook(context.Background(), client, cfg)

	r.Equal(0, fake.callCount("setWebhook"))
}

// TestVerifyTelegramIdentity_PersistsUsernameStartupResolveMissed replays the
// exact incident this fix exists for, on a real (in-memory) DB.
//
// The synchronous startup resolver makes ONE getMe bounded to 3s, because it
// runs before any handler exists and must not delay boot. On the first pod of a
// cluster with a cold DNS cache that call can time out — and when it does, the
// old code wrote nothing down. cfg.Telegram.Active() stayed false, so
// POST /users/me/telegram/link answered "telegram is not configured" forever
// after, even though the async bootstrap's own getMe (15s budget) succeeded
// seconds later and cheerfully logged "Telegram bot ready".
//
// The fix persists what bootstrap learned, so the NEXT boot resolves the
// username from the DB with no network call at all.
func TestVerifyTelegramIdentity_PersistsUsernameStartupResolveMissed(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := context.Background()

	fake := newFakeTelegramAPI(t)
	fake.getMeUsername = "solidping_dev_bot"
	dbSvc := resolveTestDB(t)

	// --- Boot 1, startup resolution: Telegram is briefly unreachable.
	fake.failGetMe = true
	cfg := resolveTestConfig(fake)
	resolveTelegramSettings(ctx, dbSvc, cfg)

	r.False(cfg.Telegram.Active(), "the incident: the connect surface is off")
	_, stored := storedParam(t, dbSvc, TelegramBotUsernameParam)
	r.False(stored, "nothing was written down, which is what made it permanent")

	// --- Boot 1, async bootstrap: Telegram is reachable again.
	fake.failGetMe = false
	client, err := telegram.NewClientFromConfig(&cfg.Telegram)
	r.NoError(err)

	verifyTelegramIdentity(ctx, dbSvc, client, &cfg.Telegram)

	username, stored := storedParam(t, dbSvc, TelegramBotUsernameParam)
	r.True(stored, "bootstrap knows the username; it must write it down")
	r.Equal("solidping_dev_bot", username)

	// --- Boot 2: the connect surface comes back with no network call.
	before := fake.callCount("getMe")
	next := resolveTestConfig(fake)
	resolveTelegramSettings(ctx, dbSvc, next)

	r.True(next.Telegram.Active(), "the connect surface must self-heal on the next boot")
	r.Equal("solidping_dev_bot", next.Telegram.ResolvedBotUsername())
	r.Equal(before, fake.callCount("getMe"),
		"a persisted username must be read from the DB, never re-fetched")
}

// TestVerifyTelegramIdentity_KeepsExplicitUsernameOutOfTheDB guards the
// precedence rule the resolver documents: env/YAML outranks the stored
// parameter. Persisting an explicitly configured username would plant a stale
// value that keeps winning after the operator changes the env var.
func TestVerifyTelegramIdentity_KeepsExplicitUsernameOutOfTheDB(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := context.Background()

	fake := newFakeTelegramAPI(t)
	fake.getMeUsername = "solidping_bot"
	dbSvc := resolveTestDB(t)

	cfg := resolveTestConfig(fake)
	cfg.Telegram.BotUsername = "solidping_bot" // explicitly configured, and correct

	client, err := telegram.NewClientFromConfig(&cfg.Telegram)
	r.NoError(err)

	verifyTelegramIdentity(ctx, dbSvc, client, &cfg.Telegram)

	_, stored := storedParam(t, dbSvc, TelegramBotUsernameParam)
	r.False(stored, "an explicitly configured username stays the operator's to change")
}

// TestVerifyTelegramIdentity_DoesNotPersistAMismatch keeps the loud-warning
// path intact: when the operator's username disagrees with the token, the bot
// the TOKEN belongs to must not be quietly written down as the answer.
func TestVerifyTelegramIdentity_DoesNotPersistAMismatch(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := context.Background()

	fake := newFakeTelegramAPI(t)
	fake.getMeUsername = "the_real_bot"
	dbSvc := resolveTestDB(t)

	cfg := resolveTestConfig(fake)
	cfg.Telegram.BotUsername = "a_stale_bot"

	client, err := telegram.NewClientFromConfig(&cfg.Telegram)
	r.NoError(err)

	verifyTelegramIdentity(ctx, dbSvc, client, &cfg.Telegram)

	_, stored := storedParam(t, dbSvc, TelegramBotUsernameParam)
	r.False(stored, "a mismatch is a warning to the operator, not a value to adopt")
}

// TestEnsureTelegramCommands_RegistersTheAdvertisedList pins the boot-time
// setMyCommands call. It is unconditional and idempotent for the same reason
// setWebhook is: the list lives on Telegram's side, nothing here can read back
// what a previous deploy set, and a version that adds a command has to make it
// discoverable without anyone opening BotFather.
func TestEnsureTelegramCommands_RegistersTheAdvertisedList(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	fake := newFakeTelegramAPI(t)
	cfg := telegramTestConfig(fake, "some-secret")

	client, err := telegram.NewClientFromConfig(&cfg.Telegram)
	r.NoError(err)

	ensureTelegramCommands(context.Background(), client)

	r.Equal(1, fake.callCount("setMyCommands"))

	commands, ok := fake.payload("setMyCommands")["commands"].([]any)
	r.True(ok)
	r.Len(commands, len(telegram.Commands))

	names := make([]string, 0, len(commands))

	for _, entry := range commands {
		command, isMap := entry.(map[string]any)
		r.True(isMap)
		r.NotEmpty(command["description"], "a command with no description is invisible in the menu")

		name, isString := command["command"].(string)
		r.True(isString)
		names = append(names, name)
	}

	// The whole point of registering is that the menu matches what the running
	// version actually answers.
	r.Subset(names, []string{"status", "incidents", "ack", "incident", "help"})
}

// TestBootstrapTelegram_RegistersCommandsAndWebhook proves the command
// registration is actually wired into the boot path, not merely callable — a
// helper nothing calls advertises nothing.
func TestBootstrapTelegram_RegistersCommandsAndWebhook(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	fake := newFakeTelegramAPI(t)
	cfg := telegramTestConfig(fake, "some-secret")
	cfg.Node.Role = "all"

	bootstrapTelegram(context.Background(), nil, cfg)

	r.Equal(1, fake.callCount("setMyCommands"))
	r.Equal(1, fake.callCount("setWebhook"))
}
