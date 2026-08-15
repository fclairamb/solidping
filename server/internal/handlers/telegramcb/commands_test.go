package telegramcb

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/handlers/incidents"
	"github.com/fclairamb/solidping/server/internal/integrations/telegram"
	"github.com/fclairamb/solidping/server/internal/jobs/jobsvc"
	"github.com/fclairamb/solidping/server/internal/notifier"
	"github.com/fclairamb/solidping/server/internal/utils/clock"
)

// cmdEnv is setupEnv plus a linked chat and a REAL incidents service as the
// acknowledger. A fake acker would happily "succeed" on an incident the real
// service refuses, so the ack tests run the production code path.
type cmdEnv struct {
	*tgEnv
	incidents *incidents.Service
}

func setupCommandEnv(t *testing.T, link bool) *cmdEnv {
	t.Helper()

	env := setupEnv(t)
	ctx := context.Background()

	jobs := jobsvc.NewService(env.db.DB(), env.db, notifier.NewLocalEventNotifier(), nil)
	svc := incidents.NewService(env.db, jobs, clock.Real{}, nil)

	// Rebuild the handler with the acknowledger wired in; setupEnv's plain one
	// cannot ack.
	env.handler = NewHandler(env.db, env.handler.cfg, WithSender(env.sender),
		WithAcknowledger(svc), WithCommenter(svc))

	if link {
		contact := models.NewUserContact(
			env.user.UID, env.org.UID, models.UserContactTypeTelegram, testChatID, "@oncall",
		)
		now := time.Now()
		contact.VerifiedAt = &now
		require.NoError(t, env.db.UpsertUserContact(ctx, contact))

		linked := env.telegramContacts(t)
		require.Len(t, linked, 1)
		require.NoError(t, env.db.MarkUserContactVerified(ctx, linked[0].UID, now))
	}

	return &cmdEnv{tgEnv: env, incidents: svc}
}

// seedCheck creates one enabled check.
func (e *cmdEnv) seedCheck(t *testing.T, slug string) *models.Check {
	t.Helper()

	check := models.NewCheck(e.org.UID, slug, "http")
	require.NoError(t, e.db.CreateCheck(context.Background(), check))

	return check
}

// seedIncident opens an incident that started `age` ago.
func (e *cmdEnv) seedIncident(t *testing.T, check *models.Check, age time.Duration) *models.Incident {
	t.Helper()

	incident := models.NewIncident(e.org.UID, check.UID, time.Now().Add(-age), "down")
	require.NoError(t, e.db.CreateIncident(context.Background(), incident))

	return incident
}

// command posts one typed command from the linked chat.
func (e *cmdEnv) command(t *testing.T, text string) {
	t.Helper()

	body := fmt.Sprintf(
		`{"update_id":1,"message":{"message_id":2,"from":{"id":9,"first_name":"Alice"},`+
			`"chat":{"id":%s,"type":"private"},"text":%q}}`, testChatID, text)
	require.Equal(t, 200, e.post(t, testWebhookSecret, body).Code)
}

// pressAck posts one Acknowledge button press.
func (e *cmdEnv) pressAck(t *testing.T, incidentUID, firstName string) {
	t.Helper()

	body := fmt.Sprintf(
		`{"update_id":3,"callback_query":{"id":"cb-1","from":{"id":9,"first_name":%q},`+
			`"message":{"message_id":31,"chat":{"id":%s,"type":"private"},"text":"alert"},`+
			`"data":"ack:%s"}}`, firstName, testChatID, incidentUID)
	require.Equal(t, 200, e.post(t, testWebhookSecret, body).Code)
}

// TestCommands_UnlinkedChatIsRefused is the guard: a chat bound to no account
// gets one identical answer for EVERY command, so it cannot use the bot to
// probe whether an org, a check or an incident exists.
func TestCommands_UnlinkedChatIsRefused(t *testing.T) {
	t.Parallel()

	for _, text := range []string{
		"/status", "/incidents", "/ack", "/ack #1", "/incident #1", "/help",
		"/comment", "/comment note", "/comment #1 note",
	} {
		t.Run(text, func(t *testing.T) {
			t.Parallel()

			r := require.New(t)
			env := setupCommandEnv(t, false)

			env.command(t, text)

			msgs := env.sender.messages()
			r.Len(msgs, 1)
			r.Equal(telegram.BuildNotLinkedHTML(), msgs[0])
		})
	}
}

