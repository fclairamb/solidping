package telegram_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/integrations/telegram"
)

const testBotToken = "123456789:AAtest-token"

// fakeBotAPI stands in for api.telegram.org and captures the last request it
// saw. Every test gets its own server — nothing here touches a package-level
// global, so the tests are safe under t.Parallel.
type fakeBotAPI struct {
	server *httptest.Server
	path   string
	ctype  string
	body   map[string]any
}

func newFakeBotAPI(t *testing.T, status int, response string) *fakeBotAPI {
	t.Helper()

	fake := &fakeBotAPI{}
	fake.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		fake.path = r.URL.Path
		fake.ctype = r.Header.Get("Content-Type")
		_ = json.Unmarshal(body, &fake.body)

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(response))
	}))
	t.Cleanup(fake.server.Close)

	return fake
}

func newTestClient(t *testing.T, fake *fakeBotAPI) *telegram.Client {
	t.Helper()

	client, err := telegram.NewClientWithBaseURL(testBotToken, fake.server.URL)
	require.NoError(t, err)

	return client
}

func TestSendMessage_PayloadShape(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	fake := newFakeBotAPI(t, http.StatusOK, `{"ok":true,"result":{"message_id":4242}}`)
	client := newTestClient(t, fake)

	id, err := client.SendMessage(context.Background(), &telegram.Message{
		ChatID:           "987654",
		HTML:             "<b>hello</b>",
		ReplyToMessageID: 17,
	})
	r.NoError(err)
	r.Equal(int64(4242), id)

	// The token rides in the PATH — Telegram's scheme, not a header.
	r.Equal("/bot"+testBotToken+"/sendMessage", fake.path)
	r.Equal("application/json", fake.ctype)

	r.Equal("987654", fake.body["chat_id"])
	r.Equal("<b>hello</b>", fake.body["text"])
	r.Equal("HTML", fake.body["parse_mode"])
	r.InDelta(float64(17), fake.body["reply_to_message_id"], 0)

	preview, ok := fake.body["link_preview_options"].(map[string]any)
	r.True(ok, "link_preview_options must be present")
	r.Equal(true, preview["is_disabled"])
}

func TestSendMessage_OmitsReplyWhenUnset(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	fake := newFakeBotAPI(t, http.StatusOK, `{"ok":true,"result":{"message_id":1}}`)
	client := newTestClient(t, fake)

	_, err := client.SendMessage(context.Background(), &telegram.Message{ChatID: "1", HTML: "x"})
	r.NoError(err)

	_, present := fake.body["reply_to_message_id"]
	r.False(present, "reply_to_message_id must be omitted when there is nothing to reply to")
	_, threadPresent := fake.body["message_thread_id"]
	r.False(threadPresent)
}

func TestSendMessage_RejectsEmptyInputs(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	fake := newFakeBotAPI(t, http.StatusOK, `{"ok":true,"result":{"message_id":1}}`)
	client := newTestClient(t, fake)

	_, err := client.SendMessage(context.Background(), &telegram.Message{ChatID: "", HTML: "x"})
	r.ErrorIs(err, telegram.ErrInvalidChat)

	_, err = client.SendMessage(context.Background(), &telegram.Message{ChatID: "1", HTML: "  "})
	r.ErrorIs(err, telegram.ErrEmptyMessage)
}

