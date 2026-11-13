package jobtypes

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/entitlements"
	"github.com/fclairamb/solidping/server/internal/jobs/jobdef"
)

// noticeFlavor is one of the three Telegram follow-up notices (acknowledged,
// acknowledgment withdrawn, comment). They share one delivery discipline: a
// per-chat marker, an audit row, a retry only for network-class failures.
type noticeFlavor struct {
	name string
	// prepare puts the seeded incident in the state the notice requires.
	prepare      func(t *testing.T, env *phoneTestEnv)
	run          func(orgUID, incidentUID string) jobdef.JobRunner
	markerPrefix string
	eventType    models.EventType
	incomplete   error
}

func noticeFlavors() []noticeFlavor {
	return []noticeFlavor{
		{
			name:    "ack",
			prepare: ackTestIncident,
			run: func(orgUID, incidentUID string) jobdef.JobRunner {
				return &IncidentAckNoticeJobRun{config: IncidentAckNoticeJobConfig{
					OrganizationUID: orgUID, IncidentUID: incidentUID, ActorName: "alice", Via: "web",
				}}
			},
			markerPrefix: telegramAckedStatePrefix,
			eventType:    models.EventTypeIncidentAcknowledged,
			incomplete:   ErrAckNoticeIncomplete,
		},
		{
			name:    "unack",
			prepare: func(*testing.T, *phoneTestEnv) {},
			run: func(orgUID, incidentUID string) jobdef.JobRunner {
				return &IncidentPersonNoticeJobRun{notice: personNotice{
					kind: personNoticeKindUnack, organizationUID: orgUID, incidentUID: incidentUID,
					actorName: "alice", via: "web",
				}}
			},
			markerPrefix: telegramUnackedStatePrefix,
			eventType:    models.EventTypeIncidentUnacknowledged,
			incomplete:   ErrPersonNoticeIncomplete,
		},
		{
			name:    "comment",
			prepare: func(*testing.T, *phoneTestEnv) {},
			run: func(orgUID, incidentUID string) jobdef.JobRunner {
				return &IncidentPersonNoticeJobRun{notice: personNotice{
					kind: personNoticeKindComment, organizationUID: orgUID, incidentUID: incidentUID,
					commentEventUID: "cmt-1", authorName: "alice", text: "looking at it",
				}}
			},
			markerPrefix: telegramCommentedStatePrefix,
			eventType:    models.EventTypeIncidentComment,
			incomplete:   ErrPersonNoticeIncomplete,
		},
	}
}

// liveStateEntries counts the live state entries whose key starts with prefix.
func liveStateEntries(t *testing.T, env *phoneTestEnv, prefix string) int {
	t.Helper()

	count, err := env.db.DB().NewSelect().TableExpr("state_entries").
		Where("key LIKE ?", prefix+"%").
		Where("deleted_at IS NULL").
		Count(t.Context())
	require.NoError(t, err)

	return count
}

// auditRows counts the incident's audit rows of one event type and status.
func auditRows(t *testing.T, env *phoneTestEnv, eventType models.EventType, status string) int {
	t.Helper()

	count, err := env.db.DB().NewSelect().TableExpr("incident_notifications").
		Where("incident_uid = ?", env.incident.UID).
		Where("event_type = ?", string(eventType)).
		Where("status = ?", status).
		Count(t.Context())
	require.NoError(t, err)

	return count
}

// seedPagedChat is the world every notice starts from: one person paged over
// Telegram, with the thread anchor and the audit row the page left behind.
// The anchor with an empty chat id is a corrupt row no notice may follow.
func seedPagedChat(t *testing.T, env *phoneTestEnv) {
	t.Helper()

	user := seedTelegramUser(t, env, true)
	seedPageAuditRow(t, env, user.UID)
	seedThreadAnchor(t, env, resolutionChatA, 100)
	seedThreadAnchor(t, env, "", 99)
}

