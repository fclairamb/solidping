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
		Enabled:       true,
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
	r.Equal([]any{"message", "my_chat_member"}, payload["allowed_updates"])
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