// TestCallbackAck_UnlinkedChatIsRefused is the same guard on the button path —
// which is the one an attacker can reach without knowing any command, by
// replaying a callback payload.
func TestCallbackAck_UnlinkedChatIsRefused(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	env := setupCommandEnv(t, false)

	check := env.seedCheck(t, "api")
	incident := env.seedIncident(t, check, time.Minute)

	env.pressAck(t, incident.UID, "Mallory")

	// The callback is still ANSWERED (an unanswered query leaves the button
	// spinning), but nothing is acknowledged and the message is untouched.
	r.Len(env.sender.answeredToasts(), 1)
	r.Empty(env.sender.editedMessages())

	stored, err := env.db.GetIncident(context.Background(), env.org.UID, incident.UID)
	r.NoError(err)
	r.Nil(stored.AcknowledgedAt)
}

// TestStatus_AllUp and its sibling below cover the two shapes of /status.
func TestStatus_AllUp(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	env := setupCommandEnv(t, true)

	env.seedCheck(t, "api")
	env.seedCheck(t, "web")

	env.command(t, "/status")

	msgs := env.sender.messages()
	r.Len(msgs, 1)
	r.Contains(msgs[0], "all 2 checks up")
}

func TestStatus_WithOpenIncidents(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	env := setupCommandEnv(t, true)

	api := env.seedCheck(t, "api")
	env.seedCheck(t, "web")
	env.seedCheck(t, "database")
	env.seedIncident(t, api, 5*time.Minute)

	env.command(t, "/status")

	msgs := env.sender.messages()
	r.Len(msgs, 1)
	r.Contains(msgs[0], "1 incident open")
	r.Contains(msgs[0], "2/3 checks up")
}

// TestIncidents_ListsEachWithItsOwnAckButton pins the listing: a header plus one
// message per incident, each carrying the button for THAT incident.
func TestIncidents_ListsEachWithItsOwnAckButton(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	env := setupCommandEnv(t, true)

	api := env.seedCheck(t, "api")
	web := env.seedCheck(t, "web")
	first := env.seedIncident(t, api, 23*time.Minute)
	second := env.seedIncident(t, web, 5*time.Minute)

	env.command(t, "/incidents")

	msgs := env.sender.messages()
	keyboards := env.sender.sentKeyboards()
	r.Len(msgs, 3, "one header plus one message per incident")
	r.Contains(msgs[0], "2 incidents open")
	r.Nil(keyboards[0], "the header carries no button")

	r.Contains(msgs[1], telegram.IncidentRef(first.Number))
	r.Contains(msgs[1], "open 23m")
	r.NotNil(keyboards[1])
	r.Equal(telegram.AckCallbackData(first.UID), keyboards[1].InlineKeyboard[0][0].CallbackData)

	r.Contains(msgs[2], telegram.IncidentRef(second.Number))
	r.Equal(telegram.AckCallbackData(second.UID), keyboards[2].InlineKeyboard[0][0].CallbackData)
}

// TestIncidents_CarriesTheViewButtonWhenBaseURLConfigured proves the /incidents
// row's keyboard combines Ack and View — Acknowledge first, View second — once
// the instance has a base URL to link to.
func TestIncidents_CarriesTheViewButtonWhenBaseURLConfigured(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	env := setupCommandEnv(t, true)
	env.handler.cfg.Server.BaseURL = "https://app.example.com"

	check := env.seedCheck(t, "api")
	incident := env.seedIncident(t, check, 23*time.Minute)

	env.command(t, "/incidents")

	keyboards := env.sender.sentKeyboards()
	r.Len(keyboards, 2, "one header plus one row")
	r.NotNil(keyboards[1])
	r.Len(keyboards[1].InlineKeyboard[0], 2)
	r.Equal(telegram.AckCallbackData(incident.UID), keyboards[1].InlineKeyboard[0][0].CallbackData)
	r.Equal(
		"https://app.example.com/dash0/orgs/"+env.org.Slug+"/incidents/"+incident.UID,
		keyboards[1].InlineKeyboard[0][1].URL,
	)
}

func TestIncidents_NothingOpen(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	env := setupCommandEnv(t, true)

	env.command(t, "/incidents")

	msgs := env.sender.messages()
	r.Len(msgs, 1)
	r.Contains(msgs[0], "no open incidents")
	r.Nil(env.sender.sentKeyboards()[0])
}

