package jobtypes

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/email"
	"github.com/fclairamb/solidping/server/internal/jobs/jobdef"
	"github.com/fclairamb/solidping/server/internal/jobs/jobsvc"
)

// Escalation step errors.
var (
	// ErrMissingStepUID is returned when the step UID is missing from the
	// job config.
	ErrMissingStepUID = errors.New("stepUid is required")
	// ErrEscalationStepNotFound is returned when the step row no longer
	// exists (policy was hard-deleted, edited, etc.).
	ErrEscalationStepNotFound = errors.New("escalation step not found")
	// ErrOnCallResolverNotWired is returned when an escalation step with
	// a schedule target fires before the on-call resolver has been wired.
	// In practice this only happens if the server boot order is broken.
	ErrOnCallResolverNotWired = errors.New("on-call resolver not wired")
)

// EscalationStepJobConfig configures one fired step of an escalation
// policy. The incidentUid key matches what jobsvc.CancelPendingForIncident
// looks for, so ack/snooze/resolve cancels pending steps automatically.
type EscalationStepJobConfig struct {
	IncidentUID string `json:"incidentUid"`
	StepUID     string `json:"stepUid"`
	// PolicyUID denormalized for telemetry; not strictly required.
	PolicyUID string `json:"policyUid,omitempty"`
	// RepeatIndex is 0 for the first cycle, then 1, 2, ... up to repeat_max.
	RepeatIndex int `json:"repeatIndex"`
	// IsLastStep flags the step that triggers the next-cycle reschedule when
	// repeat_max > 0. Stamped at scheduling time so the job runner doesn't
	// have to re-walk the policy to figure out where it is.
	IsLastStep bool `json:"isLastStep,omitempty"`
}

// EscalationStepJobDefinition is the factory for escalation-step jobs.
type EscalationStepJobDefinition struct{}

// Type returns the job type identifier.
func (d *EscalationStepJobDefinition) Type() jobdef.JobType {
	return jobdef.JobTypeEscalationStep
}

// CreateJobRun builds a new escalation-step run instance.
//
//nolint:ireturn // Factory pattern requires interface return
func (d *EscalationStepJobDefinition) CreateJobRun(config json.RawMessage) (jobdef.JobRunner, error) {
	var cfg EscalationStepJobConfig
	if err := json.Unmarshal(config, &cfg); err != nil {
		return nil, fmt.Errorf("parsing escalation step config: %w", err)
	}

	if cfg.IncidentUID == "" {
		return nil, ErrMissingIncidentUID
	}

	if cfg.StepUID == "" {
		return nil, ErrMissingStepUID
	}

	return &EscalationStepJobRun{config: cfg}, nil
}

// EscalationStepJobRun executes one rung of an escalation policy.
type EscalationStepJobRun struct {
	config EscalationStepJobConfig
}

// Run loads the incident, exits if it has been acked/snoozed/resolved
// since scheduling (belt-and-braces — the cancel sweep should already
// have killed this row), resolves the step's targets, fans out to
// notification jobs, and on the last step of a repeat-eligible policy
// schedules the next cycle.
func (r *EscalationStepJobRun) Run(ctx context.Context, jctx *jobdef.JobContext) error {
	log := jctx.Logger.With(
		"incidentUid", r.config.IncidentUID,
		"stepUid", r.config.StepUID,
		"repeatIndex", r.config.RepeatIndex,
	)

	step, err := jctx.DBService.GetEscalationPolicyStep(ctx, r.config.StepUID)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrEscalationStepNotFound, err)
	}

	policy, err := jctx.DBService.GetEscalationPolicy(ctx, "", step.PolicyUID)
	if err != nil {
		return fmt.Errorf("load policy %s: %w", step.PolicyUID, err)
	}

	incident, err := jctx.DBService.GetIncident(ctx, policy.OrganizationUID, r.config.IncidentUID)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrIncidentNotFound, err)
	}

	if !incidentNeedsPaging(incident, jctx.Services.Clock.Now()) {
		log.InfoContext(ctx, "escalation step skipped — incident already handled")

		return nil
	}

	targets, err := jctx.DBService.ListEscalationPolicyTargets(ctx, []string{step.UID})
	if err != nil {
		return fmt.Errorf("list targets: %w", err)
	}

	// Resolve the severity channel-set, if any. nil means "no severity
	// filter; deliver via the target's own channel(s) as before". An
	// explicit severity narrows: only deliveries whose channel-type is
	// in the severity's channels[] are fired; the rest are skipped with
	// an audit log entry.
	channelFilter := r.resolveSeverityChannels(ctx, jctx, log, step, policy.OrganizationUID)

	stats := r.fanOutWithSeverity(ctx, jctx, log, incident, targets, channelFilter)

	r.emitEscalatedEvent(ctx, jctx, log, incident, step, len(targets), stats)

	if r.config.IsLastStep {
		if err := r.scheduleNextCycle(ctx, jctx, log, incident, policy); err != nil {
			log.WarnContext(ctx, "failed to schedule next escalation cycle", "error", err)
		}
	}

	return nil
}

