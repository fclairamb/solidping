package incidents

import (
	"context"
	"encoding/json"
	"log/slog"
	"strconv"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/jobs/jobdef"
	"github.com/fclairamb/solidping/server/internal/jobs/jobsvc"
	"github.com/fclairamb/solidping/server/internal/jobs/jobtypes"
	"github.com/fclairamb/solidping/server/internal/notifications"
)

// queueUnackNotifications retracts an acknowledgment everywhere it was
// asserted.
//
// The mirror image of queueAckNotifications, and it exists for the mirror
// reason. An ack has to be announced because otherwise the four people who
// were woken up never learn the fifth picked it up. WITHDRAWING that ack
// inverts the sentence and every word still holds: four people now believe an
// incident is owned when it is not — and unlike a stale notification scrolled
// out of view, the incident's own alert card in Slack/Discord keeps asserting
// "Acknowledged by Alice" until something rewrites it.
//
// This supersedes spec 2026-08-24-01's "unack is silent" decision. Rarity was
// never a good argument for withholding the information: a wrong belief held
// by five on-call engineers costs the same whether it is created once a month
// or once a day.
//
// Same destination set and same two filters as the ack fan-out
// (commentFanoutConnections + notifications.AcceptsEventType, which maps
// incident.unacknowledged onto the EXISTING NotifiesAcks flag — no third
// capability flag). No per-destination opt-out, no new column, no new API
// field, matching the ack decision.
//
// There is deliberately no echo-origin skip. Unack has exactly one entry point
// (POST /orgs/:org/incidents/:uid/unack, reached from dash0, the CLI and the
// API); no chat platform ships an "unacknowledge" button, so there is no
// surface whose message was already rewritten in place. If one is ever added,
// it must ship with the skip — see isAckEchoOrigin for the shape.
//
// CALL ORDER IS LOAD-BEARING, exactly as for the ack notice: this must run
// AFTER cancelPendingNotifications, whose sweep soft-deletes every pending job
// carrying the incident's UID.
func (s *Service) queueUnackNotifications(
	ctx context.Context, orgUID string, incident *models.Incident, actorUID, via string,
) {
	// Same two gates as the ack fan-out: a rolled-up child was never paged, so
	// it has no acknowledgment to retract on any channel; and on a RESOLVED
	// incident "this is unowned again, escalation resumes" is simply false —
	// the resolution notice has already closed the conversation.
	if incident.PagingSuppressed || incident.State == models.IncidentStateResolved {
		slog.DebugContext(ctx, "Skipping unack notice fan-out",
			"incidentUid", incident.UID,
			"pagingSuppressed", incident.PagingSuppressed,
			"state", incident.State)

		return
	}

	actorName := s.unackActorName(ctx, actorUID)

	// People paged over a person contact (Telegram today) are reached by their
	// own job, exactly as the ack and resolution notices do.
	s.queueTelegramUnackNotice(ctx, orgUID, incident.UID, actorName, via)

	// AckInfo is reused rather than reinvented: the attribution an unack
	// carries is the same shape as the one an ack carries (who, and from
	// where), and reusing it means every sender's actor/via resolution is the
	// code that already shipped.
	ack := &notifications.AckInfo{
		ActorName: actorName,
		Via:       via,
	}

	for _, conn := range s.commentFanoutConnections(ctx, incident) {
		if !conn.Enabled {
			continue
		}

		if !notifications.AcceptsEventType(conn.Type, string(models.EventTypeIncidentUnacknowledged)) {
			continue
		}

		s.enqueueNotificationJob(ctx, orgUID, conn, incident.UID,
			models.EventTypeIncidentUnacknowledged, &notificationExtras{Acknowledgment: ack})
	}
}

// unackActorName resolves the human label for whoever withdrew the
// acknowledgment. Simpler than ackActorName because unack has no chat-platform
// entry points to attribute: the only actor is a platform user (or nobody, for
// an API token acting without one).
func (s *Service) unackActorName(ctx context.Context, actorUID string) string {
	if actorUID == "" {
		return ""
	}

	return s.lookupUserDisplayName(ctx, actorUID)
}

// queueTelegramUnackNotice enqueues the job that closes the loop with the
// person contacts (Telegram today) paged for this incident.
//
// Same contract as queueTelegramAckNotice: a failure to enqueue is logged and
// never fails the unacknowledgment.
func (s *Service) queueTelegramUnackNotice(ctx context.Context, orgUID, incidentUID, actorName, via string) {
	config, err := json.Marshal(jobtypes.IncidentUnackNoticeJobConfig{
		OrganizationUID: orgUID,
		IncidentUID:     incidentUID,
		ActorName:       actorName,
		Via:             via,
	})
	if err != nil {
		slog.WarnContext(ctx, "Failed to marshal incident unack notice config",
			"incidentUid", incidentUID, "error", err)

		return
	}

	if _, err := s.jobsSvc.CreateJob(
		ctx, orgUID, string(jobdef.JobTypeIncidentUnackNotice), config, nil,
	); err != nil {
		slog.WarnContext(ctx, "Failed to create incident unack notice job",
			"incidentUid", incidentUID, "error", err)
	}
}

