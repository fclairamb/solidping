package incidents_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/handlers/incidents"
	"github.com/fclairamb/solidping/server/internal/jobs/jobdef"
	"github.com/fclairamb/solidping/server/internal/jobs/jobtypes"
)

// pendingCommentPersonNotices returns the decoded person-contact comment
// notices queued for an org.
func pendingCommentPersonNotices(
	t *testing.T, s *resolveSetup, orgUID string,
) []jobtypes.IncidentCommentNoticeJobConfig {
	t.Helper()

	var jobs []*models.Job

	err := s.dbSvc.DB().NewSelect().
		Model(&jobs).
		Where("organization_uid = ?", orgUID).
		Where("type = ?", string(jobdef.JobTypeIncidentCommentNotice)).
		Where("status = ?", string(models.JobStatusPending)).
		Where("deleted_at IS NULL").
		Scan(t.Context())
	require.NoError(t, err)

	out := make([]jobtypes.IncidentCommentNoticeJobConfig, 0, len(jobs))

	for _, job := range jobs {
		raw, marshalErr := json.Marshal(job.Config)
		require.NoError(t, marshalErr)

		var cfg jobtypes.IncidentCommentNoticeJobConfig

		require.NoError(t, json.Unmarshal(raw, &cfg))

		out = append(out, cfg)
	}

	return out
}

// Piece B of spec 2026-08-28-07: someone woken by a Telegram page who is on
// none of the check's channels got the page, the ack notice and the resolution
// notice — and never a word of the discussion in between.
//
// PER COMMENT, IMMEDIATELY: no batching and no coalescing window (decision
// 2026-08-28). Three comments produce three jobs, each carrying its own body
// and its own comment id — the id is what scopes the job's per-chat marker, so
// an incident-scoped one would silently collapse this back to one forwarded
// comment per incident.
func TestCommentsForwardOneJobPerComment(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := context.Background()
	s := newResolveSetup(t)

	bodies := []string{"looking at it", "it is the upstream", "rolling back"}

	for _, body := range bodies {
		_, err := s.svc.AddComment(ctx, s.org.Slug, &incidents.AddCommentRequest{
			IncidentUID: s.incident.UID,
			Text:        body,
			Source:      incidents.CommentSourceWeb,
		})
		r.NoError(err)
	}

	notices := pendingCommentPersonNotices(t, s, s.org.UID)
	r.Len(notices, len(bodies),
		"every comment is forwarded on its own — no batching, no coalescing window")

	seenText := map[string]bool{}
	seenUID := map[string]bool{}

	for _, cfg := range notices {
		r.Equal(s.incident.UID, cfg.IncidentUID)
		r.NotEmpty(cfg.CommentEventUID,
			"the comment id scopes the per-chat marker; without it only the first comment lands")
		r.False(seenUID[cfg.CommentEventUID], "each job must carry a distinct comment")
		seenUID[cfg.CommentEventUID] = true
		seenText[cfg.Text] = true
	}

	for _, body := range bodies {
		r.True(seenText[body], "the comment body travels on the job: %q", body)
	}
}

// A rolled-up child was never paged, so there is nobody to forward to.
func TestCommentPersonNoticeSkippedWhenPagingIsSuppressed(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := context.Background()
	s := newResolveSetup(t)

	suppressed := true
	r.NoError(s.dbSvc.UpdateIncident(ctx, s.incident.UID, &models.IncidentUpdate{
		PagingSuppressed: &suppressed,
	}))

	_, err := s.svc.AddComment(ctx, s.org.Slug, &incidents.AddCommentRequest{
		IncidentUID: s.incident.UID,
		Text:        "nobody was paged for this one",
		Source:      incidents.CommentSourceWeb,
	})
	r.NoError(err)

	r.Empty(pendingCommentPersonNotices(t, s, s.org.UID))

	// Positive control: the channel fan-out is a separate decision and the
	// check's own channels still hear the comment.
	r.Positive(pendingNotificationJobs(t, s.dbSvc, s.org.UID))
}

// A comment on an ACKNOWLEDGED incident is the normal case — it is exactly the
// discussion the paged people were missing — so the forward must not be gated
// on the incident being unowned.
func TestCommentPersonNoticeStillFiresOnAnAcknowledgedIncident(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := context.Background()
	s := newResolveSetup(t)

	_, err := s.svc.AcknowledgeIncident(ctx, s.org.Slug, &incidents.AcknowledgeIncidentRequest{
		IncidentUID: s.incident.UID,
		Via:         "web",
	})
	r.NoError(err)

	_, err = s.svc.AddComment(ctx, s.org.Slug, &incidents.AddCommentRequest{
		IncidentUID: s.incident.UID,
		Text:        "found it, deploying a fix",
		Source:      incidents.CommentSourceWeb,
	})
	r.NoError(err)

	r.Len(pendingCommentPersonNotices(t, s, s.org.UID), 1)
}
