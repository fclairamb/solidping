package telegramcb

import (
	"context"
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
	"github.com/fclairamb/solidping/server/internal/handlers/usernotifications"
	"github.com/fclairamb/solidping/server/internal/integrations/telegram"
)

const (
	testWebhookSecret = "a-very-long-random-webhook-secret"
	testChatID        = "987654321"
)

// recordingSender captures the bot's replies. Injected PER HANDLER, never a
// package global, so parallel tests cannot race on it.
type recordingSender struct {
	mu        sync.Mutex
	sent      []string
	keyboards []*telegram.InlineKeyboard
	edits     []recordedEdit
	toasts    []string
	err       error
}

// recordedEdit is one editMessageText the handler asked for.
type recordedEdit struct {
	chatID    string
	messageID int64
	html      string
	keyboard  *telegram.InlineKeyboard
}

func (s *recordingSender) SendMessage(ctx context.Context, chatID, html string) error {
	return s.SendKeyboard(ctx, chatID, html, nil)
}

func (s *recordingSender) SendKeyboard(
	_ context.Context, _, html string, keyboard *telegram.InlineKeyboard,
) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.sent = append(s.sent, html)
	s.keyboards = append(s.keyboards, keyboard)

	return s.err
}

func (s *recordingSender) EditMessage(
	_ context.Context, chatID string, messageID int64, html string, keyboard *telegram.InlineKeyboard,
) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.edits = append(s.edits, recordedEdit{
		chatID: chatID, messageID: messageID, html: html, keyboard: keyboard,
	})

	return s.err
}

func (s *recordingSender) AnswerCallback(_ context.Context, _, text string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.toasts = append(s.toasts, text)

	return s.err
}

func (s *recordingSender) messages() []string {
	s.mu.Lock()
	defer s.mu.Unlock()

	return append([]string(nil), s.sent...)
}

func (s *recordingSender) sentKeyboards() []*telegram.InlineKeyboard {
	s.mu.Lock()
	defer s.mu.Unlock()

	return append([]*telegram.InlineKeyboard(nil), s.keyboards...)
}

func (s *recordingSender) editedMessages() []recordedEdit {
	s.mu.Lock()
	defer s.mu.Unlock()

	return append([]recordedEdit(nil), s.edits...)
}

func (s *recordingSender) answeredToasts() []string {
	s.mu.Lock()
	defer s.mu.Unlock()

	return append([]string(nil), s.toasts...)
}

type tgEnv struct {
	handler *Handler
	db      db.Service
	sender  *recordingSender
	org     *models.Organization
	user    *models.User
}

