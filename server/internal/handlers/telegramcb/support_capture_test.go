package telegramcb

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/support"
)

// withSupportInbox attaches a support inbox to the env's handler, over the same
// database, and returns the service so tests can read back what was captured.
func (e *tgEnv) withSupportInbox(t *testing.T) *support.Service {
	t.Helper()

	svc := support.NewService(e.db, support.Options{BaseURL: "https://solidping.example"})
	e.handler.support = svc

	return svc
}

func (e *tgEnv) capturedThreads(t *testing.T, svc *support.Service) []*models.SupportThread {
	t.Helper()

	threads, err := svc.ListThreads(context.Background(), models.ListSupportThreadsFilter{})
	require.NoError(t, err)

	return threads
}

func (e *tgEnv) threadMessages(t *testing.T, svc *support.Service, threadUID string) []*models.SupportMessage {
	t.Helper()

	messages, err := svc.ListMessages(context.Background(), threadUID, 0)
	require.NoError(t, err)

	return messages
}

// say posts one private-chat text message from a human sender.
func (e *tgEnv) say(t *testing.T, messageID int, text string) {
	t.Helper()

	rec := e.post(t, testWebhookSecret, fmt.Sprintf(
		`{"update_id":%d,"message":{"message_id":%d,"chat":{"id":%s,"type":"private"},`+
			`"from":{"id":42,"username":"alice"},"text":%q}}`,
		messageID, messageID, testChatID, text))
	require.Equal(t, 200, rec.Code)
}

func (e *tgEnv) lastReply(t *testing.T) string {
	t.Helper()

	sent := e.sender.messages()
	require.NotEmpty(t, sent)

	return sent[len(sent)-1]
}

// TestSupportMode_ProseOutsideSupportIsNotCaptured is the reason /support
// exists: a stray message (an id lookup meant for another bot, a greeting) used
// to open a ticket. It now gets a pointer to /support and records nothing.
//
// The positive control is the same sentence sent after /support, over the same
// handler: it IS captured, so the absence above is not a broken capture path.
func TestSupportMode_ProseOutsideSupportIsNotCaptured(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	env := setupEnv(t)
	svc := env.withSupportInbox(t)

	env.say(t, 11, "@getidsbot")
	r.Empty(env.capturedThreads(t, svc), "prose outside support mode must not open a thread")
	r.Contains(env.lastReply(t), "/support", "the sender must be told how to reach a person")

	// POSITIVE CONTROL.
	env.say(t, 12, "/support")
	env.say(t, 13, "hey, are you seeing errors on the api?")

	threads := env.capturedThreads(t, svc)
	r.Len(threads, 1)
	r.Equal(models.SupportChannelTelegram, threads[0].Channel)
	r.Equal(testChatID, threads[0].ChannelIdentity)
	r.Contains(threads[0].Subject, "@alice")

	messages := env.threadMessages(t, svc, threads[0].UID)
	r.Len(messages, 2)
	r.Equal(supportOpenedBody, messages[0].Body, "a bare /support explains the thread in the inbox")
	r.Equal("hey, are you seeing errors on the api?", messages[1].Body)
}

// TestSupportMode_SupportWithAMessage opens the thread with that message and
// confirms it in chat; a later bare /support is a reminder, not a new thread.
func TestSupportMode_SupportWithAMessage(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	env := setupEnv(t)
	svc := env.withSupportInbox(t)

	env.say(t, 21, "/support my check is red\nsince noon")
	r.Contains(env.lastReply(t), "talking to the SolidPing team")
	r.Contains(env.lastReply(t), "/done", "entering the mode must say how to leave it")

	threads := env.capturedThreads(t, svc)
	r.Len(threads, 1)

	messages := env.threadMessages(t, svc, threads[0].UID)
	r.Len(messages, 1)
	r.Equal("my check is red\nsince noon", messages[0].Body)

	before := len(env.sender.messages())

	env.say(t, 22, "/support")
	r.Len(env.sender.messages(), before+1)
	r.Contains(env.lastReply(t), "already")
	r.Len(env.capturedThreads(t, svc), 1)
	r.Len(env.threadMessages(t, svc, threads[0].UID), 1, "a bare /support in support mode records nothing")

	// /support with text while in the mode is just a message, answered by
	// nobody but the operator.
	env.say(t, 23, "/support also the eu region")
	r.Len(env.sender.messages(), before+1, "a message in support mode gets no bot reply")
	r.Len(env.threadMessages(t, svc, threads[0].UID), 2)
}