// incidentNeedsPaging is the belt-and-braces guard: even if the cancel
// sweep missed this row, we still skip if the incident has been handled.
func incidentNeedsPaging(incident *models.Incident, now time.Time) bool {
	if incident.AcknowledgedAt != nil || incident.ResolvedAt != nil {
		return false
	}
	if incident.SnoozedUntil != nil && incident.SnoozedUntil.After(now) {
		return false
	}

	return true
}

// fanOutStats reports how many targets actually produced an outbound
// page, broken down by mechanism. Used in the escalation event payload.
type fanOutStats struct {
	NotificationJobs int
	DirectEmails     int
	Skipped          int
}

// fanOut dispatches each target. `connection` targets enqueue a
// notification job (re-using existing per-channel formatting). `user`,
// `all_admins`, and `schedule` resolve to user emails and send directly
// via the email service. V1 reality: cleanly addressable per-user
// channels are limited to email + Pushover/Ntfy device subscriptions
// (which we do not surface yet); Slack/Discord per-user DMs require a
// per-org bot which is its own spec. Targeting Slack channels remains
// available via the `connection` target type.
// fanOutWithSeverity is the severity-aware fan-out. A non-nil filter
// means "only deliver via channel-types in this set"; nil means
// "deliver via the target's own channel as before". Skipped deliveries
// log a warning so operators can reconcile when a severity rules out a
// target's only channel.
func (r *EscalationStepJobRun) fanOutWithSeverity(
	ctx context.Context, jctx *jobdef.JobContext, log *slog.Logger,
	incident *models.Incident, targets []*models.EscalationPolicyTarget,
	filter map[string]bool,
) fanOutStats {
	stats := fanOutStats{}

	for _, target := range targets {
		switch target.TargetType {
		case models.EscalationTargetConnection:
			if !r.connectionPassesSeverityFilter(ctx, jctx, log, target, filter) {
				stats.Skipped++

				continue
			}
			if target.TargetUID != nil && r.enqueueNotificationFor(ctx, jctx, log, incident, *target.TargetUID) {
				stats.NotificationJobs++
			} else {
				stats.Skipped++
			}
		case models.EscalationTargetUser:
			if filter != nil && !filter["email"] {
				log.InfoContext(ctx, "severity skipped user target — email not in channel-set",
					"targetUID", target.TargetUID)
				stats.Skipped++

				continue
			}
			stats.DirectEmails += r.pageUser(ctx, jctx, log, incident, target.TargetUID)
		case models.EscalationTargetSchedule:
			if filter != nil && !filter["email"] {
				log.InfoContext(ctx, "severity skipped schedule target — email not in channel-set",
					"targetUID", target.TargetUID)
				stats.Skipped++

				continue
			}
			stats.DirectEmails += r.pageSchedule(ctx, jctx, log, incident, target.TargetUID)
		case models.EscalationTargetAllAdmins:
			if filter != nil && !filter["email"] {
				log.InfoContext(ctx, "severity skipped all-admins target — email not in channel-set")
				stats.Skipped++

				continue
			}
			stats.DirectEmails += r.pageAllAdmins(ctx, jctx, log, incident)
		}
	}

	return stats
}