func TestSendMessage_TypedErrors(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		status   int
		response string
		want     error
		reason   string
	}{
		{
			name:     "unauthorized token",
			status:   http.StatusUnauthorized,
			response: `{"ok":false,"error_code":401,"description":"Unauthorized"}`,
			want:     telegram.ErrUnauthorized,
			reason:   "telegram_unauthorized",
		},
		{
			name:     "bot blocked by user",
			status:   http.StatusForbidden,
			response: `{"ok":false,"error_code":403,"description":"Forbidden: bot was blocked by the user"}`,
			want:     telegram.ErrBotBlocked,
			reason:   "telegram_bot_blocked",
		},
		{
			name:     "user deactivated",
			status:   http.StatusForbidden,
			response: `{"ok":false,"error_code":403,"description":"Forbidden: user is deactivated"}`,
			want:     telegram.ErrBotBlocked,
			reason:   "telegram_bot_blocked",
		},
		{
			name:     "chat not found",
			status:   http.StatusBadRequest,
			response: `{"ok":false,"error_code":400,"description":"Bad Request: chat not found"}`,
			want:     telegram.ErrChatNotFound,
			reason:   "telegram_chat_not_found",
		},
		{
			name:     "reply target gone",
			status:   http.StatusBadRequest,
			response: `{"ok":false,"error_code":400,"description":"Bad Request: message to be replied not found"}`,
			want:     telegram.ErrReplyTargetMissing,
			reason:   "telegram_reply_target_missing",
		},
		{
			name:     "rate limited",
			status:   http.StatusTooManyRequests,
			response: `{"ok":false,"error_code":429,"description":"Too Many Requests","parameters":{"retry_after":17}}`,
			want:     telegram.ErrRateLimited,
			reason:   "telegram_rate_limited",
		},
		{
			name:     "server error",
			status:   http.StatusBadGateway,
			response: `{"ok":false,"error_code":502,"description":"Bad Gateway"}`,
			want:     telegram.ErrRequestFailed,
			reason:   "telegram_send_failed",
		},
		{
			name:     "unparseable proxy body still classified",
			status:   http.StatusBadGateway,
			response: `<html>502 Bad Gateway</html>`,
			want:     telegram.ErrRequestFailed,
			reason:   "telegram_send_failed",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			r := require.New(t)

			fake := newFakeBotAPI(t, tc.status, tc.response)
			client := newTestClient(t, fake)

			_, err := client.SendMessage(context.Background(), &telegram.Message{ChatID: "1", HTML: "x"})
			r.Error(err)
			r.ErrorIs(err, tc.want)
			r.Equal(tc.reason, telegram.FailureReason(err))
		})
	}
}

func TestSendMessage_RetryAfterExtraction(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	fake := newFakeBotAPI(t, http.StatusTooManyRequests,
		`{"ok":false,"error_code":429,"description":"Too Many Requests: retry after 23","parameters":{"retry_after":23}}`)
	client := newTestClient(t, fake)

	_, err := client.SendMessage(context.Background(), &telegram.Message{ChatID: "1", HTML: "x"})
	r.ErrorIs(err, telegram.ErrRateLimited)
	r.Equal(23*time.Second, telegram.RetryAfter(err))

	var apiErr *telegram.APIError
	r.ErrorAs(err, &apiErr)
	r.Equal(429, apiErr.ErrorCode)
	r.Contains(apiErr.Description, "retry after 23")

	// A non-rate-limit error carries no cooldown.
	r.Equal(time.Duration(0), telegram.RetryAfter(telegram.ErrBotBlocked))
}

// A 200 with ok:false is the shape Telegram actually uses for several errors,
// and treating it as success would silently drop pages.
func TestSendMessage_OKFalseWithHTTP200IsAnError(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	fake := newFakeBotAPI(t, http.StatusOK,
		`{"ok":false,"error_code":403,"description":"Forbidden: bot was blocked by the user"}`)
	client := newTestClient(t, fake)

	_, err := client.SendMessage(context.Background(), &telegram.Message{ChatID: "1", HTML: "x"})
	r.ErrorIs(err, telegram.ErrBotBlocked)
	r.True(telegram.ContactDisabling(err))
}

func TestContactDisabling(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	r.True(telegram.ContactDisabling(telegram.ErrBotBlocked))
	r.True(telegram.ContactDisabling(telegram.ErrChatNotFound))
	r.False(telegram.ContactDisabling(telegram.ErrRateLimited))
	r.False(telegram.ContactDisabling(telegram.ErrRequestFailed))
	r.False(telegram.ContactDisabling(nil))
}

