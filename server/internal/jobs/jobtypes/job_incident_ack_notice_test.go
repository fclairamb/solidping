package jobtypes

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/jobs/jobdef"
)

// ackTestChat is the chat the seeded thread anchor belongs to. Same value as
// resolutionChatA so the shared seedTelegramUser fixture resolves to it.
const ackTestChat = resolutionChatA

func newAckNoticeRun(env *phoneTestEnv, actor, via string) *IncidentAckNoticeJobRun {
	return &IncidentAckNoticeJobRun{
		config: IncidentAckNoticeJobConfig{
			OrganizationUID: env.org.UID,
			IncidentUID:     env.incident.UID,
			ActorName:       actor,
			Via:             via,
		},
	}
}

// ackTestIncident flips the seeded incident to acknowledged, the state the
// notice job requires.
func ackTestIncident(t *testing.T, env *phoneTestEnv) {
	t.Helper()

	now := time.Now()
	require.NoError(t, env.db.UpdateIncident(context.Background(), env.incident.UID,
		&models.IncidentUpdate{AcknowledgedAt: &now}))
}

func ackMarkerExists(t *testing.T, env *phoneTestEnv, chatID string) bool {
	t.Helper()

	orgUID := env.org.UID
	entry, err := env.db.GetStateEntry(
		context.Background(), &orgUID, telegramAckedKey(env.incident.UID, chatID),
	)
	require.NoError(t, err)

	return entry != nil
}

func ackNotifications(t *testing.T, env *phoneTestEnv) []*models.IncidentNotificationRow {
	t.Helper()

	rows, err := env.db.ListIncidentNotifications(context.Background(), env.org.UID,
		db.ListIncidentNotificationsFilter{IncidentUID: env.incident.UID, Limit: 100})
	require.NoError(t, err)

	out := make([]*models.IncidentNotificationRow, 0, len(rows))

	for _, row := range rows {
		if row.EventType == string(models.EventTypeIncidentAcknowledged) {
			out = append(out, row)
		}
	}

	return out
}

// A job type nothing can instantiate is a job type that silently never runs.
func TestAckNotice_RegisteredAndValidated(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	def, ok := GetJobDefinition(jobdef.JobTypeIncidentAckNotice)
	r.True(ok, "the ack notice job must be reachable from the registry")
	r.Equal(jobdef.JobTypeIncidentAckNotice, def.Type())

	_, err := def.CreateJobRun(json.RawMessage(`{"organizationUid":"org-1","incidentUid":"incident-1"}`))
	r.NoError(err)

	_, err = def.CreateJobRun(json.RawMessage(`{"incidentUid":"incident-1"}`))
	r.ErrorIs(err, ErrMissingOrganizationUID)

	_, err = def.CreateJobRun(json.RawMessage(`{"organizationUid":"org-1"}`))
	r.ErrorIs(err, ErrMissingIncidentUID)
}

// The headline behavior: the chat that was paged learns a colleague took the
// incident, threaded under the very alert that woke them up.
func TestAckNotice_ThreadsUnderTheAnchorAndNamesTheActor(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := context.Background()

	env := setupPhoneEnv(t, false, "")
	fake, baseURL := newFakeBotAPI(t, botReply{http.StatusOK, botOK(201)})
	enableTelegram(env, baseURL)

	// The chat resolves to a user, so the delivery can be attributed on the
	// audit trail. No page audit row is seeded, so the fallback pass finds
	// nobody and the anchor pass is the only source of sends.
	seedTelegramUser(t, env, true)
	seedThreadAnchor(t, env, ackTestChat, 100)
	ackTestIncident(t, env)

	r.NoError(newAckNoticeRun(env, "alice", "slack").Run(ctx, env.jctx))

	sends := fake.callsFor("sendMessage")
	r.Len(sends, 1, "exactly one ack notice per paged chat")
	r.Equal(ackTestChat, sends[0].body["chat_id"])
	r.InDelta(float64(100), sends[0].body["reply_to_message_id"], 0,
		"the notice must thread under the incident's first message")

	text, ok := sends[0].body["text"].(string)
	r.True(ok)
	r.Contains(text, "✅ Acknowledged")
	r.Contains(text, "<b>Status:</b> ACKNOWLEDGED")
	r.Contains(text, "acknowledged by alice via Slack")

	// The incident is still OPEN, so the original alert must be left alone —
	// rewriting it green is what the resolution notice does, and doing it here
	// would tell the channel the outage is over.
	r.Empty(fake.callsFor("editMessageText"))

	// And the anchor must SURVIVE: the resolution notice still needs it to
	// thread the eventual all-clear under the same message.
	r.True(anchorExists(t, env, ackTestChat),
		"an ack notice must not consume the anchor the resolution notice depends on")
	r.True(ackMarkerExists(t, env, ackTestChat))

	rows := ackNotifications(t, env)
	r.Len(rows, 1, "the delivery must leave an audit row")
	r.Equal(models.IncidentNotificationStatusSent, rows[0].Status)
}

