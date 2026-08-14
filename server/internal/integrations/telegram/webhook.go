package telegram

import (
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// SecretTokenHeader is the header Telegram echoes on every webhook delivery
// when the webhook was registered with a secret_token.
//
// It is the ONLY authenticity gate available: unlike Meta, Telegram does not
// sign the payload, so there is no second line of defense. That is why the
// secret must be high-entropy and why the comparison below is constant-time.
const SecretTokenHeader = "X-Telegram-Bot-Api-Secret-Token"

// ValidSecretToken reports whether the header value matches the configured
// webhook secret, in constant time.
//
// An empty configured secret is ALWAYS a failure: an instance that forgot to
// set one must expose no webhook that anybody can call, rather than one that
// everybody can.
func ValidSecretToken(configured, provided string) bool {
	if configured == "" || provided == "" {
		return false
	}

	return subtle.ConstantTimeCompare([]byte(configured), []byte(provided)) == 1
}

// ChatTypePrivate is the chat type of a one-to-one conversation with the bot.
// v1 connects private chats only — a contact is one person's chat, and binding
// a user's pages to a room full of people is a deliberate later feature rather
// than something to fall into by accident.
const ChatTypePrivate = "private"

// Chat is the subset of Telegram's Chat object we use.
type Chat struct {
	ID       int64  `json:"id"`
	Type     string `json:"type"`
	Username string `json:"username"`
	//nolint:tagliatelle // Telegram's Bot API wire format uses snake_case.
	FirstName string `json:"first_name"`
	//nolint:tagliatelle // Telegram's Bot API wire format uses snake_case.
	LastName string `json:"last_name"`
	Title    string `json:"title"`
}

// User is the subset of Telegram's User object we use.
type User struct {
	ID       int64  `json:"id"`
	Username string `json:"username"`
	//nolint:tagliatelle // Telegram's Bot API wire format uses snake_case.
	FirstName string `json:"first_name"`
	//nolint:tagliatelle // Telegram's Bot API wire format uses snake_case.
	LastName string `json:"last_name"`
	//nolint:tagliatelle // Telegram's Bot API wire format uses snake_case.
	IsBot bool `json:"is_bot"`
}

// IncomingMessage is the subset of Telegram's Message object we use.
type IncomingMessage struct {
	//nolint:tagliatelle // Telegram's Bot API wire format uses snake_case.
	MessageID int64 `json:"message_id"`
	From      *User `json:"from"`
	Chat      *Chat `json:"chat"`
	Text      string
}

// UnmarshalJSON decodes a message, tolerating the `text` field being absent
// (photos, stickers, service messages).
func (m *IncomingMessage) UnmarshalJSON(data []byte) error {
	type alias IncomingMessage

	var raw struct {
		alias
		Text string `json:"text"`
	}

	if err := json.Unmarshal(data, &raw); err != nil {
		return fmt.Errorf("parse telegram message: %w", err)
	}

	*m = IncomingMessage(raw.alias)
	m.Text = raw.Text

	return nil
}

// ChatMember is the subset of Telegram's ChatMember object we use: only the
// status matters for detecting a block.
type ChatMember struct {
	Status string `json:"status"`
	User   *User  `json:"user"`
}

// MyChatMemberUpdate reports a change to the bot's own membership in a chat.
// In a private chat, status "kicked" means the user blocked the bot.
type MyChatMemberUpdate struct {
	Chat *Chat `json:"chat"`
	From *User `json:"from"`
	//nolint:tagliatelle // Telegram's Bot API wire format uses snake_case.
	OldChatMember *ChatMember `json:"old_chat_member"`
	//nolint:tagliatelle // Telegram's Bot API wire format uses snake_case.
	NewChatMember *ChatMember `json:"new_chat_member"`
}

// CallbackQuery is an inline-button press.
//
// Message is the message the button was attached to, and it carries both the
// chat id and the message id — which is why acknowledging from a button needs
// no stored incident→message mapping to edit the alert afterwards: the press
// itself says exactly which message to rewrite.
type CallbackQuery struct {
	ID      string           `json:"id"`
	From    *User            `json:"from"`
	Message *IncomingMessage `json:"message"`
	Data    string           `json:"data"`
}

// Update is one inbound webhook update. Only the update types the bot
// subscribes to are decoded; anything else arrives with every field nil and is
// acknowledged untouched.
type Update struct {
	//nolint:tagliatelle // Telegram's Bot API wire format uses snake_case.
	UpdateID int64            `json:"update_id"`
	Message  *IncomingMessage `json:"message"`
	//nolint:tagliatelle // Telegram's Bot API wire format uses snake_case.
	CallbackQuery *CallbackQuery `json:"callback_query"`
	//nolint:tagliatelle // Telegram's Bot API wire format uses snake_case.
	MyChatMember *MyChatMemberUpdate `json:"my_chat_member"`
}

// ParseUpdate decodes one webhook body.
func ParseUpdate(body []byte) (*Update, error) {
	var update Update
	if err := json.Unmarshal(body, &update); err != nil {
		return nil, fmt.Errorf("parse telegram update: %w", err)
	}

	return &update, nil
}

// BlockedStatuses are the membership statuses that mean the bot can no longer
// message the chat. In a private chat Telegram reports a block as "kicked";
// "left" appears when the bot is removed from a group.
//
//nolint:gochecknoglobals // immutable set, effectively a constant.
var BlockedStatuses = map[string]bool{"kicked": true, "left": true}

// IsBlockTransition reports whether this membership update means the bot lost
// access to the chat.
func (u *MyChatMemberUpdate) IsBlockTransition() bool {
	if u == nil || u.NewChatMember == nil {
		return false
	}

	return BlockedStatuses[strings.ToLower(u.NewChatMember.Status)]
}

// ChatIDString renders a chat id the way it is stored on a contact: the decimal
// integer, so the value round-trips through the Bot API unchanged.
func ChatIDString(id int64) string {
	return strconv.FormatInt(id, 10)
}

// Command splits a message's text into a bot command and its argument.
// Returns ("", "") when the text is not a command.
//
// Telegram appends "@botname" to commands sent in groups, so the suffix is
// stripped — otherwise the very same /start would work in a DM and be ignored
// in a group.
func Command(text string) (string, string) {
	trimmed := strings.TrimSpace(text)
	if !strings.HasPrefix(trimmed, "/") {
		return "", ""
	}

	command, arg, _ := strings.Cut(trimmed, " ")
	command = strings.TrimPrefix(command, "/")

	if at := strings.IndexByte(command, '@'); at >= 0 {
		command = command[:at]
	}

	return strings.ToLower(command), strings.TrimSpace(arg)
}

// CallbackActionAck is the verb of the Acknowledge button's callback_data.
// The full payload is "ack:<incident uid>" — 4 + 36 bytes, comfortably inside
// Telegram's 64-byte callback_data limit, which is why the button can carry the
// UUID directly and does not need the short `#42` reference at all.
const CallbackActionAck = "ack"

// ParseCallbackData splits an inline button's callback_data into its action and
// argument. Returns ("", "") when the payload has no action.
func ParseCallbackData(data string) (string, string) {
	action, arg, _ := strings.Cut(strings.TrimSpace(data), ":")

	action = strings.ToLower(strings.TrimSpace(action))
	if action == "" {
		return "", ""
	}

	return action, strings.TrimSpace(arg)
}

// orgSlugRefRegex mirrors auth.orgSlugRegex — the canonical organization slug
// rule (3-20 chars, [a-z0-9] at both ends, [a-z0-9-] in the body). Mirrored
// rather than imported because this package must not depend on the auth
// handlers; the shape is stable and a drift would only ever cost a qualified
// reference being read as unparseable, never a wrong org.
//
//nolint:gochecknoglobals // compiled once, immutable.
var orgSlugRefRegex = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,18}[a-z0-9]$`)

// ParseIncidentRef reads an incident reference and reports whether it was one.
//
// Four forms are accepted:
//
//	42          bare number
//	#42         bare number, the way it is rendered
//	acme:42     org-qualified
//	#acme:42    org-qualified, the way it is rendered
//
// The leading '#' is optional because phone keyboards bury it, and accepted
// because copy-pasting "#42" out of a Slack message is the other half of the
// same workflow; both must work. The org slug is matched case-insensitively —
// autocapitalize on a phone keyboard would otherwise break every qualified
// reference typed on mobile — and returned lowercased, which is the only form
// an org slug is ever stored in. An empty returned slug means a bare reference,
// which the caller resolves against whichever orgs the chat is linked to.
func ParseIncidentRef(s string) (string, int64, bool) {
	trimmed := strings.TrimSpace(s)
	trimmed = strings.TrimPrefix(trimmed, "#")

	if trimmed == "" {
		return "", 0, false
	}

	orgSlug := ""

	if slug, rest, qualified := strings.Cut(trimmed, ":"); qualified {
		orgSlug = strings.ToLower(strings.TrimSpace(slug))
		if !orgSlugRefRegex.MatchString(orgSlug) {
			return "", 0, false
		}

		trimmed = strings.TrimSpace(rest)
	}

	number, err := strconv.ParseInt(trimmed, 10, 64)
	if err != nil || number <= 0 {
		return "", 0, false
	}

	return orgSlug, number, true
}

// DisplayLabel builds the human label stored on a Telegram contact: the
// @username when there is one, else the first/last name, else the numeric id.
// A contacts list showing a bare chat id would be unreadable.
func DisplayLabel(chat *Chat, from *User) string {
	if from != nil {
		if from.Username != "" {
			return "@" + from.Username
		}

		if name := strings.TrimSpace(from.FirstName + " " + from.LastName); name != "" {
			return name
		}
	}

	if chat != nil {
		if chat.Username != "" {
			return "@" + chat.Username
		}

		if name := strings.TrimSpace(chat.FirstName + " " + chat.LastName); name != "" {
			return name
		}

		if chat.Title != "" {
			return chat.Title
		}

		return ChatIDString(chat.ID)
	}

	return ""
}
