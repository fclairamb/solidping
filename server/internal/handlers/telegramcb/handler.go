// Package telegramcb handles Telegram's inbound Bot API webhook: the /start
// connect flow, in-chat opt-out (/stop, /unlink) and the my_chat_member update
// that reports the bot was blocked.
//
// It is the Telegram counterpart to internal/handlers/whatsappcb, with one
// critical difference: **Telegram does not sign the payload**. Meta gives us an
// HMAC over the body; Telegram gives us only the shared secret it echoes in the
// X-Telegram-Bot-Api-Secret-Token header. That header is therefore the ONLY
// line of defense on a public endpoint that creates paging contacts, which is
// why it is compared constant-time, BEFORE the body is parsed, and why a
// missing or mismatched value is a bare 403 with no body detail — never a 400
// that tells a prober how far it got.
//
// Once authenticated the handler ALWAYS answers 200, including for update types
// it does not implement. Telegram retries any non-2xx forever, so a 500 on an
// update we will never understand becomes an infinite loop.
package telegramcb

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/handlers/usernotifications"
	"github.com/fclairamb/solidping/server/internal/integrations/telegram"
)

// maxWebhookBody caps how much we will read from an unauthenticated caller
// before the secret has been checked. Telegram updates are small; 1 MiB is
// generous and bounds the memory an attacker can make us allocate.
const maxWebhookBody = 1 << 20

// Sender is the outbound half the handler needs. Kept as an interface so tests
// inject a fake **per handler instance** rather than swapping a package-level
// function, which two parallel tests would race on.
type Sender interface {
	SendMessage(ctx context.Context, chatID, html string) error
	// SendKeyboard posts a message carrying an inline keyboard.
	SendKeyboard(ctx context.Context, chatID, html string, keyboard *telegram.InlineKeyboard) error
	// EditMessage rewrites an earlier message in place. A non-nil keyboard
	// replaces the buttons; telegram.EmptyInlineKeyboard() removes them.
	EditMessage(ctx context.Context, chatID string, messageID int64, html string,
		keyboard *telegram.InlineKeyboard) error
	// AnswerCallback closes an inline-button press with a toast.
	AnswerCallback(ctx context.Context, callbackID, text string) error
}

// clientSender is the production implementation, built per request from the
// instance config.
type clientSender struct {
	cfg config.TelegramConfig
}

// SendMessage posts one HTML message to a chat.
func (s *clientSender) SendMessage(ctx context.Context, chatID, html string) error {
	return s.SendKeyboard(ctx, chatID, html, nil)
}

// SendKeyboard posts one HTML message with an inline keyboard.
func (s *clientSender) SendKeyboard(
	ctx context.Context, chatID, html string, keyboard *telegram.InlineKeyboard,
) error {
	client, err := telegram.NewClientFromConfig(&s.cfg)
	if err != nil {
		return err
	}

	_, err = client.SendMessage(ctx, &telegram.Message{ChatID: chatID, HTML: html, ReplyMarkup: keyboard})

	return err
}

// EditMessage rewrites a message in place.
func (s *clientSender) EditMessage(
	ctx context.Context, chatID string, messageID int64, html string, keyboard *telegram.InlineKeyboard,
) error {
	client, err := telegram.NewClientFromConfig(&s.cfg)
	if err != nil {
		return err
	}

	return client.EditMessage(ctx, &telegram.Edit{
		ChatID:      chatID,
		MessageID:   messageID,
		HTML:        html,
		ReplyMarkup: keyboard,
	})
}

// AnswerCallback closes an inline-button press with a toast.
func (s *clientSender) AnswerCallback(ctx context.Context, callbackID, text string) error {
	client, err := telegram.NewClientFromConfig(&s.cfg)
	if err != nil {
		return err
	}

	return client.AnswerCallbackQuery(ctx, callbackID, text, false)
}

// Acknowledger is the incident-service seam the ack paths need. An interface
// rather than the concrete *incidents.Service so this package keeps depending
// on nothing heavier than db.Service, exactly as the Slack integration does.
type Acknowledger interface {
	AcknowledgeIncidentFromTelegram(
		ctx context.Context, orgUID, incidentUID, userUID, actor string,
	) (*models.Incident, error)
}

// Handler serves the inbound Telegram webhook.
type Handler struct {
	db     db.Service
	cfg    *config.Config
	log    *slog.Logger
	sender Sender
	acker  Acknowledger
}