// A second run over the same incident must be a no-op: the per-chat marker is
// the only thing standing between a retry and a duplicate.
func TestAckNotice_SecondRunSendsNothing(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := context.Background()

	env := setupPhoneEnv(t, false, "")
	fake, baseURL := newFakeBotAPI(t, botReply{http.StatusOK, botOK(201)})
	enableTelegram(env, baseURL)

	seedThreadAnchor(t, env, ackTestChat, 100)
	ackTestIncident(t, env)

	r.NoError(newAckNoticeRun(env, "alice", "web").Run(ctx, env.jctx))
	r.Len(fake.callsFor("sendMessage"), 1)

	r.NoError(newAckNoticeRun(env, "alice", "web").Run(ctx, env.jctx))
	r.Len(fake.callsFor("sendMessage"), 1, "a re-run must not duplicate the notice")
}

// The acknowledgment was WITHDRAWN between the enqueue and the run. Announcing
// it anyway would stop somebody else picking the incident up — the exact
// opposite of what the notice is for.
func TestAckNotice_SkipsAWithdrawnAcknowledgment(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := context.Background()

	env := setupPhoneEnv(t, false, "")
	fake, baseURL := newFakeBotAPI(t, botReply{http.StatusOK, botOK(201)})
	enableTelegram(env, baseURL)

	seedThreadAnchor(t, env, ackTestChat, 100)
	// Deliberately NOT acknowledged — the incident is still unclaimed.

	r.NoError(newAckNoticeRun(env, "alice", "web").Run(ctx, env.jctx))
	r.Empty(fake.callsFor("sendMessage"))
}

// A resolved incident's acknowledgment is answered by the resolution notice,
// not by this one.
func TestAckNotice_SkipsAResolvedIncident(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := context.Background()

	env := setupPhoneEnv(t, false, "")
	fake, baseURL := newFakeBotAPI(t, botReply{http.StatusOK, botOK(201)})
	enableTelegram(env, baseURL)

	seedThreadAnchor(t, env, ackTestChat, 100)
	ackTestIncident(t, env)
	resolveTestIncident(t, env, 5*time.Minute)

	r.NoError(newAckNoticeRun(env, "alice", "web").Run(ctx, env.jctx))
	r.Empty(fake.callsFor("sendMessage"))
}

// A rolled-up child was never paged, so its acknowledgment would be the first
// thing these chats ever heard about it.
func TestAckNotice_SkipsASuppressedIncident(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := context.Background()

	env := setupPhoneEnv(t, false, "")
	fake, baseURL := newFakeBotAPI(t, botReply{http.StatusOK, botOK(201)})
	enableTelegram(env, baseURL)

	seedThreadAnchor(t, env, ackTestChat, 100)
	ackTestIncident(t, env)

	suppressed := true
	r.NoError(env.db.UpdateIncident(ctx, env.incident.UID,
		&models.IncidentUpdate{PagingSuppressed: &suppressed}))

	r.NoError(newAckNoticeRun(env, "alice", "web").Run(ctx, env.jctx))
	r.Empty(fake.callsFor("sendMessage"))
}

// The fallback for chats whose anchor expired: the audit trail still remembers
// the person was paged, and their current verified contact says where to reach
// them now.
func TestAckNotice_FallsBackToTheAuditTrail(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := context.Background()

	env := setupPhoneEnv(t, false, "")
	fake, baseURL := newFakeBotAPI(t, botReply{http.StatusOK, botOK(201)})
	enableTelegram(env, baseURL)

	user := seedTelegramUser(t, env, true)
	seedPageAuditRow(t, env, user.UID)
	ackTestIncident(t, env)

	// No thread anchor at all — the 7-day TTL has passed.
	r.NoError(newAckNoticeRun(env, "alice", "telegram").Run(ctx, env.jctx))

	sends := fake.callsFor("sendMessage")
	r.Len(sends, 1)
	r.Equal(ackTestChat, sends[0].body["chat_id"])
	r.Nil(sends[0].body["reply_to_message_id"],
		"a chat reached through the fallback has no message to thread under")
}

// telegramAckDetail is what the message actually says; a blank actor must
// degrade to a neutral word rather than to a dangling "acknowledged by ".
func TestTelegramAckDetail(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	r.Equal("acknowledged by alice via Slack",
		telegramAckDetail(&IncidentAckNoticeJobConfig{ActorName: "alice", Via: "slack"}))
	r.Equal("acknowledged by alice",
		telegramAckDetail(&IncidentAckNoticeJobConfig{ActorName: "alice", Via: "web"}))
	r.Equal("acknowledged by Someone",
		telegramAckDetail(&IncidentAckNoticeJobConfig{Via: "web"}))
}