// One thing breaks per case, for each of the three notices. What must hold:
// only a network-class failure asks for a retry, a chat is never told twice,
// and bookkeeping failures never cost the delivery.
func TestTelegramNotices_Failures(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		// fail returns the SQL fragments of the one query that breaks.
		fail func(flavor *noticeFlavor) []string
		// reply is what the Bot API answers; the zero value is a success.
		reply      botReply
		wantRetry  bool
		sends      int
		unthreaded bool
		markers    int
		audits     int
	}{
		{name: "control: nothing fails", sends: 1, markers: 1, audits: 1},
		{
			// Without the anchors the audit trail still finds the chat. The
			// retry is asked for because other anchored chats may be missing.
			name:      "anchor listing fails: retried, the audit trail still reaches the chat",
			fail:      func(*noticeFlavor) []string { return []string{"SELECT", telegramThreadStatePrefix} },
			wantRetry: true, sends: 1, unthreaded: true, markers: 1, audits: 1,
		},
		{
			// A second notice is a bug the user sees; a skipped one is not.
			name: "marker read fails: the chat is skipped rather than told twice",
			fail: func(flavor *noticeFlavor) []string { return []string{"SELECT", flavor.markerPrefix} },
		},
		{
			name:  "marker write fails: the notice is delivered and audited",
			fail:  func(flavor *noticeFlavor) []string { return []string{"INSERT", flavor.markerPrefix} },
			sends: 1, audits: 1,
		},
		{
			name:  "audit row insert fails: the notice is delivered and marked",
			fail:  func(*noticeFlavor) []string { return []string{`INSERT INTO "incident_notifications"`} },
			sends: 1, markers: 1,
		},
		{
			name:  "audit trail read fails: anchored chats are still notified",
			fail:  func(*noticeFlavor) []string { return []string{"FROM incident_notifications AS n"} },
			sends: 1, markers: 1, audits: 1,
		},
		{
			name:      "bot api server error: retried, nothing marked",
			reply:     botReply{http.StatusInternalServerError, botErrServer},
			wantRetry: true, sends: 1,
		},
		{
			// A blocked chat never becomes deliverable.
			name:  "bot blocked by the user: not retried, nothing marked",
			reply: botReply{http.StatusForbidden, botErrBlocked},
			sends: 1,
		},
	}

	for _, flavor := range noticeFlavors() {
		for _, tt := range tests {
			t.Run(flavor.name+"/"+tt.name, func(t *testing.T) {
				t.Parallel()

				r := require.New(t)
				env := setupPhoneEnv(t, false, "")

				reply := tt.reply
				if reply.status == 0 {
					reply = botReply{http.StatusOK, botOK(201)}
				}

				fake, baseURL := newFakeBotAPI(t, reply)
				enableTelegram(env, baseURL)
				seedPagedChat(t, env)
				flavor.prepare(t, env)

				var fault *queryFault
				if tt.fail != nil {
					fault = failQueries(env.db.DB(), tt.fail(&flavor)...)
				}

				err := flavor.run(env.org.UID, env.incident.UID).Run(t.Context(), env.jctx)

				if fault != nil {
					fault.requireFired(t)
				}

				if tt.wantRetry {
					r.ErrorIs(err, flavor.incomplete)
					r.True(jobdef.IsRetryable(err))
				} else {
					r.NoError(err)
				}

				sends := fake.callsFor("sendMessage")
				r.Len(sends, tt.sends)

				for _, send := range sends {
					r.Equal(resolutionChatA, send.body["chat_id"])

					if tt.unthreaded {
						r.Nil(send.body["reply_to_message_id"])
					} else {
						r.InDelta(float64(100), send.body["reply_to_message_id"], 0)
					}
				}

				r.Equal(tt.markers, liveStateEntries(t, env, flavor.markerPrefix))
				r.Equal(tt.audits, auditRows(t, env, flavor.eventType, models.IncidentNotificationStatusSent))
				r.True(anchorExists(t, env, resolutionChatA), "a follow-up notice never consumes the anchor")
			})
		}
	}
}