func TestEditMessageText(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	fake := newFakeBotAPI(t, http.StatusOK, `{"ok":true,"result":{"message_id":9}}`)
	client := newTestClient(t, fake)

	r.NoError(client.EditMessageText(context.Background(), "555", 9, "<b>done</b>"))
	r.Equal("/bot"+testBotToken+"/editMessageText", fake.path)
	r.Equal("555", fake.body["chat_id"])
	r.InDelta(float64(9), fake.body["message_id"], 0)
	r.Equal("HTML", fake.body["parse_mode"])
}

func TestGetMe(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	fake := newFakeBotAPI(t, http.StatusOK,
		`{"ok":true,"result":{"id":1,"is_bot":true,"username":"solidping_bot","first_name":"SolidPing"}}`)
	client := newTestClient(t, fake)

	me, err := client.GetMe(context.Background())
	r.NoError(err)
	r.Equal("solidping_bot", me.Username)
	r.True(me.IsBot)
	r.Equal("/bot"+testBotToken+"/getMe", fake.path)
}

func TestSetWebhook_SendsSecretAndAllowedUpdates(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	fake := newFakeBotAPI(t, http.StatusOK, `{"ok":true,"result":true}`)
	client := newTestClient(t, fake)

	r.NoError(client.SetWebhook(
		context.Background(),
		"https://solidping.io/api/v1/integrations/telegram/webhook",
		"s3cr3t",
	))

	r.Equal("/bot"+testBotToken+"/setWebhook", fake.path)
	r.Equal("https://solidping.io/api/v1/integrations/telegram/webhook", fake.body["url"])
	r.Equal("s3cr3t", fake.body["secret_token"])

	updates, ok := fake.body["allowed_updates"].([]any)
	r.True(ok)
	// callback_query is what carries the Acknowledge button press; without it in
	// the subscription Telegram simply never delivers one.
	r.Equal([]any{"message", "callback_query", "my_chat_member"}, updates)
}

func TestGetWebhookInfo(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	fake := newFakeBotAPI(t, http.StatusOK,
		`{"ok":true,"result":{"url":"https://example.com/hook","pending_update_count":3}}`)
	client := newTestClient(t, fake)

	info, err := client.GetWebhookInfo(context.Background())
	r.NoError(err)
	r.Equal("https://example.com/hook", info.URL)
	r.Equal(3, info.PendingUpdateCount)
}

func TestNewClientFromConfig(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	// Killed by the switch: no client, and the reason is typed.
	_, err := telegram.NewClientFromConfig(&config.TelegramConfig{
		Enabled: config.BoolPtr(false), BotToken: "t", BotUsername: "u",
	})
	r.ErrorIs(err, telegram.ErrNotConfigured)

	// No token means no bot identity, whatever else is set.
	_, err = telegram.NewClientFromConfig(&config.TelegramConfig{Enabled: config.BoolPtr(true), BotUsername: "u"})
	r.ErrorIs(err, telegram.ErrNotConfigured)

	_, err = telegram.NewClientFromConfig(nil)
	r.ErrorIs(err, telegram.ErrNotConfigured)

	// A token alone is enough to SEND: the @username only ever mattered for
	// building a connect link, which the client never does.
	client, err := telegram.NewClientFromConfig(&config.TelegramConfig{
		Enabled: config.BoolPtr(true), BotToken: testBotToken,
	})
	r.NoError(err)
	r.NotNil(client)

	client, err = telegram.NewClientFromConfig(&config.TelegramConfig{
		Enabled: config.BoolPtr(true), BotToken: testBotToken, BotUsername: "solidping_bot",
	})
	r.NoError(err)
	r.NotNil(client)
}

func TestValidChatID(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	r.True(telegram.ValidChatID("123456"))
	r.True(telegram.ValidChatID("-1001234567890"))
	r.False(telegram.ValidChatID(""))
	r.False(telegram.ValidChatID("@someone"))
	r.False(telegram.ValidChatID("12ab"))
	// Telegram has no chat 0: a "0" always means an update arrived with no real
	// chat, and storing it would create an undeliverable contact.
	r.False(telegram.ValidChatID("0"))
	r.False(telegram.ValidChatID(" 0 "))
}
