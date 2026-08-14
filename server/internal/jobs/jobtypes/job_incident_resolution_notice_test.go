package jobtypes

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/entitlements"
	"github.com/fclairamb/solidping/server/internal/jobs/jobdef"
)

// Two chats whose thread-anchor keys sort in a known order, so a scripted fake
// Bot API can attribute each reply to a specific chat.
const (
	resolutionChatA = "1000000001"
	resolutionChatB = "1000000002"
)

// resolveTestIncident flips the seeded incident to resolved, the state the
// notice job requires.
func resolveTestIncident(t *testing.T, env *phoneTestEnv, after time.Duration) *models.Incident {
	t.Helper()

	ctx := context.Background()
	state := models.IncidentStateResolved
	resolvedAt := env.incident.StartedAt.Add(after)

	require.NoError(t, env.db.UpdateIncident(ctx, env.incident.UID, &models.IncidentUpdate{
		State:      &state,
		ResolvedAt: &resolvedAt,
	}))

	incident, err := env.db.GetIncident(ctx, env.org.UID, env.incident.UID)
	require.NoError(t, err)

	return incident
}

// seedThreadAnchor writes the "this chat was paged for this incident" record the
// escalation step would have written.
func seedThreadAnchor(t *testing.T, env *phoneTestEnv, chatID string, messageID int) {
	t.Helper()

	ctx := context.Background()
	orgUID := env.org.UID
	ttl := telegramThreadTTL
	value := &models.JSONMap{telegramThreadMessageIDField: messageID}

	require.NoError(t, env.db.SetStateEntry(
		ctx, &orgUID, telegramThreadKey(env.incident.UID, chatID), value, &ttl,
	))
}

func anchorExists(t *testing.T, env *phoneTestEnv, chatID string) bool {
	t.Helper()

	orgUID := env.org.UID
	entry, err := env.db.GetStateEntry(context.Background(), &orgUID, telegramThreadKey(env.incident.UID, chatID))
	require.NoError(t, err)

	return entry != nil
}

func resolvedMarkerExists(t *testing.T, env *phoneTestEnv, chatID string) bool {
	t.Helper()

	orgUID := env.org.UID
	entry, err := env.db.GetStateEntry(context.Background(), &orgUID, telegramResolvedKey(env.incident.UID, chatID))
	require.NoError(t, err)

	return entry != nil
}

func newResolutionRun(env *phoneTestEnv) *IncidentResolutionNoticeJobRun {
	return &IncidentResolutionNoticeJobRun{
		config: IncidentResolutionNoticeJobConfig{
			OrganizationUID: env.org.UID,
			IncidentUID:     env.incident.UID,
		},
	}
}

// seedTelegramUser creates a user with a verified Telegram contact AND an
// enabled notification route, which is how the fallback path finds a chat id.
func seedTelegramUser(t *testing.T, env *phoneTestEnv, verified bool) *models.User {
	t.Helper()

	ctx := context.Background()
	chatID := resolutionChatA

	user := models.NewUser("oncall-" + chatID + "@example.com")
	require.NoError(t, env.db.CreateUser(ctx, user))

	contact := models.NewUserContact(
		user.UID, env.org.UID, models.UserContactTypeTelegram, chatID, "@oncall",
	)

	if verified {
		now := time.Now()
		contact.VerifiedAt = &now
	}

	require.NoError(t, env.db.UpsertUserContact(ctx, contact))

	stored, err := env.db.ListUserContactsByTypeValue(ctx, models.UserContactTypeTelegram, chatID)
	require.NoError(t, err)
	require.NotEmpty(t, stored)

	require.NoError(t, env.db.EnsureUserNotificationRoute(ctx, user.UID, env.org.UID, stored[0].UID))

	return user
}

// seedPageAuditRow records the audit trail the escalation step leaves when it
// pages a person over Telegram — the fallback's only evidence once the thread
// anchor has expired.
func seedPageAuditRow(t *testing.T, env *phoneTestEnv, userUID string) {
	t.Helper()

	ctx := context.Background()
	n := models.NewIncidentNotificationForUser(
		env.org.UID, env.incident.UID, string(models.EventTypeIncidentEscalated),
		models.IncidentNotificationSourceEscalationUser, userUID,
		models.UserContactTypeTelegram, nil, nil,
	)
	require.NoError(t, env.db.CreateIncidentNotification(ctx, n))
	require.NoError(t, env.db.MarkIncidentNotificationSentByUID(ctx, n.UID, time.Now(), "42"))
}