// TestSupportMode_CommandsAreNeverCaptured: a known command runs, an unknown
// one is answered, neither is recorded — in or out of support mode.
func TestSupportMode_CommandsAreNeverCaptured(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	env := setupEnv(t)
	svc := env.withSupportInbox(t)

	env.say(t, 31, "/getid")
	r.Contains(env.lastReply(t), "know that command", "the user must still be told the command is unknown")
	r.Contains(env.lastReply(t), "/support", "the command list names /support")
	r.Empty(env.capturedThreads(t, svc), "an unknown command must not open a thread")

	// In support mode, too.
	env.say(t, 32, "/support hello")
	env.say(t, 33, "/stauts prod")
	r.Contains(env.lastReply(t), "know that command")

	before := len(env.sender.messages())

	env.say(t, 34, "/help")
	r.Greater(len(env.sender.messages()), before, "/help must still answer in chat")

	threads := env.capturedThreads(t, svc)
	r.Len(threads, 1)

	messages := env.threadMessages(t, svc, threads[0].UID)
	r.Len(messages, 1, "only the /support message is recorded")
	r.Equal("hello", messages[0].Body)
}

// TestSupportMode_DoneLeavesSupportMode: /done closes the thread, and the next
// message is back to the hint. The thread keeps its unread state for the
// operator.
func TestSupportMode_DoneLeavesSupportMode(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	env := setupEnv(t)
	svc := env.withSupportInbox(t)

	env.say(t, 41, "/done")
	r.Contains(env.lastReply(t), "no open support conversation")

	env.say(t, 42, "/support is the api down?")
	env.say(t, 43, "/done")
	r.Contains(env.lastReply(t), "closed")

	threads := env.capturedThreads(t, svc)
	r.Len(threads, 1)
	r.Equal(models.SupportStatusClosed, threads[0].Status)
	r.Positive(threads[0].UnreadCount, "closing from the chat must not hide the message from the operator")

	env.say(t, 44, "thanks")
	r.Contains(env.lastReply(t), "/support")
	r.Len(env.threadMessages(t, svc, threads[0].UID), 1, "after /done, prose is not captured")
}

// TestSupportMode_OperatorCloseEndsSupportMode: closing the thread in the inbox
// ends the mode, because the mode IS the live thread.
func TestSupportMode_OperatorCloseEndsSupportMode(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	env := setupEnv(t)
	svc := env.withSupportInbox(t)

	env.say(t, 51, "/support hello")

	threads := env.capturedThreads(t, svc)
	r.Len(threads, 1)

	closed := models.SupportStatusClosed
	_, err := svc.UpdateThread(context.Background(), threads[0].UID, &closed, nil)
	r.NoError(err)

	env.say(t, 52, "are you there?")
	r.Contains(env.lastReply(t), "/support")
	r.Len(env.threadMessages(t, svc, threads[0].UID), 1)
}

// TestSupportMode_BotMessagesAreNotCaptured guards the self-talking-thread
// bug: our own outbound post must never come back in as a new inbound message,
// even in support mode.
func TestSupportMode_BotMessagesAreNotCaptured(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	env := setupEnv(t)
	svc := env.withSupportInbox(t)

	env.say(t, 61, "/support hello")

	threads := env.capturedThreads(t, svc)
	r.Len(threads, 1)

	rec := env.post(t, testWebhookSecret, fmt.Sprintf(
		`{"update_id":62,"message":{"message_id":62,"chat":{"id":%s,"type":"private"},`+
			`"from":{"id":7,"username":"solidping_test_bot","is_bot":true},"text":"we are on it"}}`,
		testChatID))
	r.Equal(200, rec.Code)
	r.Len(env.threadMessages(t, svc, threads[0].UID), 1, "a bot-authored message must not be captured")

	// Positive control on the same handler: a human message IS captured.
	env.say(t, 63, "thanks")
	r.Len(env.threadMessages(t, svc, threads[0].UID), 2)
}

// TestSupportMode_WithoutAnInbox: /support never claims a message was received
// when there is nowhere to put it, and prose still gets the hint.
func TestSupportMode_WithoutAnInbox(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	env := setupEnv(t)

	env.say(t, 71, "/support hello")
	r.Contains(env.lastReply(t), "can't be received")
	r.NotContains(env.lastReply(t), "talking to the SolidPing team")

	env.say(t, 72, "hello")
	r.Contains(env.lastReply(t), "only understand commands")
}
