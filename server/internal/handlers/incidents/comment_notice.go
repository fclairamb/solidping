package incidents

import (
	"context"
	"encoding/json"
	"log/slog"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/jobs/jobdef"
	"github.com/fclairamb/solidping/server/internal/jobs/jobtypes"
)

// queueCommentPersonNotice forwards ONE comment to the people the escalation
// policy paged.
//
// Closes the asymmetry the ack fan-out exposed. Channel connections have
// received comments since the feature shipped; person contacts never did — so
// an on-call engineer woken by a Telegram page who is on none of the check's
// channels got the page, the ack notice and the resolution notice, and never a
// word of the discussion in between.
//
// ONE JOB PER COMMENT, ENQUEUED IMMEDIATELY. No batching, no coalescing
// window, no throttle (decision 2026-08-28). The notification-noise cost that
// the original v1 scope call was avoiding is real and was accepted
// deliberately: a chatty incident does buzz a phone per comment. Anything that
// delays or merges comments would change what the recipient reads — a merged
// digest is a different message from the conversation people are actually
// having — and the people being written to are the ones the system judged
// important enough to wake up. If this ever needs a limit, the runaway guard
// inside the job (reserveTelegramSend) is where it belongs, not here.
//
// Best-effort like every other fan-out: a comment that was recorded but not
// forwarded is a missed message; failing the comment itself would lose
// operator knowledge the caller has already been told was saved.
func (s *Service) queueCommentPersonNotice(
	ctx context.Context, orgUID string, incident *models.Incident,
	event *models.Event, text, author string,
) {
	// A rolled-up child was never paged, so there is nobody to forward to.
	// (Resolution is gated inside the job, where it is re-read at send time.)
	if incident.PagingSuppressed {
		slog.DebugContext(ctx, "Skipping comment person notice — paging is suppressed",
			"incidentUid", incident.UID)

		return
	}

	commentUID := ""
	if event != nil {
		commentUID = event.UID
	}

	config, err := json.Marshal(jobtypes.IncidentCommentNoticeJobConfig{
		OrganizationUID: orgUID,
		IncidentUID:     incident.UID,
		// Scopes the job's per-chat marker to THIS comment. Without it the
		// marker would be incident-scoped and every comment after the first
		// would be silently suppressed — which is precisely the batching
		// behavior this feature decided against.
		CommentEventUID: commentUID,
		AuthorName:      author,
		Text:            text,
	})
	if err != nil {
		slog.WarnContext(ctx, "Failed to marshal incident comment notice config",
			"incidentUid", incident.UID, "error", err)

		return
	}

	if _, err := s.jobsSvc.CreateJob(
		ctx, orgUID, string(jobdef.JobTypeIncidentCommentNotice), config, nil,
	); err != nil {
		slog.WarnContext(ctx, "Failed to create incident comment notice job",
			"incidentUid", incident.UID, "error", err)
	}
}
