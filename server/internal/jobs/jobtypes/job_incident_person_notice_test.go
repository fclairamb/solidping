package jobtypes

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/jobs/jobdef"
)

// personNoticeChat is the chat the seeded thread anchor belongs to.
const personNoticeChat = resolutionChatA

func newUnackNoticeRun(env *phoneTestEnv) *IncidentPersonNoticeJobRun {
	return &IncidentPersonNoticeJobRun{notice: personNotice{
		kind:            personNoticeKindUnack,
		organizationUID: env.org.UID,
		incidentUID:     env.incident.UID,
		actorName:       "alice",
		via:             "web",
	}}
}

func newCommentNoticeRun(env *phoneTestEnv, commentUID, author, text string) *IncidentPersonNoticeJobRun {
	return &IncidentPersonNoticeJobRun{notice: personNotice{
		kind:            personNoticeKindComment,
		organizationUID: env.org.UID,
		incidentUID:     env.incident.UID,
		commentEventUID: commentUID,
		authorName:      author,
		text:            text,
	}}
}

// Job types nothing can instantiate silently never run.
func TestPersonNotice_JobTypesAreRegisteredAndValidated(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	for _, jobType := range []jobdef.JobType{
		jobdef.JobTypeIncidentUnackNotice, jobdef.JobTypeIncidentCommentNotice,
	} {
		def, ok := GetJobDefinition(jobType)
		r.True(ok, "%s must be reachable from the registry", jobType)
		r.Equal(jobType, def.Type())

		_, err := def.CreateJobRun(json.RawMessage(`{"organizationUid":"org-1","incidentUid":"inc-1"}`))
		r.NoError(err)

		_, err = def.CreateJobRun(json.RawMessage(`{"incidentUid":"inc-1"}`))
		r.ErrorIs(err, ErrMissingOrganizationUID)

		_, err = def.CreateJobRun(json.RawMessage(`{"organizationUid":"org-1"}`))
		r.ErrorIs(err, ErrMissingIncidentUID)
	}
}

// The paged person hears that the incident is unowned again — threaded under
// the very alert that woke them — and the message says escalation RESUMES,
// which it does.
func TestUnackNotice_ThreadsUnderTheAnchorAndSaysEscalationResumes(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := context.Background()

	env := setupPhoneEnv(t, false, "")
	fake, baseURL := newFakeBotAPI(t, botReply{http.StatusOK, botOK(201)})
	enableTelegram(env, baseURL)

	seedTelegramUser(t, env, true)
	seedThreadAnchor(t, env, personNoticeChat, 100)
	// The incident is deliberately left UNACKNOWLEDGED — that is the state an
	// unack puts it in, and the state the job requires.

	r.NoError(newUnackNoticeRun(env).Run(ctx, env.jctx))

	sends := fake.callsFor("sendMessage")
	r.Len(sends, 1, "exactly one retraction per paged chat")
	r.Equal(personNoticeChat, sends[0].body["chat_id"])
	r.InDelta(float64(100), sends[0].body["reply_to_message_id"], 0,
		"the notice must thread under the incident's first message")

	text, ok := sends[0].body["text"].(string)
	r.True(ok)
	r.Contains(text, "acknowledgment withdrawn by alice")
	r.Contains(text, "escalation resumes")
	r.NotContains(text, "ACKNOWLEDGED")

	// The anchor must SURVIVE: the resolution notice still needs it.
	r.True(anchorExists(t, env, personNoticeChat))
}

// A re-acknowledgment between the enqueue and the run makes the retraction
// false. Sending it anyway would tell four people to pick up an incident
// somebody is already on — the exact inversion of the harm this prevents.
func TestUnackNotice_SkipsAReacknowledgedIncident(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := context.Background()

	env := setupPhoneEnv(t, false, "")
	fake, baseURL := newFakeBotAPI(t, botReply{http.StatusOK, botOK(201)})
	enableTelegram(env, baseURL)

	seedThreadAnchor(t, env, personNoticeChat, 100)
	ackTestIncident(t, env)

	r.NoError(newUnackNoticeRun(env).Run(ctx, env.jctx))
	r.Empty(fake.callsFor("sendMessage"))
}

func TestUnackNotice_SkipsResolvedAndSuppressedIncidents(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := context.Background()

	resolved := setupPhoneEnv(t, false, "")
	resolvedFake, resolvedURL := newFakeBotAPI(t, botReply{http.StatusOK, botOK(201)})
	enableTelegram(resolved, resolvedURL)
	seedThreadAnchor(t, resolved, personNoticeChat, 100)
	resolveTestIncident(t, resolved, 5*time.Minute)

	r.NoError(newUnackNoticeRun(resolved).Run(ctx, resolved.jctx))
	r.Empty(resolvedFake.callsFor("sendMessage"),
		"a resolved incident is not unowned; the resolution notice already closed this")

	suppressed := setupPhoneEnv(t, false, "")
	suppressedFake, suppressedURL := newFakeBotAPI(t, botReply{http.StatusOK, botOK(201)})
	enableTelegram(suppressed, suppressedURL)
	seedThreadAnchor(t, suppressed, personNoticeChat, 100)

	on := true
	r.NoError(suppressed.db.UpdateIncident(ctx, suppressed.incident.UID,
		&models.IncidentUpdate{PagingSuppressed: &on}))

	r.NoError(newUnackNoticeRun(suppressed).Run(ctx, suppressed.jctx))
	r.Empty(suppressedFake.callsFor("sendMessage"),
		"a rolled-up child was never paged, so it has nothing to retract")
}

