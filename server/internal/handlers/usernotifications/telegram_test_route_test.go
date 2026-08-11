package usernotifications

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
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/webpush"
)

// fakeBotAPI records the Bot API calls a test provokes.
type fakeBotAPI struct {
	server *httptest.Server

	mu       sync.Mutex
	calls    []string
	payloads map[string]map[string]any
}

func newFakeBotAPI(t *testing.T) *fakeBotAPI {
	t.Helper()

	fake := &fakeBotAPI{payloads: map[string]map[string]any{}}

	fake.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)

		// Path shape is /bot<token>/<method>.
		parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/"), "/")
		method := parts[len(parts)-1]

		payload := map[string]any{}
		_ = json.Unmarshal(body, &payload)

		fake.mu.Lock()
		fake.calls = append(fake.calls, method)
		fake.payloads[method] = payload
		fake.mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":4242}}`))
	}))
	t.Cleanup(fake.server.Close)

	return fake
}

func (f *fakeBotAPI) callCount(method string) int {
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

func (f *fakeBotAPI) payload(method string) map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()

	return f.payloads[method]
}

// telegramRoute builds a connected, verified Telegram route for chatID.
func telegramRoute(chatID string) *models.UserNotificationRoute {
	return &models.UserNotificationRoute{
		UID:     "route-uid",
		Enabled: true,
		Contact: &models.UserContact{
			UID:   "contact-uid",
			Type:  models.UserContactTypeTelegram,
			Value: chatID,
			Label: "Florent",
		},
	}
}

// TestDispatchTestRoute_TelegramSendsMessage is the regression test for a
// shipped gap: dispatchTestRoute knew email, Slack and web push but had no
// Telegram case, so it fell through to the default and answered
// "provider not configured for contact type \"telegram\"" — a 422 on the one
// button a user presses to confirm the setup they have just finished, while
// real escalation delivery through the same contact worked fine.
//
// It deliberately goes through dispatchTestRoute rather than calling
// dispatchTestTelegram directly: the bug WAS the missing case, so a test that
// invokes the helper itself would have passed against the broken code.
func TestDispatchTestRoute_TelegramSendsMessage(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	fake := newFakeBotAPI(t)
	cfg := activeTelegramConfig()
	cfg.BaseURL = fake.server.URL

	svc := NewService(nil, nil, WithTelegramConfig(cfg))

	err := svc.dispatchTestRoute(
		context.Background(), "org-uid", telegramRoute("8688918336"),
		nil, nil, webpush.Options{},
	)
	r.NoError(err)

	r.Equal(1, fake.callCount("sendMessage"), "the test button must actually send a message")

	payload := fake.payload("sendMessage")
	r.Equal("8688918336", payload["chat_id"], "the message must go to the connected chat")
	r.Contains(payload["text"], "Test alert from SolidPing")
}

// TestDispatchTestRoute_TelegramWithoutTokenIsNamed proves an unconfigured
// instance gets the specific error the handler maps to a clear message, not the
// generic "provider not configured" default that hid this bug.
func TestDispatchTestRoute_TelegramWithoutTokenIsNamed(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	// No bot token: the instance cannot send at all.
	svc := NewService(nil, nil, WithTelegramConfig(&config.TelegramConfig{}))

	err := svc.dispatchTestRoute(
		context.Background(), "org-uid", telegramRoute("8688918336"),
		nil, nil, webpush.Options{},
	)

	r.ErrorIs(err, ErrTelegramNotEnabled)
	r.NotContains(err.Error(), "provider not configured",
		"an unconfigured instance must be named as such, not reported as an unknown contact type")
}

// TestDispatchTestRoute_TelegramNeedsOnlyTheToken pins the Configured() vs
// Active() distinction: a chat that is already connected can be messaged with
// the token alone. The bot @username exists to BUILD connect links, and gating
// the test button on it would fail exactly the deployments this feature's
// "token is enough" design was built for.
func TestDispatchTestRoute_TelegramNeedsOnlyTheToken(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	fake := newFakeBotAPI(t)
	cfg := activeTelegramConfig()
	cfg.BaseURL = fake.server.URL
	cfg.BotUsername = "" // token-only instance: no username known yet

	svc := NewService(nil, nil, WithTelegramConfig(cfg))
	r.False(svc.TelegramEnabled(), "no username means no connect-link surface")

	err := svc.dispatchTestRoute(
		context.Background(), "org-uid", telegramRoute("8688918336"),
		nil, nil, webpush.Options{},
	)
	r.NoError(err, "sending to a connected chat must not require the bot username")
	r.Equal(1, fake.callCount("sendMessage"))
}