// The states in which there is nothing to send. Each one is checked against a
// Bot API that would accept the message.
func TestTelegramNotices_NothingToSend(t *testing.T) {
	t.Parallel()

	for _, flavor := range noticeFlavors() {
		t.Run(flavor.name+"/unknown incident", func(t *testing.T) {
			t.Parallel()

			env := setupPhoneEnv(t, false, "")
			fake, baseURL := newFakeBotAPI(t)
			enableTelegram(env, baseURL)
			seedPagedChat(t, env)

			err := flavor.run(env.org.UID, "no-such-incident").Run(t.Context(), env.jctx)
			require.ErrorIs(t, err, ErrIncidentNotFound)
			require.False(t, jobdef.IsRetryable(err), "a missing incident does not come back")
			require.Zero(t, fake.sendCount())
		})

		t.Run(flavor.name+"/telegram not configured", func(t *testing.T) {
			t.Parallel()

			env := setupPhoneEnv(t, false, "")
			seedPagedChat(t, env)
			flavor.prepare(t, env)

			require.NoError(t, flavor.run(env.org.UID, env.incident.UID).Run(t.Context(), env.jctx))
			require.Zero(t, liveStateEntries(t, env, flavor.markerPrefix), "nothing was delivered, nothing is marked")
		})
	}
}

// The anchor expired (7-day TTL) but the audit trail remembers the page: the
// notice goes out unthreaded, to the person's current verified chat.
func TestTelegramNotices_FallBackToTheAuditTrail(t *testing.T) {
	t.Parallel()

	for _, flavor := range noticeFlavors() {
		t.Run(flavor.name, func(t *testing.T) {
			t.Parallel()

			r := require.New(t)
			env := setupPhoneEnv(t, false, "")
			fake, baseURL := newFakeBotAPI(t, botReply{http.StatusOK, botOK(201)})
			enableTelegram(env, baseURL)

			user := seedTelegramUser(t, env, true)
			seedPageAuditRow(t, env, user.UID)
			// A second page of the same person must not produce a second notice.
			seedPageAuditRow(t, env, user.UID)
			flavor.prepare(t, env)

			r.NoError(flavor.run(env.org.UID, env.incident.UID).Run(t.Context(), env.jctx))

			sends := fake.callsFor("sendMessage")
			r.Len(sends, 1)
			r.Equal(resolutionChatA, sends[0].body["chat_id"])
			r.Nil(sends[0].body["reply_to_message_id"])
			r.Equal(1, liveStateEntries(t, env, flavor.markerPrefix))
		})
	}
}

// The hourly runaway guard bounds the fan-out. The denied chat leaves a
// skipped audit row, and the job still succeeds.
func TestTelegramNotices_RunawayGuard(t *testing.T) {
	t.Parallel()

	for _, flavor := range noticeFlavors() {
		for _, auditFails := range []bool{false, true} {
			name := flavor.name
			if auditFails {
				name += "/skip audit row cannot be written"
			}

			t.Run(name, func(t *testing.T) {
				t.Parallel()

				r := require.New(t)
				env := setupPhoneEnv(t, false, "")
				fake, baseURL := newFakeBotAPI(t)
				enableTelegram(env, baseURL)

				env.jctx.Services.Entitlements = entitlements.NewService(
					env.db, entitlements.DefaultsFor(config.DeploymentModeSelfHosted), 0,
					entitlements.WithTelegramRunawayCap(1),
				)

				seedThreadAnchor(t, env, resolutionChatA, 100)
				seedThreadAnchor(t, env, resolutionChatB, 101)
				flavor.prepare(t, env)

				var fault *queryFault
				if auditFails {
					fault = failQueries(env.db.DB(), `INSERT INTO "incident_notifications"`, "runaway_guard")
				}

				r.NoError(flavor.run(env.org.UID, env.incident.UID).Run(t.Context(), env.jctx))

				wantSkipRows := 1

				if fault != nil {
					fault.requireFired(t)

					wantSkipRows = 0
				}

				r.Equal(1, fake.sendCount(), "the guard lets one notice through")
				r.Equal(wantSkipRows, auditRows(t, env, flavor.eventType, models.IncidentNotificationStatusSkipped))
				r.Equal(1, liveStateEntries(t, env, flavor.markerPrefix), "the denied chat is not marked as told")
			})
		}
	}
}