// setupEnv builds a handler over a fresh in-memory SQLite. Each test gets its
// own DB — no shared global state.
func setupEnv(t *testing.T) *tgEnv {
	t.Helper()

	ctx := context.Background()

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	require.NoError(t, err)
	require.NoError(t, dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	org := models.NewOrganization("tg-org", "TG Org")
	require.NoError(t, dbSvc.CreateOrganization(ctx, org))

	user := models.NewUser("oncall@example.com")
	require.NoError(t, dbSvc.CreateUser(ctx, user))

	cfg := &config.Config{}
	cfg.Telegram = config.TelegramConfig{
		Enabled:       config.BoolPtr(true),
		BotToken:      "123:AAtest",
		BotUsername:   "solidping_test_bot",
		WebhookSecret: testWebhookSecret,
	}

	sender := &recordingSender{}

	return &tgEnv{
		handler: NewHandler(dbSvc, cfg, WithSender(sender)),
		db:      dbSvc,
		sender:  sender,
		org:     org,
		user:    user,
	}
}

// post fires one webhook request with the given secret header.
func (e *tgEnv) post(t *testing.T, secret, body string) *httptest.ResponseRecorder {
	t.Helper()

	req := httptest.NewRequestWithContext(
		context.Background(), http.MethodPost,
		"/api/v1/integrations/telegram/webhook", strings.NewReader(body),
	)
	if secret != "" {
		req.Header.Set(telegram.SecretTokenHeader, secret)
	}

	rec := httptest.NewRecorder()
	require.NoError(t, e.handler.HandleUpdate(rec, req))

	return rec
}

// mintToken creates a live connect token bound to this env's user + org.
func (e *tgEnv) mintToken(t *testing.T) string {
	t.Helper()

	svc := usernotifications.NewService(e.db, nil,
		usernotifications.WithTelegramConfig(&config.TelegramConfig{
			Enabled: config.BoolPtr(true), BotToken: "123:AAtest", BotUsername: "solidping_test_bot",
		}))

	resp, err := svc.CreateTelegramLink(context.Background(), e.org.Slug, e.user)
	require.NoError(t, err)
	require.Contains(t, resp.URL, "https://t.me/solidping_test_bot?start=")

	_, token, _ := strings.Cut(resp.URL, "?start=")
	require.NotEmpty(t, token)

	return token
}

func (e *tgEnv) telegramContacts(t *testing.T) []*models.UserContact {
	t.Helper()

	contacts, err := e.db.ListUserContactsByTypeValue(
		context.Background(), models.UserContactTypeTelegram, testChatID,
	)
	require.NoError(t, err)

	return contacts
}

func startBody(token string) string {
	return `{"update_id":1,"message":{"message_id":10,` +
		`"from":{"id":987654321,"is_bot":false,"first_name":"Flo","username":"flo"},` +
		`"chat":{"id":987654321,"type":"private","username":"flo"},` +
		`"text":"/start ` + token + `"}}`
}

// --- Authenticity -----------------------------------------------------------

// The secret header is the ONLY gate on this public endpoint. Every way of
// getting it wrong must be a bare 403, and the body must never be parsed —
// a malformed body with a bad secret must not produce a different answer than
// a well-formed one.
func TestHandleUpdate_RejectsBadSecretBeforeParsing(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		secret string
		body   string
	}{
		{"missing header", "", startBody("tok")},
		{"wrong secret", "not-the-secret", startBody("tok")},
		{"empty secret value", "", `{"update_id":1}`},
		{"prefix of the real secret", testWebhookSecret[:10], startBody("tok")},
		{"malformed body, bad secret", "nope", `{{{not json`},
		{"malformed body, no secret", "", `{{{not json`},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			r := require.New(t)
			env := setupEnv(t)

			rec := env.post(t, tc.secret, tc.body)
			r.Equal(http.StatusForbidden, rec.Code)
			// A bare 403: no body detail that would tell a prober how far it got.
			r.Empty(rec.Body.String())
			// Nothing was created, and the bot did not answer.
			r.Empty(env.telegramContacts(t))
			r.Empty(env.sender.messages())
		})
	}
}

// An instance with no webhook secret configured must accept nothing at all —
// an unauthenticated endpoint that creates paging contacts is worse than a
// missing feature.
func TestHandleUpdate_UnconfiguredSecretAcceptsNothing(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	env := setupEnv(t)
	env.handler.cfg.Telegram.WebhookSecret = ""

	rec := env.post(t, "", startBody("tok"))
	r.Equal(http.StatusForbidden, rec.Code)

	rec = env.post(t, "anything", startBody("tok"))
	r.Equal(http.StatusForbidden, rec.Code)
}

// Telegram retries any non-2xx forever, so once authenticated everything is a
// 200 — including updates we do not implement and bodies we cannot parse.
func TestHandleUpdate_AlwaysAcksOnceAuthenticated(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		body string
	}{
		{"unparseable body", `{{{not json`},
		{"unhandled update type", `{"update_id":2,"edited_message":{"message_id":3}}`},
		{"empty object", `{}`},
		{"message with no chat", `{"update_id":3,"message":{"message_id":4,"text":"hi"}}`},
		{"non-command text", `{"update_id":4,"message":{"message_id":5,"chat":{"id":1,"type":"private"},"text":"hello"}}`},
		{"channel post we never subscribed to", `{"update_id":5,"channel_post":{"message_id":6}}`},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			r := require.New(t)
			env := setupEnv(t)

			rec := env.post(t, testWebhookSecret, tc.body)
			r.Equal(http.StatusOK, rec.Code)
		})
	}
}