func resolutionNotifications(t *testing.T, env *phoneTestEnv) []*models.IncidentNotificationRow {
	t.Helper()

	rows, err := env.db.ListIncidentNotifications(context.Background(), env.org.UID,
		db.ListIncidentNotificationsFilter{IncidentUID: env.incident.UID, Limit: 100})
	require.NoError(t, err)

	out := make([]*models.IncidentNotificationRow, 0, len(rows))

	for _, row := range rows {
		if row.EventType == string(models.EventTypeIncidentResolved) {
			out = append(out, row)
		}
	}

	return out
}

// --- Registration -----------------------------------------------------------

// A job type nothing can instantiate is a job type that silently never runs.
func TestResolutionNotice_RegisteredAndValidated(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	def, ok := GetJobDefinition(jobdef.JobTypeIncidentResolutionNotice)
	r.True(ok, "the resolution notice job must be reachable from the registry")
	r.Equal(jobdef.JobTypeIncidentResolutionNotice, def.Type())

	_, err := def.CreateJobRun(json.RawMessage(`{"organizationUid":"org-1","incidentUid":"incident-1"}`))
	r.NoError(err)

	_, err = def.CreateJobRun(json.RawMessage(`{"incidentUid":"incident-1"}`))
	r.ErrorIs(err, ErrMissingOrganizationUID)

	_, err = def.CreateJobRun(json.RawMessage(`{"organizationUid":"org-1"}`))
	r.ErrorIs(err, ErrMissingIncidentUID)
}

// --- Happy path -------------------------------------------------------------

// The headline behavior: the chat that was paged hears the all-clear, in the
// same thread, and the original red alert stops looking live.
func TestResolutionNotice_ThreadsUnderAnchorAndEditsTheOriginal(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := context.Background()

	env := setupPhoneEnv(t, false, "")
	fake, baseURL := newFakeBotAPI(t,
		botReply{http.StatusOK, botOK(201)},
		botReply{http.StatusOK, botOK(100)},
	)
	enableTelegram(env, baseURL)

	seedThreadAnchor(t, env, resolutionChatA, 100)
	resolveTestIncident(t, env, 12*time.Minute)

	r.NoError(newResolutionRun(env).Run(ctx, env.jctx))

	sends := fake.callsFor("sendMessage")
	r.Len(sends, 1, "exactly one resolution notice per paged chat")
	r.Equal(resolutionChatA, sends[0].body["chat_id"])
	r.InDelta(float64(100), sends[0].body["reply_to_message_id"], 0,
		"the notice must thread under the incident's first message")

	text, ok := sends[0].body["text"].(string)
	r.True(ok)
	r.Contains(text, "🟢 Resolved")
	r.Contains(text, "<b>Status:</b> RESOLVED")
	r.Contains(text, "resolved after 12m0s")
	r.NotContains(sends[0].body, "reply_markup", "a resolved incident has nothing left to acknowledge")

	edits := fake.callsFor("editMessageText")
	r.Len(edits, 1, "the original red alert must be rewritten")
	r.InDelta(float64(100), edits[0].body["message_id"], 0)

	markup, ok := edits[0].body["reply_markup"].(map[string]any)
	r.True(ok, "the edit must SEND reply_markup to strip the stale Acknowledge button")
	r.Empty(markup["inline_keyboard"])

	r.False(anchorExists(t, env, resolutionChatA), "a used anchor must be dropped")
	r.True(resolvedMarkerExists(t, env, resolutionChatA), "the notified chat must be marked")
}

