package telegram

import (
	"crypto/subtle"
	"encoding/json"
	"fmt"
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

// Update is one inbound webhook update. Only the two update types the bot
// subscribes to are decoded; anything else arrives with both fields nil and is
// acknowledged untouched.
type Update struct {
	//nolint:tagliatelle // Telegram's Bot API wire format uses snake_case.
	UpdateID int64            `json:"update_id"`
	Message  *IncomingMessage `json:"message"`
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
