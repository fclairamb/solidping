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

// TestSupportCapture_ProseIsCapturedAndCommandsAreNot is the central assertion
// of spec 2026-08-22-02 for Telegram, together with its positive control.
//
// The negative half ("prose is no longer dropped") is worthless on its own: a
// handler that captured EVERYTHING would pass it while quietly turning every
// /status into a support ticket. So the parseable command is asserted in the
// same test, over the same handler.
func TestSupportCapture_ProseIsCapturedAndCommandsAreNot(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	env := setupEnv(t)
	svc := env.withSupportInbox(t)

	// 1. Prose. Previously: one INFO line, body discarded.
	rec := env.post(t, testWebhookSecret, fmt.Sprintf(
		`{"update_id":1,"message":{"message_id":11,"chat":{"id":%s,"type":"private"},`+
			`"from":{"id":42,"username":"alice"},"text":"hey, are you seeing errors on the api?"}}`,
		testChatID))
	r.Equal(200, rec.Code)

	threads := env.capturedThreads(t, svc)
	r.Len(threads, 1, "a prose message must be captured")
	r.Equal(models.SupportChannelTelegram, threads[0].Channel)
	r.Equal(testChatID, threads[0].ChannelIdentity)
	r.Contains(threads[0].Subject, "@alice")

	messages, err := svc.ListMessages(context.Background(), threads[0].UID, 0)
	r.NoError(err)
	r.Len(messages, 1)
	r.Equal("hey, are you seeing errors on the api?", messages[0].Body)

	// 2. POSITIVE CONTROL: a KNOWN command still executes normally and is NOT
	// captured. /help answers in-chat and creates no support message.
	before := len(env.sender.messages())

	rec = env.post(t, testWebhookSecret, fmt.Sprintf(
		`{"update_id":2,"message":{"message_id":12,"chat":{"id":%s,"type":"private"},`+
			`"from":{"id":42,"username":"alice"},"text":"/help"}}`,
		testChatID))
	r.Equal(200, rec.Code)
	r.Greater(len(env.sender.messages()), before, "/help must still answer in chat")

	messages, err = svc.ListMessages(context.Background(), threads[0].UID, 0)
	r.NoError(err)
	r.Len(messages, 1, "a parseable command must NOT be captured as a support message")
}

// TestSupportCapture_UnknownCommandBothAnswersAndCaptures pins the deliberate
// double behavior: a mistyped command is very often a person trying to talk,
// so the "unknown command" answer is kept AND the message is recorded.
func TestSupportCapture_UnknownCommandBothAnswersAndCaptures(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	env := setupEnv(t)
	svc := env.withSupportInbox(t)

	rec := env.post(t, testWebhookSecret, fmt.Sprintf(
		`{"update_id":3,"message":{"message_id":13,"chat":{"id":%s,"type":"private"},`+
			`"from":{"id":42,"username":"bob"},"text":"/stauts prod"}}`,
		testChatID))
	r.Equal(200, rec.Code)

	sent := env.sender.messages()
	r.NotEmpty(sent)
	r.Contains(sent[len(sent)-1], "know that command", "the user must still be told the command is unknown")

	threads := env.capturedThreads(t, svc)
	r.Len(threads, 1)

	messages, err := svc.ListMessages(context.Background(), threads[0].UID, 0)
	r.NoError(err)
	r.Len(messages, 1)
	r.Contains(messages[0].Body, "/stauts", "the mistyped verb is part of what the person said")
	r.Contains(messages[0].Body, "prod")
}

// TestSupportCapture_BotMessagesAreNotCaptured guards the self-talking-thread
// bug: our own outbound post must never come back in as a new inbound message.
func TestSupportCapture_BotMessagesAreNotCaptured(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	env := setupEnv(t)
	svc := env.withSupportInbox(t)

	rec := env.post(t, testWebhookSecret, fmt.Sprintf(
		`{"update_id":4,"message":{"message_id":14,"chat":{"id":%s,"type":"private"},`+
			`"from":{"id":7,"username":"solidping_test_bot","is_bot":true},"text":"we are on it"}}`,
		testChatID))
	r.Equal(200, rec.Code)

	r.Empty(env.capturedThreads(t, svc), "a bot-authored message must not open a support thread")

	// Positive control on the same handler: a human message DOES open one.
	rec = env.post(t, testWebhookSecret, fmt.Sprintf(
		`{"update_id":5,"message":{"message_id":15,"chat":{"id":%s,"type":"private"},`+
			`"from":{"id":42,"username":"alice"},"text":"thanks"}}`,
		testChatID))
	r.Equal(200, rec.Code)
	r.Len(env.capturedThreads(t, svc), 1)
}

// TestSupportCapture_WithoutAnInboxTheWebhookStillWorks proves capture is
// additive: an instance with no support inbox behaves exactly as before.
func TestSupportCapture_WithoutAnInboxTheWebhookStillWorks(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	env := setupEnv(t)

	rec := env.post(t, testWebhookSecret, fmt.Sprintf(
		`{"update_id":6,"message":{"message_id":16,"chat":{"id":%s,"type":"private"},`+
			`"from":{"id":42},"text":"hello"}}`,
		testChatID))
	r.Equal(200, rec.Code)
}