// A second run over the same incident must be a no-op: the anchor is gone and
// the marker is set.
func TestResolutionNotice_SecondRunSendsNothing(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := context.Background()

	env := setupPhoneEnv(t, false, "")
	fake, baseURL := newFakeBotAPI(t)
	enableTelegram(env, baseURL)

	seedThreadAnchor(t, env, resolutionChatA, 100)
	resolveTestIncident(t, env, time.Minute)

	r.NoError(newResolutionRun(env).Run(ctx, env.jctx))
	r.Equal(1, fake.sendCount())

	r.NoError(newResolutionRun(env).Run(ctx, env.jctx))
	r.Equal(1, fake.sendCount(), "a re-run must not repeat a delivered notice")
}

// The audit trail has to show the resolution notice, attributed to the person
// who was paged.
func TestResolutionNotice_RecordsASentAuditRow(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := context.Background()

	env := setupPhoneEnv(t, false, "")
	_, baseURL := newFakeBotAPI(t, botReply{http.StatusOK, botOK(201)})
	enableTelegram(env, baseURL)

	user := seedTelegramUser(t, env, true)
	seedThreadAnchor(t, env, resolutionChatA, 100)
	resolveTestIncident(t, env, time.Minute)

	r.NoError(newResolutionRun(env).Run(ctx, env.jctx))

	rows := resolutionNotifications(t, env)
	r.Len(rows, 1)
	r.Equal(models.IncidentNotificationStatusSent, rows[0].Status)
	r.Equal(models.UserContactTypeTelegram, rows[0].ChannelType)
	r.NotNil(rows[0].UserUID)
	r.Equal(user.UID, *rows[0].UserUID)
}

// --- Partial failure and retry ---------------------------------------------

// One chat failing for a network reason must not cost the others their notice,
// and the retry must reach ONLY the chat that missed out.
func TestResolutionNotice_PartialNetworkFailureRetriesOnlyTheFailedChat(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := context.Background()

	env := setupPhoneEnv(t, false, "")
	fake, baseURL := newFakeBotAPI(t,
		// Chat A (lowest key) fails transiently, chat B goes through, then its
		// resolution edit succeeds. Every later call reuses the last reply.
		botReply{http.StatusInternalServerError, botErrServer},
		botReply{http.StatusOK, botOK(202)},
		botReply{http.StatusOK, botOK(101)},
	)
	enableTelegram(env, baseURL)

	seedThreadAnchor(t, env, resolutionChatA, 100)
	seedThreadAnchor(t, env, resolutionChatB, 101)
	resolveTestIncident(t, env, time.Minute)

	err := newResolutionRun(env).Run(ctx, env.jctx)
	r.Error(err)
	r.True(jobdef.IsRetryable(err), "a network-class failure must ask for a retry")
	r.ErrorIs(err, ErrResolutionNoticeIncomplete)

	sends := fake.callsFor("sendMessage")
	r.Len(sends, 2, "the failed chat must not stop the next one")
	r.Equal(resolutionChatA, sends[0].body["chat_id"])
	r.Equal(resolutionChatB, sends[1].body["chat_id"])

	r.True(anchorExists(t, env, resolutionChatA), "a failed chat keeps its anchor")
	r.False(resolvedMarkerExists(t, env, resolutionChatA),
		"a failed send must release its marker, or the retry would be gagged")
	r.False(anchorExists(t, env, resolutionChatB))
	r.True(resolvedMarkerExists(t, env, resolutionChatB))

	// The re-run: only the chat that missed out is contacted.
	r.NoError(newResolutionRun(env).Run(ctx, env.jctx))

	sends = fake.callsFor("sendMessage")
	r.Len(sends, 3, "the re-run must send exactly one more notice")
	r.Equal(resolutionChatA, sends[2].body["chat_id"])
	r.False(anchorExists(t, env, resolutionChatA))
}

// A permanent per-contact failure (the user blocked the bot) is NOT worth
// retrying the job for — the positive control for the test above.
func TestResolutionNotice_PermanentFailureDoesNotRetry(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := context.Background()

	env := setupPhoneEnv(t, false, "")
	fake, baseURL := newFakeBotAPI(t, botReply{http.StatusForbidden, botErrBlocked})
	enableTelegram(env, baseURL)

	seedThreadAnchor(t, env, resolutionChatA, 100)
	resolveTestIncident(t, env, time.Minute)

	r.NoError(newResolutionRun(env).Run(ctx, env.jctx),
		"a blocked chat never becomes deliverable; retrying would only burn the guard")
	r.Equal(1, fake.sendCount())
	r.False(resolvedMarkerExists(t, env, resolutionChatA))
}