// The per-chat marker is the only thing between a retry and a duplicate.
func TestUnackNotice_SecondRunSendsNothing(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := context.Background()

	env := setupPhoneEnv(t, false, "")
	fake, baseURL := newFakeBotAPI(t, botReply{http.StatusOK, botOK(201)})
	enableTelegram(env, baseURL)

	seedThreadAnchor(t, env, personNoticeChat, 100)

	r.NoError(newUnackNoticeRun(env).Run(ctx, env.jctx))
	r.Len(fake.callsFor("sendMessage"), 1)

	r.NoError(newUnackNoticeRun(env).Run(ctx, env.jctx))
	r.Len(fake.callsFor("sendMessage"), 1, "a re-run must not duplicate the retraction")
}

// The property that makes per-comment forwarding real: a SECOND comment must
// land even though a first one already did. An incident-scoped marker — the
// discipline the ack notice uses — would suppress it, which is exactly the
// batching behavior this feature decided against.
func TestCommentNotice_EveryCommentLandsNotJustTheFirst(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := context.Background()

	env := setupPhoneEnv(t, false, "")
	fake, baseURL := newFakeBotAPI(t, botReply{http.StatusOK, botOK(201)})
	enableTelegram(env, baseURL)

	seedTelegramUser(t, env, true)
	seedThreadAnchor(t, env, personNoticeChat, 100)

	r.NoError(newCommentNoticeRun(env, "cmt-1", "alice", "looking at it").Run(ctx, env.jctx))
	r.NoError(newCommentNoticeRun(env, "cmt-2", "bob", "it is the upstream").Run(ctx, env.jctx))

	sends := fake.callsFor("sendMessage")
	r.Len(sends, 2, "each comment is forwarded on its own — no coalescing")

	first, ok := sends[0].body["text"].(string)
	r.True(ok)
	r.Contains(first, "alice commented: looking at it")

	second, ok := sends[1].body["text"].(string)
	r.True(ok)
	r.Contains(second, "bob commented: it is the upstream")

	// Re-running the SAME comment is still idempotent: per-comment, not
	// per-run.
	r.NoError(newCommentNoticeRun(env, "cmt-2", "bob", "it is the upstream").Run(ctx, env.jctx))
	r.Len(fake.callsFor("sendMessage"), 2, "a retry of one comment must not duplicate it")
}

// A comment on an ACKNOWLEDGED incident is the normal case — it is precisely
// the discussion the paged people were missing.
func TestCommentNotice_FiresOnAnAcknowledgedIncident(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := context.Background()

	env := setupPhoneEnv(t, false, "")
	fake, baseURL := newFakeBotAPI(t, botReply{http.StatusOK, botOK(201)})
	enableTelegram(env, baseURL)

	seedThreadAnchor(t, env, personNoticeChat, 100)
	ackTestIncident(t, env)

	r.NoError(newCommentNoticeRun(env, "cmt-1", "alice", "found it").Run(ctx, env.jctx))
	r.Len(fake.callsFor("sendMessage"), 1)
}

// A long comment must arrive as one readable line, not as a wall of text on a
// phone.
func TestPersonNotice_LongCommentIsTruncated(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	long := strings.Repeat("abcdefghij ", 100)

	out := truncateForChat(long, personNoticeCommentPreviewMax)
	r.LessOrEqual(len(out), personNoticeCommentPreviewMax+len("…"))
	r.Contains(out, "…")

	// Positive control: a short body is passed through untouched.
	r.Equal("all good", truncateForChat("  all   good  ", personNoticeCommentPreviewMax))
}

// The two kinds must not share a marker namespace: a retraction and a comment
// on the same incident are different messages and neither may suppress the
// other.
func TestPersonNotice_MarkerNamespacesAreDistinct(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	unack := (&IncidentPersonNoticeJobRun{notice: personNotice{kind: personNoticeKindUnack}}).
		markerKey("inc-1", "chat-1")
	commentA := (&IncidentPersonNoticeJobRun{
		notice: personNotice{kind: personNoticeKindComment, commentEventUID: "cmt-1"},
	}).markerKey("inc-1", "chat-1")
	commentB := (&IncidentPersonNoticeJobRun{
		notice: personNotice{kind: personNoticeKindComment, commentEventUID: "cmt-2"},
	}).markerKey("inc-1", "chat-1")

	r.NotEqual(unack, commentA)
	r.NotEqual(commentA, commentB, "two comments must not share one marker")
	r.NotEqual(unack, telegramAckedKey("inc-1", "chat-1"),
		"a retraction must not be suppressed by the acknowledgment's own marker")
}