func TestHandleUpdate_BodyIsCapped(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	env := setupEnv(t)

	// Two MiB of junk: the read is capped at one, so the body cannot parse and
	// (crucially) the request is still answered rather than buffering forever.
	rec := env.post(t, testWebhookSecret, strings.Repeat("x", 2<<20))
	r.Equal(http.StatusOK, rec.Code)
}

// --- Connect flow -----------------------------------------------------------

func TestHandleStart_CreatesVerifiedContactBoundToUserAndOrg(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	env := setupEnv(t)

	token := env.mintToken(t)

	rec := env.post(t, testWebhookSecret, startBody(token))
	r.Equal(http.StatusOK, rec.Code)

	contacts := env.telegramContacts(t)
	r.Len(contacts, 1)
	r.Equal(env.user.UID, contacts[0].UserUID)
	r.Equal(env.org.UID, contacts[0].OrganizationUID)
	r.Equal(models.UserContactTypeTelegram, contacts[0].Type)
	r.Equal(testChatID, contacts[0].Value)
	// Pressing Start IS the verification — there is no code round-trip.
	r.NotNil(contacts[0].VerifiedAt)
	// The label is human-readable, not a bare numeric id.
	r.Equal("@flo", contacts[0].Label)

	// A route exists, so the contact is actually pageable.
	routes, err := env.db.ListUserContactsWithRoutes(context.Background(), env.user.UID, env.org.UID)
	r.NoError(err)

	found := false

	for _, route := range routes {
		if route.Contact != nil && route.Contact.Type == models.UserContactTypeTelegram {
			found = true

			r.True(route.Enabled)
		}
	}

	r.True(found, "a notification route must exist for the new telegram contact")

	// The confirmation names the org so a wrong-account link is obvious now.
	msgs := env.sender.messages()
	r.Len(msgs, 1)
	r.Contains(msgs[0], "tg-org")
}

func TestHandleStart_ReplayedTokenCreatesNothingSecondTime(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	env := setupEnv(t)

	token := env.mintToken(t)

	r.Equal(http.StatusOK, env.post(t, testWebhookSecret, startBody(token)).Code)
	r.Len(env.telegramContacts(t), 1)

	// Replay the exact same token: single-use means the second redemption is a
	// no-op, not a second contact.
	r.Equal(http.StatusOK, env.post(t, testWebhookSecret, startBody(token)).Code)
	r.Len(env.telegramContacts(t), 1)

	msgs := env.sender.messages()
	r.Len(msgs, 2)
	// The replay gets the same polite expiry answer as an unknown token.
	r.Contains(msgs[1], "expired")
}

func TestHandleStart_ExpiredTokenRepliesPolitelyAndCreatesNothing(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	env := setupEnv(t)

	ctx := context.Background()
	token := "expired-token-value"
	past := -time.Minute

	// A token stored with an already-elapsed TTL is exactly what an expired
	// link looks like at redemption time.
	r.NoError(env.db.SetStateEntry(ctx, nil,
		usernotifications.TelegramLinkStatePrefix+token,
		&models.JSONMap{"userUid": env.user.UID, "orgUid": env.org.UID}, &past))

	rec := env.post(t, testWebhookSecret, startBody(token))
	r.Equal(http.StatusOK, rec.Code)
	r.Empty(env.telegramContacts(t))

	msgs := env.sender.messages()
	r.Len(msgs, 1)
	r.Contains(msgs[0], "expired")
	// Never leak WHICH failure it was: expired and never-existed answer the same.
	r.NotContains(strings.ToLower(msgs[0]), "unknown")
}

func TestHandleStart_UnknownTokenAnswersIdenticallyToExpired(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	env := setupEnv(t)

	r.Equal(http.StatusOK, env.post(t, testWebhookSecret, startBody("never-existed")).Code)
	r.Empty(env.telegramContacts(t))

	msgs := env.sender.messages()
	r.Len(msgs, 1)
	r.Equal(telegram.BuildExpiredLinkHTML(), msgs[0])
}