// What the message says: who acted, and from where when that is worth saying.
func TestTelegramNoticeDetailLines(t *testing.T) {
	t.Parallel()

	unack := func(actor, via string) string {
		return (&IncidentPersonNoticeJobRun{notice: personNotice{
			kind: personNoticeKindUnack, actorName: actor, via: via,
		}}).detailLine()
	}
	comment := func(author, text string) string {
		return (&IncidentPersonNoticeJobRun{notice: personNotice{
			kind: personNoticeKindComment, authorName: author, text: text,
		}}).detailLine()
	}

	tests := []struct {
		name string
		got  string
		want string
	}{
		{
			"ack via discord",
			telegramAckDetail(&IncidentAckNoticeJobConfig{ActorName: "alice", Via: "Discord"}),
			"acknowledged by alice via Discord",
		},
		{
			"ack via telegram",
			telegramAckDetail(&IncidentAckNoticeJobConfig{ActorName: "alice", Via: " telegram "}),
			"acknowledged by alice via Telegram",
		},
		{
			"ack via email",
			telegramAckDetail(&IncidentAckNoticeJobConfig{ActorName: "alice", Via: "email"}),
			"acknowledged by alice via email",
		},
		{
			"ack via phone",
			telegramAckDetail(&IncidentAckNoticeJobConfig{ActorName: "alice", Via: "phone"}),
			"acknowledged by alice via phone",
		},
		{
			"ack via an unknown channel says nothing about it",
			telegramAckDetail(&IncidentAckNoticeJobConfig{ActorName: "alice", Via: "carrier-pigeon"}),
			"acknowledged by alice",
		},
		{
			"unack names the channel",
			unack("alice", "slack"),
			"acknowledgment withdrawn by alice via Slack — unowned again, escalation resumes",
		},
		{
			"unack without an actor",
			unack("  ", "web"),
			"acknowledgment withdrawn by Someone — unowned again, escalation resumes",
		},
		{"comment without an author", comment("", "found it"), "Someone commented: found it"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, tt.want, tt.got)
		})
	}
}

// --- Resolution notice ------------------------------------------------------

// The resolution notice claims the thread anchor before it sends, so its
// failure modes differ from the follow-up notices above.
func TestResolutionNotice_StoreFailures(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		fail []string
		// noAnchor seeds only the audit trail, so the fallback pass does the work.
		noAnchor   bool
		reply      botReply
		wantRetry  bool
		sends      int
		unthreaded bool
		markers    int
		audits     int
		anchorLeft bool
	}{
		{name: "control: nothing fails", sends: 1, markers: 1, audits: 1},
		{
			name:      "anchor listing fails: retried, the audit trail still reaches the chat",
			fail:      []string{"SELECT", telegramThreadStatePrefix},
			wantRetry: true, sends: 1, unthreaded: true, markers: 1, audits: 1, anchorLeft: true,
		},
		{
			// Without a claim there is no exclusion between two runners.
			name:       "anchor claim fails: the chat is skipped, its anchor kept",
			fail:       []string{`UPDATE "state_entries"`, telegramThreadStatePrefix},
			anchorLeft: true,
		},
		{
			name: "marker read fails: the chat is skipped rather than told twice",
			fail: []string{"SELECT", telegramResolvedStatePrefix},
		},
		{
			name:  "marker write fails: the notice is delivered and audited",
			fail:  []string{"INSERT", telegramResolvedStatePrefix},
			sends: 1, audits: 1,
		},
		{
			name:  "audit row insert fails: the notice is delivered and marked",
			fail:  []string{`INSERT INTO "incident_notifications"`},
			sends: 1, markers: 1,
		},
		{
			name:  "audit trail read fails: anchored chats are still notified",
			fail:  []string{"FROM incident_notifications AS n"},
			sends: 1, markers: 1, audits: 1,
		},
		{
			name:     "contact lookup of a paged user fails: no notice, no retry",
			fail:     []string{`FROM "user_notification_routes"`},
			noAnchor: true,
		},
		{
			// The send failed and the anchor could not be put back: the retry
			// is still asked for, it will go through the audit trail.
			name:      "failed send whose anchor cannot be restored: still retried",
			fail:      []string{"INSERT", telegramThreadStatePrefix},
			reply:     botReply{http.StatusInternalServerError, botErrServer},
			wantRetry: true, sends: 1,
		},
		{
			// The fallback never held an anchor: a failed send restores nothing.
			name:      "failed send through the audit trail: retried, no anchor appears",
			noAnchor:  true,
			reply:     botReply{http.StatusInternalServerError, botErrServer},
			wantRetry: true, sends: 1, unthreaded: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			r := require.New(t)
			env := setupPhoneEnv(t, false, "")

			reply := tt.reply
			if reply.status == 0 {
				reply = botReply{http.StatusOK, botOK(201)}
			}

			fake, baseURL := newFakeBotAPI(t, reply)
			enableTelegram(env, baseURL)

			user := seedTelegramUser(t, env, true)
			seedPageAuditRow(t, env, user.UID)

			if !tt.noAnchor {
				seedThreadAnchor(t, env, resolutionChatA, 100)
				seedThreadAnchor(t, env, "", 99)
			}

			resolveTestIncident(t, env, time.Minute)

			var fault *queryFault
			if tt.fail != nil {
				fault = failQueries(env.db.DB(), tt.fail...)
			}

			err := newResolutionRun(env).Run(t.Context(), env.jctx)

			if fault != nil {
				fault.requireFired(t)
			}

			if tt.wantRetry {
				r.ErrorIs(err, ErrResolutionNoticeIncomplete)
				r.True(jobdef.IsRetryable(err))
			} else {
				r.NoError(err)
			}

			sends := fake.callsFor("sendMessage")
			r.Len(sends, tt.sends)

			for _, send := range sends {
				r.Equal(resolutionChatA, send.body["chat_id"])

				if tt.unthreaded {
					r.Nil(send.body["reply_to_message_id"])
				} else {
					r.InDelta(float64(100), send.body["reply_to_message_id"], 0)
				}
			}

			r.Equal(tt.markers, liveStateEntries(t, env, telegramResolvedStatePrefix))
			r.Equal(tt.audits, auditRows(t, env, models.EventTypeIncidentResolved, models.IncidentNotificationStatusSent))
			r.Equal(tt.anchorLeft, anchorExists(t, env, resolutionChatA))
		})
	}
}