// --- Audit-row fallback -----------------------------------------------------

// A long incident outlives its 7-day anchor TTL. The audit trail still knows
// who was paged, and their current verified contact says where to reach them.
func TestResolutionNotice_FallsBackToAuditRowsWhenAnchorIsGone(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := context.Background()

	env := setupPhoneEnv(t, false, "")
	fake, baseURL := newFakeBotAPI(t, botReply{http.StatusOK, botOK(201)})
	enableTelegram(env, baseURL)

	user := seedTelegramUser(t, env, true)
	seedPageAuditRow(t, env, user.UID)
	// Deliberately NO thread anchor: this is the expired-TTL case.
	resolveTestIncident(t, env, 3*time.Minute)

	r.NoError(newResolutionRun(env).Run(ctx, env.jctx))

	sends := fake.callsFor("sendMessage")
	r.Len(sends, 1)
	r.Equal(resolutionChatA, sends[0].body["chat_id"])

	_, hasReply := sends[0].body["reply_to_message_id"]
	r.False(hasReply, "with no anchor the notice is standalone")
	r.Empty(fake.callsFor("editMessageText"), "there is no original message to rewrite")
	r.True(resolvedMarkerExists(t, env, resolutionChatA))

	// The marker is the whole protection here — a second run must be silent.
	r.NoError(newResolutionRun(env).Run(ctx, env.jctx))
	r.Equal(1, fake.sendCount(), "the marker must gag the audit fallback on a re-run")
}

// A chat that already got its notice through its anchor must not be contacted a
// second time through the audit fallback.
func TestResolutionNotice_AuditFallbackSkipsAnchoredChats(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := context.Background()

	env := setupPhoneEnv(t, false, "")
	fake, baseURL := newFakeBotAPI(t)
	enableTelegram(env, baseURL)

	user := seedTelegramUser(t, env, true)
	seedPageAuditRow(t, env, user.UID)
	seedThreadAnchor(t, env, resolutionChatA, 100)
	resolveTestIncident(t, env, time.Minute)

	r.NoError(newResolutionRun(env).Run(ctx, env.jctx))
	r.Equal(1, fake.sendCount(), "one chat, one notice — not one per evidence source")
}

// Someone who unlinked their chat since being paged is simply skipped: an
// un-verified contact is never messaged, resolution notice included.
func TestResolutionNotice_UnlinkedContactIsSkipped(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := context.Background()

	env := setupPhoneEnv(t, false, "")
	fake, baseURL := newFakeBotAPI(t)
	enableTelegram(env, baseURL)

	user := seedTelegramUser(t, env, false)
	seedPageAuditRow(t, env, user.UID)
	resolveTestIncident(t, env, time.Minute)

	r.NoError(newResolutionRun(env).Run(ctx, env.jctx))
	r.Equal(0, fake.sendCount(), "an un-verified contact must never be messaged")
}

// The fallback scan must not feed on this job's own output: a resolution notice
// audited as incident.resolved is not evidence that someone was PAGED.
func TestResolutionNotice_AuditFallbackIgnoresItsOwnRows(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := context.Background()

	env := setupPhoneEnv(t, false, "")
	fake, baseURL := newFakeBotAPI(t)
	enableTelegram(env, baseURL)

	user := seedTelegramUser(t, env, true)
	resolveTestIncident(t, env, time.Minute)

	// A resolved-event row exists (as this job writes), but no page ever happened.
	n := models.NewIncidentNotificationForUser(
		env.org.UID, env.incident.UID, string(models.EventTypeIncidentResolved),
		models.IncidentNotificationSourceEscalationUser, user.UID,
		models.UserContactTypeTelegram, nil, nil,
	)
	r.NoError(env.db.CreateIncidentNotification(ctx, n))
	r.NoError(env.db.MarkIncidentNotificationSentByUID(ctx, n.UID, time.Now(), "7"))

	r.NoError(newResolutionRun(env).Run(ctx, env.jctx))
	r.Equal(0, fake.sendCount(), "a resolution notice is not evidence of a page")
}