func TestHandleStart_NoTokenAtAll(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	env := setupEnv(t)

	body := `{"update_id":1,"message":{"message_id":10,` +
		`"chat":{"id":987654321,"type":"private"},"text":"/start"}}`

	r.Equal(http.StatusOK, env.post(t, testWebhookSecret, body).Code)
	r.Empty(env.telegramContacts(t))
}

// Re-linking must UPDATE the existing contact, never duplicate it — and must
// clear a "reconnect needed" state left by an earlier block.
func TestHandleStart_ReLinkUpdatesInsteadOfDuplicating(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	env := setupEnv(t)

	ctx := context.Background()

	r.Equal(http.StatusOK, env.post(t, testWebhookSecret, startBody(env.mintToken(t))).Code)

	contacts := env.telegramContacts(t)
	r.Len(contacts, 1)
	firstUID := contacts[0].UID

	// Simulate a block having un-verified the contact.
	r.NoError(env.db.ClearUserContactVerified(ctx, firstUID))
	contacts = env.telegramContacts(t)
	r.Nil(contacts[0].VerifiedAt)

	// Connect again with a fresh token and a changed Telegram username.
	body := `{"update_id":2,"message":{"message_id":11,` +
		`"from":{"id":987654321,"is_bot":false,"first_name":"Flo","username":"newhandle"},` +
		`"chat":{"id":987654321,"type":"private","username":"newhandle"},` +
		`"text":"/start ` + env.mintToken(t) + `"}}`
	r.Equal(http.StatusOK, env.post(t, testWebhookSecret, body).Code)

	contacts = env.telegramContacts(t)
	r.Len(contacts, 1, "re-linking must not create a duplicate contact")
	r.Equal(firstUID, contacts[0].UID)
	r.NotNil(contacts[0].VerifiedAt, "re-linking must clear the reconnect-needed state")
	r.Equal("@newhandle", contacts[0].Label)

	// Exactly one route, still.
	routes, err := env.db.ListUserContactsWithRoutes(ctx, env.user.UID, env.org.UID)
	r.NoError(err)

	telegramRoutes := 0

	for _, route := range routes {
		if route.Contact != nil && route.Contact.Type == models.UserContactTypeTelegram {
			telegramRoutes++
		}
	}

	r.Equal(1, telegramRoutes)
}

// A failing reply must not undo a successful link: the contact stays.
func TestHandleStart_ReplyFailureDoesNotUndoTheLink(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	env := setupEnv(t)
	env.sender.err = telegram.ErrRateLimited

	r.Equal(http.StatusOK, env.post(t, testWebhookSecret, startBody(env.mintToken(t))).Code)
	r.Len(env.telegramContacts(t), 1)
}

// --- Opt-out ----------------------------------------------------------------

func TestHandleStop_RemovesTheContact(t *testing.T) {
	t.Parallel()

	for _, command := range []string{"/stop", "/unlink", "/stop@solidping_test_bot"} {
		t.Run(command, func(t *testing.T) {
			t.Parallel()

			r := require.New(t)
			env := setupEnv(t)

			r.Equal(http.StatusOK, env.post(t, testWebhookSecret, startBody(env.mintToken(t))).Code)
			r.Len(env.telegramContacts(t), 1)

			body := `{"update_id":9,"message":{"message_id":12,` +
				`"chat":{"id":987654321,"type":"private"},"text":"` + command + `"}}`
			r.Equal(http.StatusOK, env.post(t, testWebhookSecret, body).Code)

			r.Empty(env.telegramContacts(t), "the user must be able to opt out from Telegram itself")
		})
	}
}