// resolveSeverityChannels returns the channel-set the step's severity
// allows, or nil when the step has no severity (= no filter, default
// behavior). The returned map keys are channel-type strings ("email",
// "slack", "sms", …).
func (r *EscalationStepJobRun) resolveSeverityChannels(
	ctx context.Context, jctx *jobdef.JobContext, log *slog.Logger,
	step *models.EscalationPolicyStep, orgUID string,
) map[string]bool {
	if step.SeverityUID == nil || *step.SeverityUID == "" {
		return nil
	}
	sev, err := jctx.DBService.GetSeverity(ctx, orgUID, *step.SeverityUID)
	if err != nil {
		log.WarnContext(ctx, "severity lookup failed; firing without filter",
			"severityUID", *step.SeverityUID, "error", err)

		return nil
	}
	out := make(map[string]bool, 8)
	for _, c := range sev.ChannelList() {
		out[c] = true
	}

	return out
}

// connectionPassesSeverityFilter checks whether a connection target's
// underlying channel type is permitted by the severity. Returns true
// when the filter is nil (no severity in play) or when the connection's
// type is in the allowed set.
func (r *EscalationStepJobRun) connectionPassesSeverityFilter(
	ctx context.Context, jctx *jobdef.JobContext, log *slog.Logger,
	target *models.EscalationPolicyTarget, filter map[string]bool,
) bool {
	if filter == nil {
		return true
	}
	if target.TargetUID == nil {
		return false
	}
	conn, err := jctx.DBService.GetChannel(ctx, *target.TargetUID)
	if err != nil {
		log.WarnContext(ctx, "severity filter: channel lookup failed; skipping target",
			"connectionUID", *target.TargetUID, "error", err)

		return false
	}
	if !filter[string(conn.Type)] {
		log.InfoContext(ctx, "severity skipped connection target — channel type not in set",
			"connectionUID", *target.TargetUID, "channelType", string(conn.Type))

		return false
	}

	return true
}

// enqueueNotificationFor queues a notification job for the
// incident.escalated event on this connection. Returns true on success.
func (r *EscalationStepJobRun) enqueueNotificationFor(
	ctx context.Context, jctx *jobdef.JobContext, log *slog.Logger,
	incident *models.Incident, connectionUID string,
) bool {
	cfg, err := json.Marshal(NotificationJobConfig{
		ConnectionUID: connectionUID,
		IncidentUID:   incident.UID,
		EventType:     string(models.EventTypeIncidentEscalated),
	})
	if err != nil {
		log.WarnContext(ctx, "failed to marshal escalation notification config",
			"connectionUid", connectionUID, "error", err)

		return false
	}

	if jctx.Services == nil || jctx.Services.Jobs == nil {
		return false
	}

	if _, err := jctx.Services.Jobs.CreateJob(
		ctx, incident.OrganizationUID, string(jobdef.JobTypeNotification), cfg, nil,
	); err != nil {
		log.WarnContext(ctx, "failed to create escalation notification job",
			"connectionUid", connectionUID, "error", err)

		return false
	}

	return true
}

// pageUser sends a direct email to the named user. Returns the count of
// emails actually sent (0 or 1).
func (r *EscalationStepJobRun) pageUser(
	ctx context.Context, jctx *jobdef.JobContext, log *slog.Logger,
	incident *models.Incident, userUID *string,
) int {
	if userUID == nil {
		return 0
	}

	user, err := jctx.DBService.GetUser(ctx, *userUID)
	if err != nil {
		log.WarnContext(ctx, "escalation user target not found",
			"userUid", *userUID, "error", err)

		return 0
	}

	return r.sendEscalationEmail(ctx, jctx, log, incident, user.Email)
}

