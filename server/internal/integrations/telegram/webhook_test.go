package telegram_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/integrations/telegram"
)

func TestValidSecretToken(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	r.True(telegram.ValidSecretToken("s3cr3t", "s3cr3t"))
	r.False(telegram.ValidSecretToken("s3cr3t", "wrong"))
	r.False(telegram.ValidSecretToken("s3cr3t", ""))
	// An unconfigured secret must never accept anything — an instance that
	// forgot to set one exposes no webhook anyone can call.
	r.False(telegram.ValidSecretToken("", "anything"))
	r.False(telegram.ValidSecretToken("", ""))
	// Length differences must not pass either (constant-time compare returns 0).
	r.False(telegram.ValidSecretToken("s3cr3t", "s3cr3tt"))
}

func TestParseUpdate_Message(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	update, err := telegram.ParseUpdate([]byte(`{
		"update_id": 1,
		"message": {
			"message_id": 7,
			"from": {"id": 42, "is_bot": false, "first_name": "Flo", "username": "flo"},
			"chat": {"id": 42, "type": "private", "username": "flo"},
			"text": "/start tok123"
		}
	}`))
	r.NoError(err)
	r.NotNil(update.Message)
	r.Equal(int64(42), update.Message.Chat.ID)
	r.Equal("/start tok123", update.Message.Text)

	command, arg := telegram.Command(update.Message.Text)
	r.Equal("start", command)
	r.Equal("tok123", arg)
}

func TestParseUpdate_MessageWithoutText(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	// A sticker/photo carries no `text`; parsing must not fail.
	update, err := telegram.ParseUpdate([]byte(`{
		"update_id": 2,
		"message": {"message_id": 8, "chat": {"id": 42, "type": "private"}, "sticker": {"emoji": "🐿"}}
	}`))
	r.NoError(err)
	r.NotNil(update.Message)
	r.Empty(update.Message.Text)

	command, arg := telegram.Command(update.Message.Text)
	r.Empty(command)
	r.Empty(arg)
}

func TestParseUpdate_UnknownTypeYieldsEmptyUpdate(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	update, err := telegram.ParseUpdate([]byte(`{"update_id": 3, "edited_message": {"message_id": 9}}`))
	r.NoError(err)
	r.Nil(update.Message)
	r.Nil(update.MyChatMember)
}

func TestParseUpdate_Malformed(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	_, err := telegram.ParseUpdate([]byte(`not json`))
	r.Error(err)
}

func TestMyChatMember_BlockTransition(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	update, err := telegram.ParseUpdate([]byte(`{
		"update_id": 4,
		"my_chat_member": {
			"chat": {"id": 42, "type": "private"},
			"from": {"id": 42, "is_bot": false, "first_name": "Flo"},
			"old_chat_member": {"status": "member"},
			"new_chat_member": {"status": "kicked"}
		}
	}`))
	r.NoError(err)
	r.True(update.MyChatMember.IsBlockTransition())

	unblocked, err := telegram.ParseUpdate([]byte(`{
		"update_id": 5,
		"my_chat_member": {
			"chat": {"id": 42, "type": "private"},
			"old_chat_member": {"status": "kicked"},
			"new_chat_member": {"status": "member"}
		}
	}`))
	r.NoError(err)
	r.False(unblocked.MyChatMember.IsBlockTransition())

	var nilUpdate *telegram.MyChatMemberUpdate

	r.False(nilUpdate.IsBlockTransition())
}

func TestCommand(t *testing.T) {
	t.Parallel()

	cases := []struct {
		text    string
		command string
		arg     string
	}{
		{"/start abc123", "start", "abc123"},
		{"  /START  abc123  ", "start", "abc123"},
		{"/stop", "stop", ""},
		{"/unlink", "unlink", ""},
		// Telegram appends @botname in groups; the suffix must be stripped or
		// the same command would work in a DM and be ignored in a group.
		{"/start@solidping_bot tok", "start", "tok"},
		{"/stop@solidping_bot", "stop", ""},
		{"hello there", "", ""},
		{"", "", ""},
	}

	for _, tc := range cases {
		t.Run(tc.text, func(t *testing.T) {
			t.Parallel()

			r := require.New(t)

			command, arg := telegram.Command(tc.text)
			r.Equal(tc.command, command)
			r.Equal(tc.arg, arg)
		})
	}
}

func TestDisplayLabel(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	r.Equal("@flo", telegram.DisplayLabel(
		&telegram.Chat{ID: 42},
		&telegram.User{Username: "flo", FirstName: "Florent"},
	))
	r.Equal("Florent Clairambault", telegram.DisplayLabel(
		&telegram.Chat{ID: 42},
		&telegram.User{FirstName: "Florent", LastName: "Clairambault"},
	))
	// No user at all: fall back through the chat, and finally to the id so the
	// contacts list never renders an empty label.
	r.Equal("@chatuser", telegram.DisplayLabel(&telegram.Chat{ID: 42, Username: "chatuser"}, nil))
	r.Equal("42", telegram.DisplayLabel(&telegram.Chat{ID: 42}, nil))
	r.Empty(telegram.DisplayLabel(nil, nil))
}

func TestChatIDString(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	r.Equal("42", telegram.ChatIDString(42))
	r.Equal("-1001234567890", telegram.ChatIDString(-1001234567890))
}