// --- Lifecycle gates --------------------------------------------------------

// A rolled-up child was never paged, so it has nothing to un-page. Paging is
// suppressed for its whole lifecycle, resolution included.
func TestResolutionNotice_PagingSuppressedSendsNothing(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := context.Background()

	env := setupPhoneEnv(t, false, "")
	fake, baseURL := newFakeBotAPI(t)
	enableTelegram(env, baseURL)

	seedThreadAnchor(t, env, resolutionChatA, 100)
	resolveTestIncident(t, env, time.Minute)

	suppressed := true
	r.NoError(env.db.UpdateIncident(ctx, env.incident.UID, &models.IncidentUpdate{
		PagingSuppressed: &suppressed,
	}))

	r.NoError(newResolutionRun(env).Run(ctx, env.jctx))
	r.Equal(0, fake.sendCount(), "a paging-suppressed incident must send nothing")
	r.True(anchorExists(t, env, resolutionChatA), "and must not consume the anchor")
}

// A relapse that beat the job to the punch: announcing "resolved" for an
// incident that is open again would be a lie. The anchor survives, so the next
// genuine resolution still notifies.
func TestResolutionNotice_ReopenedBeforeRunSendsNothing(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := context.Background()

	env := setupPhoneEnv(t, false, "")
	fake, baseURL := newFakeBotAPI(t)
	enableTelegram(env, baseURL)

	seedThreadAnchor(t, env, resolutionChatA, 100)
	// The incident is still active — exactly the state a reopen leaves it in.

	r.NoError(newResolutionRun(env).Run(ctx, env.jctx))
	r.Equal(0, fake.sendCount())
	r.True(anchorExists(t, env, resolutionChatA))

	// Once it really resolves, the notice goes out.
	resolveTestIncident(t, env, time.Minute)
	r.NoError(newResolutionRun(env).Run(ctx, env.jctx))
	r.Equal(1, fake.sendCount())
}

// Reopen semantics, end to end: resolve → notice → reopen → resolve again must
// produce EXACTLY ONE notice. A reopen never re-pages person contacts, so a
// second "resolved" message would announce the end of an outage the person was
// never told about.
func TestResolutionNotice_ReopenRelapseSendsExactlyOneNotice(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := context.Background()

	env := setupPhoneEnv(t, false, "")
	fake, baseURL := newFakeBotAPI(t)
	enableTelegram(env, baseURL)

	user := seedTelegramUser(t, env, true)
	seedPageAuditRow(t, env, user.UID)
	seedThreadAnchor(t, env, resolutionChatA, 100)

	resolveTestIncident(t, env, time.Minute)
	r.NoError(newResolutionRun(env).Run(ctx, env.jctx))
	r.Equal(1, fake.sendCount())

	// The relapse: back to active, no new page (escalation deliberately does not
	// restart), then resolved again.
	active := models.IncidentStateActive
	r.NoError(env.db.UpdateIncident(ctx, env.incident.UID, &models.IncidentUpdate{State: &active}))
	resolveTestIncident(t, env, 5*time.Minute)

	r.NoError(newResolutionRun(env).Run(ctx, env.jctx))
	r.Equal(1, fake.sendCount(),
		"a relapse the person never heard about must not produce a second all-clear")
}