func TestHandleMyChatMember_BlockRemovesTheContact(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	env := setupEnv(t)

	r.Equal(http.StatusOK, env.post(t, testWebhookSecret, startBody(env.mintToken(t))).Code)
	r.Len(env.telegramContacts(t), 1)

	before := len(env.sender.messages())

	body := `{"update_id":20,"my_chat_member":{"chat":{"id":987654321,"type":"private"},` +
		`"from":{"id":987654321,"is_bot":false,"first_name":"Flo"},` +
		`"old_chat_member":{"status":"member"},"new_chat_member":{"status":"kicked"}}}`
	r.Equal(http.StatusOK, env.post(t, testWebhookSecret, body).Code)

	r.Empty(env.telegramContacts(t))
	// No reply: the bot cannot message a chat that just blocked it.
	r.Len(env.sender.messages(), before)
}

func TestHandleMyChatMember_UnblockLeavesEverythingAlone(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	env := setupEnv(t)

	r.Equal(http.StatusOK, env.post(t, testWebhookSecret, startBody(env.mintToken(t))).Code)

	body := `{"update_id":21,"my_chat_member":{"chat":{"id":987654321,"type":"private"},` +
		`"old_chat_member":{"status":"kicked"},"new_chat_member":{"status":"member"}}}`
	r.Equal(http.StatusOK, env.post(t, testWebhookSecret, body).Code)

	r.Len(env.telegramContacts(t), 1)
}

func TestHandleMessage_UnknownCommandGetsAHint(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	env := setupEnv(t)

	body := `{"update_id":30,"message":{"message_id":13,` +
		`"chat":{"id":987654321,"type":"private"},"text":"/mute 2h"}}`
	r.Equal(http.StatusOK, env.post(t, testWebhookSecret, body).Code)

	// The hint is the help text: an answer that names every command that DOES
	// work is more useful than one that only says the typed command does not.
	msgs := env.sender.messages()
	r.Len(msgs, 1)
	r.Contains(msgs[0], "/status")
	r.Contains(msgs[0], "/incidents")
	r.Contains(msgs[0], "/ack")
	r.Contains(msgs[0], "/stop")
}

// v1 delivers to one person's chat. A token redeemed in a group would bind that
// user's pages to a room full of people — and, crucially, the refusal must NOT
// burn the single-use token, so the user can retry the same link in a DM.
func TestHandleStart_RefusesGroupChatsWithoutBurningTheToken(t *testing.T) {
	t.Parallel()

	for _, chatType := range []string{"group", "supergroup", "channel"} {
		t.Run(chatType, func(t *testing.T) {
			t.Parallel()

			r := require.New(t)
			env := setupEnv(t)

			token := env.mintToken(t)

			body := `{"update_id":40,"message":{"message_id":14,` +
				`"from":{"id":42,"is_bot":false,"first_name":"Flo","username":"flo"},` +
				`"chat":{"id":987654321,"type":"` + chatType + `","title":"Ops room"},` +
				`"text":"/start ` + token + `"}}`

			r.Equal(http.StatusOK, env.post(t, testWebhookSecret, body).Code)
			r.Empty(env.telegramContacts(t), "a group chat must never become a contact")

			msgs := env.sender.messages()
			r.Len(msgs, 1)
			r.Contains(msgs[0], "direct chat")

			// The same token still works from a private chat: the refusal above
			// must not have consumed it.
			r.Equal(http.StatusOK, env.post(t, testWebhookSecret, startBody(token)).Code)
			r.Len(env.telegramContacts(t), 1)
		})
	}
}

// An update carrying no real chat must not create a contact nothing could ever
// be delivered to.
func TestHandleStart_RefusesAZeroChatID(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	env := setupEnv(t)

	token := env.mintToken(t)

	body := `{"update_id":41,"message":{"message_id":15,` +
		`"chat":{"id":0,"type":"private"},"text":"/start ` + token + `"}}`

	r.Equal(http.StatusOK, env.post(t, testWebhookSecret, body).Code)

	contacts, err := env.db.ListUserContactsByTypeValue(
		context.Background(), models.UserContactTypeTelegram, "0",
	)
	r.NoError(err)
	r.Empty(contacts)
}