// pageSchedule resolves who is on call right now and pages them via
// email. Empty schedules emit `incident.escalation_failed` (logged) but
// do not abort subsequent steps.
func (r *EscalationStepJobRun) pageSchedule(
	ctx context.Context, jctx *jobdef.JobContext, log *slog.Logger,
	incident *models.Incident, scheduleUID *string,
) int {
	if scheduleUID == nil {
		return 0
	}

	resolver := jctx.Services
	if resolver == nil {
		return 0
	}

	user, err := resolveOnCallUser(ctx, jctx, *scheduleUID, jctx.Services.Clock.Now())
	if err != nil {
		log.WarnContext(ctx, "on-call schedule resolution failed",
			"scheduleUid", *scheduleUID, "error", err)
		r.emitEscalationFailed(ctx, jctx, incident, "schedule_resolve_failed", err.Error())

		return 0
	}

	return r.sendEscalationEmail(ctx, jctx, log, incident, user.Email)
}

// pageAllAdmins emails every admin member of the incident's org.
func (r *EscalationStepJobRun) pageAllAdmins(
	ctx context.Context, jctx *jobdef.JobContext, log *slog.Logger,
	incident *models.Incident,
) int {
	members, err := jctx.DBService.ListMembersByOrg(ctx, incident.OrganizationUID)
	if err != nil {
		log.WarnContext(ctx, "list org members failed", "error", err)

		return 0
	}

	count := 0
	for _, member := range members {
		if member.Role != models.MemberRoleAdmin {
			continue
		}

		if member.User == nil || member.User.Email == "" {
			continue
		}

		count += r.sendEscalationEmail(ctx, jctx, log, incident, member.User.Email)
	}

	return count
}

// sendEscalationEmail sends a minimal escalation email directly via the
// email service. Returns 1 on success, 0 otherwise. V1 body is plain
// text — richer formatting can come later by routing through a
// per-user notification connection.
func (r *EscalationStepJobRun) sendEscalationEmail(
	ctx context.Context, jctx *jobdef.JobContext, log *slog.Logger,
	incident *models.Incident, recipient string,
) int {
	if jctx.Services == nil || jctx.Services.EmailSender == nil || recipient == "" {
		return 0
	}

	subject := fmt.Sprintf("[escalation] incident %s requires attention", incident.UID)
	body := fmt.Sprintf(
		"Escalation policy fired for incident %s.\n\nOpen the dashboard to acknowledge or resolve.",
		incident.UID,
	)

	msg := &email.Message{
		Recipients: email.Recipients{To: []string{recipient}},
		Subject:    subject,
		Text:       body,
	}
	if _, err := jctx.Services.EmailSender.Send(ctx, msg); err != nil {
		log.WarnContext(ctx, "failed to send escalation email",
			"recipient", recipient, "error", err)

		return 0
	}

	return 1
}

// emitEscalationFailed records a soft failure for the timeline.
func (r *EscalationStepJobRun) emitEscalationFailed(
	ctx context.Context, jctx *jobdef.JobContext, incident *models.Incident, reason, detail string,
) {
	event := models.NewEvent(incident.OrganizationUID, models.EventTypeIncidentEscalationFailed, models.ActorTypeSystem)
	event.IncidentUID = &incident.UID
	event.CheckUID = &incident.CheckUID
	event.Payload = models.JSONMap{
		"reason": reason,
		"detail": detail,
	}
	_ = jctx.DBService.CreateEvent(ctx, event)
}

// scheduleNextCycle schedules the next repeat cycle's steps if the
// policy still has repeats remaining.
func (r *EscalationStepJobRun) scheduleNextCycle(
	ctx context.Context, jctx *jobdef.JobContext, log *slog.Logger,
	incident *models.Incident, policy *models.EscalationPolicy,
) error {
	if policy.RepeatMax == 0 || r.config.RepeatIndex >= policy.RepeatMax {
		return nil
	}
	if policy.RepeatAfterMinutes == nil {
		return nil
	}

	steps, err := jctx.DBService.ListEscalationPolicySteps(ctx, policy.UID)
	if err != nil {
		return err
	}

	startAt := jctx.Services.Clock.Now().Add(time.Duration(*policy.RepeatAfterMinutes) * time.Minute)

	return ScheduleEscalationCycle(
		ctx, jctx.Services.Jobs, incident, policy, steps, startAt, r.config.RepeatIndex+1, log,
	)
}