func TestResolutionNotice_UnknownIncident(t *testing.T) {
	t.Parallel()

	env := setupPhoneEnv(t, false, "")
	fake, baseURL := newFakeBotAPI(t)
	enableTelegram(env, baseURL)
	seedThreadAnchor(t, env, resolutionChatA, 100)

	run := &IncidentResolutionNoticeJobRun{config: IncidentResolutionNoticeJobConfig{
		OrganizationUID: env.org.UID, IncidentUID: "no-such-incident",
	}}

	err := run.Run(context.Background(), env.jctx)
	require.ErrorIs(t, err, ErrIncidentNotFound)
	require.False(t, jobdef.IsRetryable(err))
	require.Zero(t, fake.sendCount())
}

// The runaway guard denied the chat and the skip row cannot be written: the
// job still succeeds and the denied chat keeps its anchor for a later run.
func TestResolutionNotice_RunawayGuardSkipRowFails(t *testing.T) {
	t.Parallel()

	r := require.New(t)
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

	fault := failQueries(env.db.DB(), `INSERT INTO "incident_notifications"`, "runaway_guard")

	r.NoError(newResolutionRun(env).Run(t.Context(), env.jctx))
	fault.requireFired(t)

	r.Equal(1, fake.sendCount())
	r.Zero(auditRows(t, env, models.EventTypeIncidentResolved, models.IncidentNotificationStatusSkipped))
	r.True(anchorExists(t, env, resolutionChatB))
}

func TestTelegramResolvedDetail_ClosedWithoutRecovery(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	started := time.Now()
	before := started.Add(-time.Minute)
	disabled := models.ResolutionTypeCheckDisabled
	deleted := models.ResolutionTypeCheckDeleted

	r.Equal("closed because the check was disabled",
		telegramResolvedDetail(&models.Incident{StartedAt: started, ResolutionType: &disabled}))
	r.Equal("closed because the check was deleted",
		telegramResolvedDetail(&models.Incident{StartedAt: started, ResolutionType: &deleted}))
	// A resolution dated before the start (clock skew) has no duration to show.
	r.Empty(telegramResolvedDetail(&models.Incident{StartedAt: started, ResolvedAt: &before}))
}