// Option customizes a Handler at construction.
type Option func(*Handler)

// WithSender injects an explicit reply sender, taking precedence over the
// config-derived one. Per handler instance, never a package global.
func WithSender(sender Sender) Option {
	return func(h *Handler) { h.sender = sender }
}

// WithAcknowledger injects the incident service used by /ack and the inline
// Acknowledge button. Without it the bot still links, unlinks and answers the
// read-only commands — it simply cannot acknowledge.
func WithAcknowledger(acker Acknowledger) Option {
	return func(h *Handler) { h.acker = acker }
}

// NewHandler builds a Telegram webhook handler.
func NewHandler(dbSvc db.Service, cfg *config.Config, opts ...Option) *Handler {
	handler := &Handler{db: dbSvc, cfg: cfg, log: slog.Default()}
	for _, opt := range opts {
		opt(handler)
	}

	if handler.sender == nil {
		handler.sender = &clientSender{cfg: handler.telegramConfig()}
	}

	return handler
}

// telegramConfig returns the instance Telegram config, or the zero value.
func (h *Handler) telegramConfig() config.TelegramConfig {
	if h.cfg == nil {
		return config.TelegramConfig{}
	}

	return h.cfg.Telegram
}

// HandleUpdate processes one POST from Telegram.
func (h *Handler) HandleUpdate(writer http.ResponseWriter, req *http.Request) error {
	cfg := h.telegramConfig()

	// Cap the body BEFORE authenticating: an unauthenticated caller must not be
	// able to make us allocate arbitrarily.
	body, err := io.ReadAll(io.LimitReader(req.Body, maxWebhookBody))
	if err != nil {
		return forbidden(writer)
	}

	// Authenticate BEFORE parsing. Constant-time, and a bare 403 either way.
	if !telegram.ValidSecretToken(cfg.WebhookSecret, req.Header.Get(telegram.SecretTokenHeader)) {
		return forbidden(writer)
	}

	update, parseErr := telegram.ParseUpdate(body)
	if parseErr != nil {
		// Authenticated but unparseable: acknowledge so Telegram stops retrying
		// an update we will never understand, and leave a trace.
		h.log.WarnContext(req.Context(), "unparseable telegram update", "error", parseErr)

		return ok(writer)
	}

	h.dispatch(req.Context(), update)

	// Always 200 once authenticated — including for update types we do not
	// handle. Telegram retries non-2xx forever.
	return ok(writer)
}

// dispatch routes one update to its handler. Unknown shapes are logged and
// dropped.
func (h *Handler) dispatch(ctx context.Context, update *telegram.Update) {
	switch {
	case update.Message != nil && update.Message.Chat != nil:
		h.handleMessage(ctx, update.Message)
	case update.CallbackQuery != nil:
		h.handleCallbackQuery(ctx, update.CallbackQuery)
	case update.MyChatMember != nil:
		h.handleChatMember(ctx, update.MyChatMember)
	default:
		h.log.InfoContext(ctx, "ignoring unhandled telegram update type", "updateId", update.UpdateID)
	}
}

// handleMessage routes one inbound command.
//
// /start and /stop are the linking lifecycle and answer in ANY chat. Everything
// else reads or changes org data and is therefore gated on the chat being
// linked — see requireLinkedOrgs (resolver.go), which answers identically for
// every command so an unlinked chat cannot use the bot to probe what exists.
func (h *Handler) handleMessage(ctx context.Context, msg *telegram.IncomingMessage) {
	chatID := telegram.ChatIDString(msg.Chat.ID)
	command, arg := telegram.Command(msg.Text)

	switch command {
	case "start":
		h.handleStart(ctx, msg, chatID, arg)
	case "stop", "unlink":
		h.handleStop(ctx, chatID)
	case "help":
		h.handleHelp(ctx, chatID)
	case "status":
		h.handleStatus(ctx, chatID, arg)
	case "incidents":
		h.handleIncidents(ctx, chatID, arg)
	case "ack":
		h.handleAck(ctx, msg, chatID, arg)
	case "incident":
		h.handleIncidentDetail(ctx, chatID, arg)
	case "":
		h.log.InfoContext(ctx, "ignoring non-command telegram message", "chatId", chatID)
	default:
		h.log.InfoContext(ctx, "ignoring unknown telegram command", "chatId", chatID, "command", command)
		h.reply(ctx, chatID, telegram.BuildUnknownCommandHTML())
	}
}

