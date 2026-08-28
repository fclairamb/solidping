package incidents

import (
	"context"
	"encoding/json"
	"log/slog"
	"sort"
	"strconv"
	"time"

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
// where the cycle stood; they were simply never read back.
//
// Which rungs may be resumed is decided by resumableEscalationSteps over the
// FULL job history for the incident — see its doc for why the whole history,
// and not just the canceled rows, is what makes "an already-fired rung is
// never replayed" an invariant rather than an accident of ordering.
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
	history, err := s.jobsSvc.ListForIncident(
		ctx, incident.UID, string(jobdef.JobTypeEscalationStep))
	if err != nil {
		slog.WarnContext(ctx, "Failed to read the escalation step history for an unack",
			"incidentUid", incident.UID, "error", err)

		return
	}

	steps := resumableEscalationSteps(ctx, history)
	if len(steps) == 0 {
		// No policy, or the cycle had already run itself out before the ack.
		// Unack never STARTS an escalation that was not running.
		slog.DebugContext(ctx, "No interrupted escalation cycle to resume",
			"incidentUid", incident.UID)

		return
	}

	shift := s.clock.Now().Sub(steps[0].dueAt)
	if shift < 0 {
		shift = 0
	}

	for i := range steps {
		step := &steps[i]
		fireAt := step.dueAt.Add(shift)

		if _, err := s.jobsSvc.CreateJob(
			ctx, step.orgUID, string(jobdef.JobTypeEscalationStep), step.rawConfig,
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

// escalationRung is one (stepUid, repeatIndex) rung of a policy cycle,
// collapsed from however many job generations the incident has accumulated for
// it. The raw config is kept so a resumed job is created with the ORIGINAL
// bytes — re-marshaling would silently drop any field this build does not know
// about.
type escalationRung struct {
	key       string
	config    jobtypes.EscalationStepJobConfig
	rawConfig json.RawMessage
	// dueAt is the LATEST generation's scheduled_at: the most recent decision
	// about when this rung should fire, not a stale time from a cycle ago.
	dueAt time.Time
	// orgUID is carried off the job row so the resumed job lands in the same
	// organization.
	orgUID string
	// ran records that SOME generation of this rung left 'pending' — it fired,
	// is firing, or failed. Any of those means the page already went out (or is
	// going out), so the rung must never be recreated.
	ran bool
	// live records that SOME generation of this rung is still queued and not
	// canceled. Recreating it would double-page.
	live bool
}

// resumableEscalationSteps decides which rungs of an interrupted escalation
// cycle may be brought back, given the incident's FULL escalation-step job
// history, and returns them ordered by due time.
//
// A rung is identified by (stepUid, repeatIndex), NOT by job row: repeated
// ack → unack cycles accumulate several generations of the same rung, and the
// only correct question is about the rung, not about any one row.
//
// Two exclusions, and the first is the load-bearing one:
//
//   - ALREADY RAN. If any generation of the rung left 'pending' — success,
//     failed, retried, or currently running — the page already went out and the
//     rung is dead for good. This must be evaluated ACROSS generations. The
//     obvious implementation (list only the canceled-pending rows and keep the
//     earliest) is wrong in a way that is easy to miss and expensive when it
//     bites: after ack#1 → unack#1, rung s2 exists as a canceled gen-1 row AND
//     as a live gen-2 row; once gen-2 FIRES, a second ack/unack cycle still
//     finds the gen-1 row sitting there canceled-and-pending, resurrects it,
//     and — its due time now being in the past — pages immediately. That is the
//     page storm option (c) was chosen over (b) to avoid, reintroduced through
//     the back door. Correlating generations is what makes the invariant
//     structural instead of true only until someone acks twice.
//   - STILL LIVE. A generation that is queued and not canceled is already going
//     to fire; recreating it would double-page.
//
// A rung with no canceled generation left to copy is likewise skipped — there
// is nothing to recreate from.
//
// The "ran" test is deliberately conservative: a 'retried' generation counts,
// even though a retried job means the attempt FAILED and its clone carried the
// real delivery. If that clone was itself canceled by the ack, this drops the
// rung rather than resuming it. Under-paging by one rung in a rare failure path
// is the right side to err on for a mechanism whose whole justification is not
// replaying pages people already received.
//
// Rows whose config no longer parses are dropped rather than guessed at.
func resumableEscalationSteps(ctx context.Context, jobs []*models.Job) []escalationRung {
	rungs := make(map[string]*escalationRung, len(jobs))
	order := make([]string, 0, len(jobs))

	for _, job := range jobs {
		rung, ok := escalationRungFor(ctx, rungs, &order, job)
		if !ok {
			continue
		}

		switch {
		case job.Status != models.JobStatusPending:
			rung.ran = true
		case job.DeletedAt == nil:
			rung.live = true
		default:
			// A canceled generation: the newest one is the copy to resume from,
			// and the rows arrive oldest-first so a later one simply wins.
			applyCanceledGeneration(ctx, rung, job)
		}
	}

	out := make([]escalationRung, 0, len(order))

	for _, key := range order {
		rung := rungs[key]
		if rung.ran || rung.live || rung.rawConfig == nil {
			continue
		}

		out = append(out, *rung)
	}

	sort.Slice(out, func(i, j int) bool { return out[i].dueAt.Before(out[j].dueAt) })

	return out
}

// escalationRungFor decodes a job row and returns the rung it belongs to,
// registering it on first sight so the output keeps a stable, deterministic
// order before the final sort.
func escalationRungFor(
	ctx context.Context, rungs map[string]*escalationRung, order *[]string, job *models.Job,
) (*escalationRung, bool) {
	cfg, _, ok := decodeEscalationStepConfig(ctx, job)
	if !ok {
		return nil, false
	}

	key := cfg.StepUID + "|" + strconv.Itoa(cfg.RepeatIndex)

	rung, seen := rungs[key]
	if !seen {
		rung = &escalationRung{key: key, config: cfg}
		rungs[key] = rung
		*order = append(*order, key)
	}

	return rung, true
}

// applyCanceledGeneration records a canceled generation as the copy to resume
// from. Later generations overwrite earlier ones, so the resumed job carries
// the most recent scheduling decision rather than a stale one.
func applyCanceledGeneration(ctx context.Context, rung *escalationRung, job *models.Job) {
	cfg, raw, ok := decodeEscalationStepConfig(ctx, job)
	if !ok {
		return
	}

	rung.config = cfg
	rung.rawConfig = raw
	rung.dueAt = job.ScheduledAt
	rung.orgUID = orgUIDOrEmpty(job)
}

// decodeEscalationStepConfig reads a step job's config, returning both the
// decoded form and the original bytes.
func decodeEscalationStepConfig(
	ctx context.Context, job *models.Job,
) (jobtypes.EscalationStepJobConfig, json.RawMessage, bool) {
	var cfg jobtypes.EscalationStepJobConfig

	raw, err := json.Marshal(job.Config)
	if err != nil {
		slog.WarnContext(ctx, "Skipping an unreadable escalation step job",
			"jobUid", job.UID, "error", err)

		return cfg, nil, false
	}

	if err := json.Unmarshal(raw, &cfg); err != nil || cfg.StepUID == "" {
		slog.WarnContext(ctx, "Skipping an escalation step job with an unparseable config",
			"jobUid", job.UID, "error", err)

		return cfg, nil, false
	}

	return cfg, raw, true
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