// A group incident pages through the same machinery under its own UID, so the
// notice job works on it unchanged.
func TestResolutionNotice_GroupIncidentNotifies(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := context.Background()

	env := setupPhoneEnv(t, false, "")
	fake, baseURL := newFakeBotAPI(t, botReply{http.StatusOK, botOK(201)}, botReply{http.StatusOK, botOK(100)})
	enableTelegram(env, baseURL)

	group := models.NewCheckGroup(env.org.UID, "Platform", "platform")
	r.NoError(env.db.CreateCheckGroup(ctx, group))

	started := time.Now().Add(-10 * time.Minute)
	resolvedAt := started.Add(4 * time.Minute)
	groupIncident := &models.Incident{
		UID:             "incident-group",
		OrganizationUID: env.org.UID,
		CheckUID:        env.incident.CheckUID,
		CheckGroupUID:   &group.UID,
		State:           models.IncidentStateResolved,
		StartedAt:       started,
		ResolvedAt:      &resolvedAt,
	}
	r.NoError(env.db.CreateIncident(ctx, groupIncident))

	// The escalation step pages under the GROUP incident's UID, so that is what
	// the anchor is keyed by.
	orgUID := env.org.UID
	ttl := telegramThreadTTL
	r.NoError(env.db.SetStateEntry(ctx, &orgUID,
		telegramThreadKey(groupIncident.UID, resolutionChatA), &models.JSONMap{telegramThreadMessageIDField: 100}, &ttl))

	run := &IncidentResolutionNoticeJobRun{
		config: IncidentResolutionNoticeJobConfig{
			OrganizationUID: env.org.UID,
			IncidentUID:     groupIncident.UID,
		},
	}
	r.NoError(run.Run(ctx, env.jctx))

	r.Equal(1, fake.sendCount(), "a group incident's anchors are keyed by the group incident UID")

	entry, err := env.db.GetStateEntry(ctx, &orgUID, telegramThreadKey(groupIncident.UID, resolutionChatA))
	r.NoError(err)
	r.Nil(entry, "the group incident's anchor must be consumed like any other")
}

// An instance with Telegram switched off degrades to a silent no-op rather than
// a failing job.
func TestResolutionNotice_TelegramDisabledIsANoOp(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := context.Background()

	env := setupPhoneEnv(t, false, "")
	seedThreadAnchor(t, env, resolutionChatA, 100)
	resolveTestIncident(t, env, time.Minute)

	r.NoError(newResolutionRun(env).Run(ctx, env.jctx))
	r.True(anchorExists(t, env, resolutionChatA))
}

// --- Runaway guard ----------------------------------------------------------

// A mass recovery (a network blip heals and dozens of incidents resolve at once)
// must not blast unbounded sends. The denied chat gets a skipped audit row, and
// the job still succeeds.
func TestResolutionNotice_RunawayGuardSkipsWithAuditRow(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := context.Background()

	env := setupPhoneEnv(t, false, "")
	fake, baseURL := newFakeBotAPI(t)
	enableTelegram(env, baseURL)

	env.jctx.Services.Entitlements = entitlements.NewService(
		env.db, entitlements.DefaultsFor(config.DeploymentModeSelfHosted), 0,
		entitlements.WithTelegramRunawayCap(1),
	)

	seedThreadAnchor(t, env, resolutionChatA, 100)
	seedThreadAnchor(t, env, resolutionChatB, 101)
	resolveTestIncident(t, env, time.Minute)

	r.NoError(newResolutionRun(env).Run(ctx, env.jctx),
		"a guard denial is not a job failure")
	r.Equal(1, fake.sendCount(), "the hourly runaway guard bounds the fan-out")

	rows := resolutionNotifications(t, env)
	r.Len(rows, 1)
	r.Equal(models.IncidentNotificationStatusSkipped, rows[0].Status)
	r.NotNil(rows[0].SkipReason)
	r.Contains(*rows[0].SkipReason, "runaway_guard")

	// The denied chat keeps both its anchor and a clean slate, so a later run can
	// still deliver.
	r.True(anchorExists(t, env, resolutionChatB))
	r.False(resolvedMarkerExists(t, env, resolutionChatB))
}

// --- Detail rendering -------------------------------------------------------

func TestTelegramResolvedDetail(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	started := time.Now().Add(-90 * time.Minute)
	resolved := started.Add(42 * time.Minute)

	r.Equal("resolved after 42m0s", telegramResolvedDetail(&models.Incident{
		StartedAt: started, ResolvedAt: &resolved,
	}))

	// Sub-minute outages must not render as "0s".
	short := started.Add(20 * time.Second)
	r.Equal("resolved after 20s", telegramResolvedDetail(&models.Incident{
		StartedAt: started, ResolvedAt: &short,
	}))

	// No resolution timestamp: the caller keeps the alert's own detail line.
	r.Empty(telegramResolvedDetail(&models.Incident{StartedAt: started}))
}