// TestIncidentDetail covers /incident <#ref>, including the failure cause the
// incident recorded when it opened.
func TestIncidentDetail(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	env := setupCommandEnv(t, true)

	check := env.seedCheck(t, "api")
	incident := env.seedIncident(t, check, 2*time.Hour)
	region := "eu2"
	incident.Region = &region
	incident.Details = models.JSONMap{"failure_reason": "HTTP request failed: 503 Service Unavailable"}
	r.NoError(env.db.UpdateIncident(context.Background(), incident.UID, &models.IncidentUpdate{
		Region:  &region,
		Details: &incident.Details,
	}))

	env.command(t, "/incident "+telegram.IncidentRef(incident.Number))

	msgs := env.sender.messages()
	r.Len(msgs, 1)
	r.Contains(msgs[0], telegram.IncidentRef(incident.Number))
	r.Contains(msgs[0], "api")
	r.Contains(msgs[0], "eu2")
	r.Contains(msgs[0], "503 Service Unavailable")
	r.Contains(msgs[0], "not yet")

	// An open, unacked incident's detail reply carries the Acknowledge button —
	// every incident line, including the detail view, is tappable through to
	// the dashboard once a URL is available, but here none is configured.
	keyboards := env.sender.sentKeyboards()
	r.Len(keyboards, 1)
	r.NotNil(keyboards[0])
	r.Len(keyboards[0].InlineKeyboard[0], 1)
	r.Equal(telegram.AckCallbackData(incident.UID), keyboards[0].InlineKeyboard[0][0].CallbackData)
}

// TestIncidentDetail_AckedCarriesViewButtonOnly: once acked, /incident answers
// with the View button alone — Acknowledge would be noise, but the dashboard
// link stays useful.
func TestIncidentDetail_AckedCarriesViewButtonOnly(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	env := setupCommandEnv(t, true)
	env.handler.cfg.Server.BaseURL = "https://app.example.com"

	check := env.seedCheck(t, "api")
	incident := env.seedIncident(t, check, time.Hour)

	env.pressAck(t, incident.UID, "Alice")

	env.command(t, "/incident "+telegram.IncidentRef(incident.Number))

	// The ack flow only EDITS the original alert; /incident's own reply is a
	// fresh SendKeyboard call, so it is the last (and only) one recorded here.
	keyboards := env.sender.sentKeyboards()
	last := keyboards[len(keyboards)-1]
	r.NotNil(last)
	r.Len(last.InlineKeyboard[0], 1, "no Acknowledge button once already acked")
	r.Equal(
		"https://app.example.com/dash0/orgs/"+env.org.Slug+"/incidents/"+incident.UID,
		last.InlineKeyboard[0][0].URL,
	)
}

func TestIncidentDetail_UnknownRefAndBadRef(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	env := setupCommandEnv(t, true)

	env.command(t, "/incident #999")
	env.command(t, "/incident banana")
	env.command(t, "/incident")

	msgs := env.sender.messages()
	r.Len(msgs, 3)
	r.Contains(msgs[0], "No incident <b>#999</b>")
	r.Contains(msgs[1], "/incident #42")
	r.Contains(msgs[2], "/incident #42")
}

// TestAck_ByRef is the typed happy path.
func TestAck_ByRef(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	env := setupCommandEnv(t, true)

	check := env.seedCheck(t, "api")
	incident := env.seedIncident(t, check, time.Minute)

	env.command(t, "/ack "+telegram.IncidentRef(incident.Number))

	msgs := env.sender.messages()
	r.Len(msgs, 1)
	r.Contains(msgs[0], telegram.IncidentRef(incident.Number))
	r.Contains(msgs[0], "acknowledged")

	stored, err := env.db.GetIncident(context.Background(), env.org.UID, incident.UID)
	r.NoError(err)
	r.NotNil(stored.AcknowledgedAt)
	r.NotNil(stored.AcknowledgedBy)
	r.Equal(env.user.UID, *stored.AcknowledgedBy, "the ack is credited to the linked account")
}

// TestAck_BareWithOneOpenIncident: with exactly one candidate there is nothing
// to disambiguate, so the shortest possible command works.
func TestAck_BareWithOneOpenIncident(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	env := setupCommandEnv(t, true)

	check := env.seedCheck(t, "api")
	incident := env.seedIncident(t, check, time.Minute)

	env.command(t, "/ack")

	stored, err := env.db.GetIncident(context.Background(), env.org.UID, incident.UID)
	r.NoError(err)
	r.NotNil(stored.AcknowledgedAt)
}