// emitEscalatedEvent records what fired for the incident timeline.
func (r *EscalationStepJobRun) emitEscalatedEvent(
	ctx context.Context, jctx *jobdef.JobContext, log *slog.Logger,
	incident *models.Incident, step *models.EscalationPolicyStep, targetCount int, stats fanOutStats,
) {
	event := models.NewEvent(incident.OrganizationUID, models.EventTypeIncidentEscalated, models.ActorTypeSystem)
	event.IncidentUID = &incident.UID
	event.CheckUID = &incident.CheckUID
	event.Payload = models.JSONMap{
		"step_uid":          step.UID,
		"step_position":     step.Position,
		"repeat_index":      r.config.RepeatIndex,
		"target_count":      targetCount,
		"notification_jobs": stats.NotificationJobs,
		"direct_emails":     stats.DirectEmails,
		"skipped":           stats.Skipped,
		"source":            "escalation_policy",
	}

	if err := jctx.DBService.CreateEvent(ctx, event); err != nil {
		log.WarnContext(ctx, "failed to record escalated event", "error", err)
	}
}

// ScheduleEscalationCycle schedules every step in `steps` for the given
// repeat cycle. Step 0's delay_minutes is applied to startAt; subsequent
// steps stack their delay_minutes onto the previous fire time.
//
// Exported because the incident-open path also calls it (cycle 0).
func ScheduleEscalationCycle(
	ctx context.Context, jobs jobsvc.Service,
	incident *models.Incident, policy *models.EscalationPolicy,
	steps []*models.EscalationPolicyStep, startAt time.Time, repeatIndex int,
	log *slog.Logger,
) error {
	if len(steps) == 0 {
		return nil
	}

	cumulative := startAt

	for i, step := range steps {
		cumulative = cumulative.Add(time.Duration(step.DelayMinutes) * time.Minute)
		fireAt := cumulative
		isLast := i == len(steps)-1

		cfg, err := json.Marshal(EscalationStepJobConfig{
			IncidentUID: incident.UID,
			StepUID:     step.UID,
			PolicyUID:   policy.UID,
			RepeatIndex: repeatIndex,
			IsLastStep:  isLast,
		})
		if err != nil {
			return fmt.Errorf("marshal escalation step config: %w", err)
		}

		if _, err := jobs.CreateJob(
			ctx, incident.OrganizationUID, string(jobdef.JobTypeEscalationStep), cfg,
			&jobsvc.JobOptions{ScheduledAt: &fireAt},
		); err != nil {
			log.WarnContext(ctx, "failed to schedule escalation step",
				"stepUid", step.UID, "fireAt", fireAt, "error", err)

			return err
		}
	}

	return nil
}

// resolveOnCallUser is the on-call resolver seam — exposed as a package
// variable so tests can stub it without dragging in the real schedule
// service. Default implementation is wired in escalationruntime.
//
//nolint:gochecknoglobals // pluggable seam for cross-package wiring
var resolveOnCallUser OnCallResolverFn = func(
	_ context.Context, _ *jobdef.JobContext, _ string, _ time.Time,
) (*models.User, error) {
	return nil, ErrOnCallResolverNotWired
}

// OnCallResolverFn is the signature the escalation runtime calls for
// schedule-typed targets.
type OnCallResolverFn func(
	ctx context.Context, jctx *jobdef.JobContext, scheduleUID string, at time.Time,
) (*models.User, error)

// SetOnCallResolver overrides the on-call resolver used by escalation
// step jobs. Called once at server startup with a closure that knows
// how to reach oncallschedules.Service without an import cycle.
func SetOnCallResolver(fn OnCallResolverFn) {
	resolveOnCallUser = fn
}