// handleStart redeems a connect token and links the chat.
//
// A /start with NO token on an already-linked chat is the help text, not the
// linking flow: someone re-opening the bot months later types /start out of
// habit, and answering "this link has expired" to a perfectly healthy
// connection reads like the integration broke.
func (h *Handler) handleStart(
	ctx context.Context, msg *telegram.IncomingMessage, chatID, token string,
) {
	if token == "" {
		if contacts := h.linkedContacts(ctx, chatID); len(contacts) > 0 {
			h.reply(ctx, chatID, telegram.BuildHelpHTML())

			return
		}
	}

	if !h.chatIsConnectable(ctx, msg, chatID) {
		return
	}

	payload, err := usernotifications.ConsumeTelegramLinkToken(ctx, h.db, token)
	if err != nil {
		h.log.WarnContext(ctx, "failed to consume telegram link token", "chatId", chatID, "error", err)
		h.reply(ctx, chatID, telegram.BuildExpiredLinkHTML())

		return
	}

	if payload == nil {
		// Unknown, expired, malformed or already redeemed — all answered
		// identically so the bot cannot be used to probe live tokens.
		h.log.InfoContext(ctx, "telegram /start with no live link token", "chatId", chatID)
		h.reply(ctx, chatID, telegram.BuildExpiredLinkHTML())

		return
	}

	label := telegram.DisplayLabel(msg.Chat, msg.From)

	if linkErr := h.linkContact(ctx, payload, chatID, label); linkErr != nil {
		h.log.ErrorContext(ctx, "failed to link telegram contact",
			"chatId", chatID, "userUid", payload.UserUID, "error", linkErr)

		return
	}

	orgSlug := h.orgSlug(ctx, payload.OrgUID)

	h.log.InfoContext(ctx, "telegram chat linked",
		"chatId", chatID, "userUid", payload.UserUID, "orgUid", payload.OrgUID)
	// Naming the org in the confirmation is the whole point: a user who
	// connected the wrong account finds out now, not during an outage.
	h.reply(ctx, chatID, telegram.BuildLinkedHTML(orgSlug))
}

// chatIsConnectable rejects a /start that could not produce a usable contact,
// BEFORE the single-use token is consumed — a refusal here must leave the user
// able to retry with the same link rather than burning it.
//
// Two cases:
//
//   - A group, supergroup or channel. v1 delivers to one person's chat; group
//     routing is a deliberate later feature (which is why /setjoingroups stays
//     enabled on the bot), not something a mis-pasted link should fall into.
//   - A chat id of 0, i.e. an update that carried no real chat. Storing it
//     would create a contact nothing can ever be delivered to.
func (h *Handler) chatIsConnectable(
	ctx context.Context, msg *telegram.IncomingMessage, chatID string,
) bool {
	// An absent type is treated as private: Telegram always sends one, and
	// refusing on a field we merely failed to read would break real users.
	if msg.Chat != nil && msg.Chat.Type != "" && msg.Chat.Type != telegram.ChatTypePrivate {
		h.log.InfoContext(ctx, "ignoring telegram /start from a non-private chat",
			"chatId", chatID, "chatType", msg.Chat.Type)
		h.reply(ctx, chatID, telegram.BuildGroupNotSupportedHTML())

		return false
	}

	if !telegram.ValidChatID(chatID) {
		h.log.WarnContext(ctx, "ignoring telegram /start with an unusable chat id", "chatId", chatID)

		return false
	}

	return true
}

// errContactNotPersisted means the contact row we just upserted could not be
// read back — a bug or a lost write, never a normal outcome.
var errContactNotPersisted = errors.New("telegram contact was not persisted")

// linkContact creates or refreshes the verified telegram contact and its
// notification route.
//
// Re-linking an already-linked chat UPDATES the existing contact rather than
// creating a duplicate: UpsertUserContact keys on (user, org, type, value), so
// the second link refreshes the label and undeletes the row. The explicit
// re-verify afterwards is what turns a "reconnect needed" contact (one whose
// VerifiedAt a block cleared) back into a live route.
func (h *Handler) linkContact(
	ctx context.Context, payload *usernotifications.TelegramLinkPayload, chatID, label string,
) error {
	contact := models.NewUserContact(
		payload.UserUID, payload.OrgUID, models.UserContactTypeTelegram, chatID, label,
	)

	// Pressing Start IS the verification: it proves the user controls this chat
	// and constitutes the opt-in. There is no code round-trip to wait for.
	now := time.Now()
	contact.VerifiedAt = &now

	if err := h.db.UpsertUserContact(ctx, contact); err != nil {
		return err
	}

	// Read back the canonical row: after an upsert the UID may be the
	// pre-existing one rather than the one we just generated.
	persisted, err := h.findContact(ctx, payload, chatID)
	if err != nil {
		return err
	}

	if persisted == nil {
		return errContactNotPersisted
	}

	if verifyErr := h.db.MarkUserContactVerified(ctx, persisted.UID, now); verifyErr != nil {
		return verifyErr
	}

	return h.db.EnsureUserNotificationRoute(ctx, payload.UserUID, payload.OrgUID, persisted.UID)
}