// TestAck_BareWithSeveralOpenListsCandidates: guessing would silence a page for
// the WRONG outage while the real one stays unclaimed, so the bot asks.
func TestAck_BareWithSeveralOpenListsCandidates(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	env := setupCommandEnv(t, true)

	api := env.seedCheck(t, "api")
	web := env.seedCheck(t, "web")
	first := env.seedIncident(t, api, time.Minute)
	second := env.seedIncident(t, web, time.Minute)

	env.command(t, "/ack")

	msgs := env.sender.messages()
	r.Len(msgs, 1)
	r.Contains(msgs[0], "Which one?")
	r.Contains(msgs[0], telegram.IncidentRef(first.Number))
	r.Contains(msgs[0], telegram.IncidentRef(second.Number))

	for _, uid := range []string{first.UID, second.UID} {
		stored, err := env.db.GetIncident(context.Background(), env.org.UID, uid)
		r.NoError(err)
		r.Nil(stored.AcknowledgedAt, "an ambiguous /ack must acknowledge nothing")
	}
}

func TestAck_NothingOpen(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	env := setupCommandEnv(t, true)

	env.command(t, "/ack")

	msgs := env.sender.messages()
	r.Len(msgs, 1)
	r.Contains(msgs[0], "Nothing to acknowledge")
}

// TestCallbackAck_AcksEditsAndAnswers is the button-first happy path, and the
// EDIT is the assertion that matters: the toast is gone in two seconds, the
// rewritten message is what the next person scrolling the chat reads.
func TestCallbackAck_AcksEditsAndAnswers(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	env := setupCommandEnv(t, true)

	check := env.seedCheck(t, "api")
	incident := env.seedIncident(t, check, time.Minute)

	env.pressAck(t, incident.UID, "Alice")

	stored, err := env.db.GetIncident(context.Background(), env.org.UID, incident.UID)
	r.NoError(err)
	r.NotNil(stored.AcknowledgedAt)
	r.Equal(env.user.UID, *stored.AcknowledgedBy)

	toasts := env.sender.answeredToasts()
	r.Len(toasts, 1)
	r.Contains(toasts[0], "Acknowledged")

	edits := env.sender.editedMessages()
	r.Len(edits, 1)
	r.Equal(testChatID, edits[0].chatID)
	r.EqualValues(31, edits[0].messageID)
	r.Contains(edits[0].html, "Acknowledged by")
	r.Contains(edits[0].html, telegram.IncidentRef(incident.Number))
	r.NotNil(edits[0].keyboard, "the edit must SEND a keyboard to clear the button")
	r.Empty(edits[0].keyboard.InlineKeyboard, "and it must be the empty one — no base URL is configured")
}

// TestCallbackAck_EditKeepsViewButtonWhenBaseURLConfigured: once a base URL is
// available the ack edit must NOT fall back to the fully-empty marker — it
// drops only Acknowledge and keeps View, since a URL button is never stale
// noise.
func TestCallbackAck_EditKeepsViewButtonWhenBaseURLConfigured(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	env := setupCommandEnv(t, true)
	env.handler.cfg.Server.BaseURL = "https://app.example.com"

	check := env.seedCheck(t, "api")
	incident := env.seedIncident(t, check, time.Minute)

	env.pressAck(t, incident.UID, "Alice")

	edits := env.sender.editedMessages()
	r.Len(edits, 1)
	r.NotNil(edits[0].keyboard)
	r.Len(edits[0].keyboard.InlineKeyboard, 1, "the empty marker has ZERO rows; a View-only keyboard has one")
	r.Len(edits[0].keyboard.InlineKeyboard[0], 1)
	r.Equal(
		"https://app.example.com/dash0/orgs/"+env.org.Slug+"/incidents/"+incident.UID,
		edits[0].keyboard.InlineKeyboard[0][0].URL,
	)
	r.Empty(edits[0].keyboard.InlineKeyboard[0][0].CallbackData)
}

// TestCallbackAck_RecordsTheTelegramActor pins the group-chat attribution: the
// SolidPing user is credited, and the human who actually pressed the button is
// recorded alongside — which the user UID alone cannot express.
func TestCallbackAck_RecordsTheTelegramActor(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	env := setupCommandEnv(t, true)

	check := env.seedCheck(t, "api")
	incident := env.seedIncident(t, check, time.Minute)

	env.pressAck(t, incident.UID, "Bob")

	events, err := env.db.ListEvents(context.Background(), &models.ListEventsFilter{
		OrganizationUID: env.org.UID,
		IncidentUID:     &incident.UID,
	})
	r.NoError(err)

	var actor string

	for _, event := range events {
		if event.EventType != models.EventTypeIncidentAcknowledged {
			continue
		}

		if value, ok := event.Payload["acknowledged_by_telegram"].(string); ok {
			actor = value
		}
	}

	r.Equal("via Telegram (Bob)", actor)
}