// resumeEscalationAfterUnack restarts the escalation cycle the acknowledgment
// interrupted — from the step it interrupted, NOT from step 1.
//
// Why this is required at all: unack means the incident is genuinely unowned
// again. Telling people "this is unowned" while the system itself has stopped
// paging converts a silent failure into a loud one that still depends on a
// human noticing a chat message. Decided 2026-08-28, option (c).
//
// Why it does not need a new column: the ack's sweep
// (jobsvc.CancelPendingForIncident) SOFT-deletes the pending escalation-step
// jobs, and a soft-deleted row keeps its config (stepUid, repeatIndex,
// isLastStep) and its scheduled_at. Those rows are already an exact record of
// where the cycle stood. Steps that had ALREADY fired are in a terminal status
// and are never in this set, which is precisely why undoing a mis-click cannot
// replay pages that already went out.
//
// The recreated jobs keep their original configs verbatim and are shifted as a
// block so the cycle keeps its shape: shift = max(0, now - earliest due time).
// A rung that fell due during the acknowledgment fires immediately; a rung
// still in the future keeps the wait it had left. Repeat cycles ride along on
// the untouched isLastStep/repeatIndex pair, so the policy continues from the
// cycle the ack interrupted rather than from cycle 0.
//
// CALL ORDER IS LOAD-BEARING: this must run AFTER cancelPendingNotifications,
// or the sweep would immediately cancel what it just scheduled.
//
// Best-effort throughout: a failure here is logged and never fails the
// unacknowledgment, which the caller has already been told succeeded.
func (s *Service) resumeEscalationAfterUnack(ctx context.Context, incident *models.Incident) {
	canceled, err := s.jobsSvc.ListCanceledPendingForIncident(
		ctx, incident.UID, string(jobdef.JobTypeEscalationStep))
	if err != nil {
		slog.WarnContext(ctx, "Failed to read the canceled escalation steps for an unack",
			"incidentUid", incident.UID, "error", err)

		return
	}

	steps := dedupeCanceledEscalationSteps(ctx, canceled)
	if len(steps) == 0 {
		// No policy, or the cycle had already run itself out before the ack.
		// Unack never STARTS an escalation that was not running.
		slog.DebugContext(ctx, "No interrupted escalation cycle to resume",
			"incidentUid", incident.UID)

		return
	}

	shift := s.clock.Now().Sub(steps[0].job.ScheduledAt)
	if shift < 0 {
		shift = 0
	}

	for _, step := range steps {
		fireAt := step.job.ScheduledAt.Add(shift)

		if _, err := s.jobsSvc.CreateJob(
			ctx, orgUIDOrEmpty(step.job), string(jobdef.JobTypeEscalationStep), step.rawConfig,
			&jobsvc.JobOptions{ScheduledAt: &fireAt},
		); err != nil {
			slog.WarnContext(ctx, "Failed to resume an escalation step after an unack",
				"incidentUid", incident.UID, "stepUid", step.config.StepUID, "error", err)

			continue
		}

		slog.InfoContext(ctx, "Resumed escalation step after an unack",
			"incidentUid", incident.UID,
			"stepUid", step.config.StepUID,
			"repeatIndex", step.config.RepeatIndex,
			"fireAt", fireAt)
	}
}

// canceledEscalationStep pairs a canceled job row with its decoded config. The
// raw config is kept so the resumed job is created with the ORIGINAL bytes —
// re-marshalling would silently drop any field this build does not know about.
type canceledEscalationStep struct {
	job       *models.Job
	config    jobtypes.EscalationStepJobConfig
	rawConfig json.RawMessage
}

// dedupeCanceledEscalationSteps decodes the canceled rows and keeps one entry
// per (repeatIndex, stepUid), the earliest-scheduled one, ordered by due time.
//
// The de-duplication is not theoretical: an ack → unack → ack sequence leaves
// TWO canceled generations of the same rung behind, and resuming both would
// page the same person twice for one rung. Rows whose config no longer parses
// are dropped rather than guessed at.
func dedupeCanceledEscalationSteps(ctx context.Context, jobs []*models.Job) []canceledEscalationStep {
	out := make([]canceledEscalationStep, 0, len(jobs))
	seen := make(map[string]bool, len(jobs))

	for _, job := range jobs {
		raw, err := json.Marshal(job.Config)
		if err != nil {
			slog.WarnContext(ctx, "Skipping an unreadable canceled escalation step",
				"jobUid", job.UID, "error", err)

			continue
		}

		var cfg jobtypes.EscalationStepJobConfig
		if err := json.Unmarshal(raw, &cfg); err != nil || cfg.StepUID == "" {
			slog.WarnContext(ctx, "Skipping a canceled escalation step with an unparseable config",
				"jobUid", job.UID, "error", err)

			continue
		}

		key := cfg.StepUID + "|" + strconv.Itoa(cfg.RepeatIndex)
		if seen[key] {
			continue
		}

		seen[key] = true

		out = append(out, canceledEscalationStep{job: job, config: cfg, rawConfig: raw})
	}

	return out
}

// orgUIDOrEmpty reads the organization off a job row. Jobs are org-scoped in
// practice; the pointer is nullable only for the handful of global jobs, none
// of which are escalation steps.
func orgUIDOrEmpty(job *models.Job) string {
	if job.OrganizationUID == nil {
		return ""
	}

	return *job.OrganizationUID
}