// findContact returns this chat's contact row within the linking user's org.
func (h *Handler) findContact(
	ctx context.Context, payload *usernotifications.TelegramLinkPayload, chatID string,
) (*models.UserContact, error) {
	contacts, err := h.db.ListUserContactsByTypeValue(ctx, models.UserContactTypeTelegram, chatID)
	if err != nil {
		return nil, err
	}

	for _, contact := range contacts {
		if contact.UserUID == payload.UserUID && contact.OrganizationUID == payload.OrgUID {
			return contact, nil
		}
	}

	return nil, nil //nolint:nilnil // "no such contact" is not an error here
}

// handleStop deletes every contact bound to this chat. The user must be able to
// opt out from inside Telegram, without going back to a dashboard they may not
// even have open — and one chat can be linked in several orgs, so the opt-out
// has to reach all of them.
func (h *Handler) handleStop(ctx context.Context, chatID string) {
	removed := h.removeContactsForChat(ctx, chatID, "user sent /stop")

	h.log.InfoContext(ctx, "telegram chat unlinked by user", "chatId", chatID, "removed", removed)
	h.reply(ctx, chatID, telegram.BuildUnlinkedHTML())
}

// handleChatMember reacts to the bot being blocked or kicked: the contacts are
// removed, exactly as an explicit /stop would.
func (h *Handler) handleChatMember(ctx context.Context, update *telegram.MyChatMemberUpdate) {
	if update.Chat == nil || !update.IsBlockTransition() {
		return
	}

	chatID := telegram.ChatIDString(update.Chat.ID)
	removed := h.removeContactsForChat(ctx, chatID, "bot blocked or removed")

	// No reply: the bot cannot message a chat that just blocked it.
	h.log.InfoContext(ctx, "telegram bot blocked; contacts removed", "chatId", chatID, "removed", removed)
}

// removeContactsForChat soft-deletes every live telegram contact for a chat id.
func (h *Handler) removeContactsForChat(ctx context.Context, chatID, reason string) int {
	contacts, err := h.db.ListUserContactsByTypeValue(ctx, models.UserContactTypeTelegram, chatID)
	if err != nil {
		h.log.WarnContext(ctx, "failed to list telegram contacts for chat",
			"chatId", chatID, "reason", reason, "error", err)

		return 0
	}

	removed := 0

	for _, contact := range contacts {
		if delErr := h.db.DeleteUserContact(ctx, contact.UID); delErr != nil {
			h.log.WarnContext(ctx, "failed to remove telegram contact",
				"contactUid", contact.UID, "reason", reason, "error", delErr)

			continue
		}

		removed++
	}

	return removed
}

// orgSlug resolves an org slug for the confirmation message, falling back to
// the UID rather than failing the link over a cosmetic lookup.
func (h *Handler) orgSlug(ctx context.Context, orgUID string) string {
	org, err := h.db.GetOrganization(ctx, orgUID)
	if err != nil || org == nil {
		return orgUID
	}

	return org.Slug
}

// reply sends a best-effort in-chat message. A failure here is logged and
// dropped: the linking itself already succeeded, and Telegram would retry the
// whole update if we answered non-2xx.
func (h *Handler) reply(ctx context.Context, chatID, html string) {
	if h.sender == nil {
		return
	}

	if err := h.sender.SendMessage(ctx, chatID, html); err != nil {
		h.log.WarnContext(ctx, "failed to send telegram reply",
			"chatId", chatID, "reason", telegram.FailureReason(err), "error", err)
	}
}

func forbidden(writer http.ResponseWriter) error {
	writer.WriteHeader(http.StatusForbidden)

	return nil
}

func ok(writer http.ResponseWriter) error {
	writer.WriteHeader(http.StatusOK)

	return nil
}