// TestCallbackAck_IsIdempotent: two people pressing the same button seconds
// apart is the NORMAL case during an outage. The second press must report the
// state instead of erroring, and must not overwrite the first acker.
func TestCallbackAck_IsIdempotent(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	env := setupCommandEnv(t, true)

	check := env.seedCheck(t, "api")
	incident := env.seedIncident(t, check, time.Minute)

	env.pressAck(t, incident.UID, "Alice")

	first, err := env.db.GetIncident(context.Background(), env.org.UID, incident.UID)
	r.NoError(err)
	r.NotNil(first.AcknowledgedAt)

	env.pressAck(t, incident.UID, "Bob")

	second, err := env.db.GetIncident(context.Background(), env.org.UID, incident.UID)
	r.NoError(err)
	r.Equal(first.AcknowledgedAt.UTC(), second.AcknowledgedAt.UTC(),
		"a second press must not move the acknowledgement time")

	toasts := env.sender.answeredToasts()
	r.Len(toasts, 2)
	r.Contains(toasts[1], "Already acknowledged")

	// The message is still repainted, so the loser of the race sees the truth.
	r.Len(env.sender.editedMessages(), 2)
}

// TestCallbackAck_ResolvedIncidentIsNotAcked closes the other idempotency case:
// a button pressed on an alert whose incident has since resolved.
func TestCallbackAck_ResolvedIncidentIsNotAcked(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	env := setupCommandEnv(t, true)

	check := env.seedCheck(t, "api")
	incident := env.seedIncident(t, check, time.Hour)

	resolved := models.IncidentStateResolved
	resolvedAt := time.Now()
	r.NoError(env.db.UpdateIncident(context.Background(), incident.UID, &models.IncidentUpdate{
		State: &resolved, ResolvedAt: &resolvedAt,
	}))

	env.pressAck(t, incident.UID, "Alice")

	stored, err := env.db.GetIncident(context.Background(), env.org.UID, incident.UID)
	r.NoError(err)
	r.Nil(stored.AcknowledgedAt, "a resolved incident is never acknowledged")

	toasts := env.sender.answeredToasts()
	r.Len(toasts, 1)
	r.Contains(toasts[0], "Already resolved")
}

// TestCallbackAck_UnknownIncidentAnswersInstead proves the callback is always
// closed, even on the failure path — an unanswered query leaves the button
// spinning forever, which on a paging channel reads as a dead bot.
func TestCallbackAck_UnknownIncidentAnswersInstead(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	env := setupCommandEnv(t, true)

	env.pressAck(t, "00000000-0000-4000-8000-000000000000", "Alice")

	toasts := env.sender.answeredToasts()
	r.Len(toasts, 1)
	r.Contains(toasts[0], "no longer available")
	r.Empty(env.sender.editedMessages())
}

// TestStartWithoutTokenOnALinkedChatShowsHelp: someone re-opening the bot months
// later types /start out of habit. Answering "this link has expired" to a
// perfectly healthy connection reads like the integration broke.
func TestStartWithoutTokenOnALinkedChatShowsHelp(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	env := setupCommandEnv(t, true)

	env.command(t, "/start")

	msgs := env.sender.messages()
	r.Len(msgs, 1)
	r.Equal(telegram.BuildHelpHTML(), msgs[0])

	// And the linkage survived: /start must never be able to unlink.
	r.Len(env.telegramContacts(t), 1)
}

// TestStartWithoutTokenOnAnUnlinkedChatStillExplainsTheLink keeps the connect
// flow's answer for a chat that has nothing to show help about.
func TestStartWithoutTokenOnAnUnlinkedChatStillExplainsTheLink(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	env := setupCommandEnv(t, false)

	env.command(t, "/start")

	msgs := env.sender.messages()
	r.Len(msgs, 1)
	r.Contains(msgs[0], "expired")
	r.NotContains(msgs[0], "/incidents")
}

// TestCommandsWithBotSuffixRoute covers the "@botname" suffix Telegram appends
// in groups: the very same /status must work in a DM and in a group.
func TestCommandsWithBotSuffixRoute(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	env := setupCommandEnv(t, true)

	env.seedCheck(t, "api")
	env.command(t, "/status@solidping_test_bot")

	msgs := env.sender.messages()
	r.Len(msgs, 1)
	r.Contains(msgs[0], "all 1 checks up")
}
