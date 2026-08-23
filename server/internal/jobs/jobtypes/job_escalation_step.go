package jobtypes

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"strings"
	"time"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/email"
	"github.com/fclairamb/solidping/server/internal/entitlements"
	"github.com/fclairamb/solidping/server/internal/incidentlinks"
	slackclient "github.com/fclairamb/solidping/server/internal/integrations/slack"
	smssvc "github.com/fclairamb/solidping/server/internal/integrations/sms"
	"github.com/fclairamb/solidping/server/internal/integrations/twilio"
	"github.com/fclairamb/solidping/server/internal/jobs/jobdef"
	"github.com/fclairamb/solidping/server/internal/jobs/jobsvc"
	"github.com/fclairamb/solidping/server/internal/webpush"
)

// phoneAckTokenTTL bounds the SMS/voice ack magic-link lifetime (mirrors the
// email ack TTL: 7 days from incident start).
const phoneAckTokenTTL = 7 * 24 * time.Hour

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
	// errEmptySlackToken is returned when the Slack access token is empty.
	errEmptySlackToken = errors.New("empty slack access token")
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
	// sentPhones dedupes phone deliveries within a single job run, keyed by
	// "kind:number" — so a user matched via both a user target and all_admins
	// is texted/called at most once per channel per step.
	sentPhones map[string]bool
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

	pagingNow := time.Now()
	if jctx.Services != nil && jctx.Services.Clock != nil {
		pagingNow = jctx.Services.Clock.Now()
	}

	if !incidentNeedsPaging(incident, pagingNow) {
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
			if !severityAllowsPersonTargets(filter) {
				log.InfoContext(ctx, "severity skipped user target — no person channel in set",
					"targetUID", target.TargetUID)
				stats.Skipped++

				continue
			}
			stats.DirectEmails += r.pageUser(ctx, jctx, log, incident, target.TargetUID, filter)
		case models.EscalationTargetSchedule:
			if !severityAllowsPersonTargets(filter) {
				log.InfoContext(ctx, "severity skipped schedule target — no person channel in set",
					"targetUID", target.TargetUID)
				stats.Skipped++

				continue
			}
			stats.DirectEmails += r.pageSchedule(ctx, jctx, log, incident, target.TargetUID, filter)
		case models.EscalationTargetAllAdmins:
			if !severityAllowsPersonTargets(filter) {
				log.InfoContext(ctx, "severity skipped all-admins target — no person channel in set")
				stats.Skipped++

				continue
			}
			stats.DirectEmails += r.pageAllAdmins(ctx, jctx, log, incident, filter)
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
// type is in the allowed set. A twilio connection has no severity token of
// its own, so it passes when the severity requests SMS or voice.
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

	// A phone connection's type string ("twilio") is not a severity token; it
	// delivers SMS and/or voice, so it passes when either of those is
	// requested. Asked as a CAPABILITY rather than a type comparison, so a
	// second phone integration type would need no change here.
	if caps := models.CapabilitiesFor(conn.Type); caps.CanSendSMS || caps.CanPlaceCall {
		if (caps.CanSendSMS && filter[channelTokenSMS]) || (caps.CanPlaceCall && filter[channelTokenVoice]) {
			return true
		}

		log.InfoContext(ctx, "severity skipped phone connection — neither sms nor voice in set",
			"connectionUID", *target.TargetUID, "channelType", string(conn.Type))

		return false
	}

	if !filter[string(conn.Type)] {
		log.InfoContext(ctx, "severity skipped connection target — channel type not in set",
			"connectionUID", *target.TargetUID, "channelType", string(conn.Type))

		return false
	}

	return true
}

// Severity channel tokens (the strings stored in a severity's channels[]).
const (
	channelTokenEmail        = "email"
	channelTokenSMS          = "sms"
	channelTokenVoice        = "voice"
	channelTokenPush         = "push"
	channelTokenCriticalPush = "critical_push"
	channelTokenWhatsApp     = "whatsapp"
	channelTokenTelegram     = "telegram"
)

// severityAllowsPersonTargets reports whether a severity permits paging
// person-type targets (user / schedule / all_admins) at all. A nil filter (no
// severity) allows everything; otherwise any person-deliverable token opens the
// gate, and per-route filtering below narrows to specific contact types.
func severityAllowsPersonTargets(filter map[string]bool) bool {
	if filter == nil {
		return true
	}

	for _, tok := range []string{
		channelTokenEmail, channelTokenSMS, channelTokenVoice,
		channelTokenPush, channelTokenCriticalPush, channelTokenWhatsApp,
		channelTokenTelegram,
	} {
		if filter[tok] {
			return true
		}
	}

	return false
}

// severityAllowsEmail reports whether an email (or Slack DM, which historically
// rides the email token) delivery is permitted.
func severityAllowsEmail(filter map[string]bool) bool {
	return filter == nil || filter[channelTokenEmail]
}

// severityAllowsWebPush reports whether a web-push delivery is permitted:
// nil (everything), the historical email-compat token, or an explicit push token.
func severityAllowsWebPush(filter map[string]bool) bool {
	return filter == nil || filter[channelTokenEmail] ||
		filter[channelTokenPush] || filter[channelTokenCriticalPush]
}

// severityAllowsSMS reports whether an SMS to a phone contact is permitted:
// nil (everything) or an explicit sms token.
func severityAllowsSMS(filter map[string]bool) bool {
	return filter == nil || filter[channelTokenSMS]
}

// severityAllowsVoice reports whether a voice call is permitted. Voice is only
// ever placed on an explicit "voice" token — never on a nil filter — so a
// severity-less escalation never surprises an on-call engineer with a phone
// call.
func severityAllowsVoice(filter map[string]bool) bool {
	return filter[channelTokenVoice]
}

// severityAllowsWhatsApp reports whether a WhatsApp message is permitted. Like
// voice, WhatsApp is only ever sent on an explicit token — never on a nil
// filter — so a severity-less escalation cannot surprise a user with a message
// on a channel they only opted into for specific severities.
func severityAllowsWhatsApp(filter map[string]bool) bool {
	return filter[channelTokenWhatsApp]
}

// severityAllowsTelegram reports whether a Telegram message is permitted:
// a nil filter (no severity attached, so everything the user configured fires)
// or an explicit "telegram" token.
//
// Deliberately NOT the voice/WhatsApp rule. Those two are explicit-token-only
// because each delivery costs money and interrupts hard — a surprise phone call
// is a different kind of event from a chat message. Telegram is free and
// arrives in the same place the user already reads alerts, so it follows the
// email/SMS rule instead: connecting a chat IS the opt-in, and a severity-less
// escalation should reach every channel the user connected. (Spec
// 2026-08-10-04, "Escalation dispatch": *explicit token or nil filter*.)
func severityAllowsTelegram(filter map[string]bool) bool {
	return filter == nil || filter[channelTokenTelegram]
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

	job, err := jctx.Services.Jobs.CreateJob(
		ctx, incident.OrganizationUID, string(jobdef.JobTypeNotification), cfg, nil,
	)
	if err != nil {
		log.WarnContext(ctx, "failed to create escalation notification job",
			"connectionUid", connectionUID, "error", err)

		return false
	}

	// Audit: record a pending row. Load the connection to get the channel type.
	conn, connErr := jctx.DBService.GetChannel(ctx, connectionUID)
	if connErr != nil {
		log.WarnContext(ctx, "failed to load connection for audit row",
			"connectionUid", connectionUID, "error", connErr)
	} else {
		stepUID := r.config.StepUID
		repeatIndex := r.config.RepeatIndex
		if auditErr := jctx.DBService.CreateIncidentNotification(ctx, models.NewIncidentNotificationForJob(
			incident.OrganizationUID, incident.UID, string(models.EventTypeIncidentEscalated),
			models.IncidentNotificationSourceEscalationConnection,
			connectionUID, job.UID, string(conn.Type),
			&stepUID, &repeatIndex,
		)); auditErr != nil {
			log.WarnContext(ctx, "failed to create notification audit row", "error", auditErr)
		}
	}

	return true
}

// pageUser fans out over a user's notification routes. Falls back to a direct
// email when no routes exist in the DB (preserves V1 behavior for users who
// have not visited Account → Notifications yet).
func (r *EscalationStepJobRun) pageUser(
	ctx context.Context, jctx *jobdef.JobContext, log *slog.Logger,
	incident *models.Incident, userUID *string, filter map[string]bool,
) int {
	if userUID == nil {
		log.WarnContext(ctx, "pageUser: nil userUID")

		return 0
	}

	routes, err := jctx.DBService.ListUserContactsWithRoutes(ctx, *userUID, incident.OrganizationUID)
	if err != nil || len(routes) == 0 {
		// Fallback: no routes in DB yet — email directly as V1 did, but only
		// when the severity permits email (a sms/voice-only severity must not
		// silently fall back to email).
		if !severityAllowsEmail(filter) {
			return 0
		}

		user, userErr := jctx.DBService.GetUser(ctx, *userUID)
		if userErr != nil {
			log.WarnContext(ctx, "escalation user target not found",
				"userUid", *userUID, "error", userErr)

			return 0
		}

		return r.sendEscalationEmail(
			ctx, jctx, log, incident, user.Email, *userUID,
			models.IncidentNotificationSourceEscalationUser,
		)
	}

	sent := 0
	for _, route := range routes {
		if !route.Enabled {
			continue
		}

		sent += r.dispatchRoute(ctx, jctx, log, incident, route, filter)
	}

	return sent
}

// dispatchRoute dispatches a single notification route, honoring the severity
// channel filter per contact type (nil filter = deliver via every route).
func (r *EscalationStepJobRun) dispatchRoute(
	ctx context.Context, jctx *jobdef.JobContext, log *slog.Logger,
	incident *models.Incident, route *models.UserNotificationRoute, filter map[string]bool,
) int {
	if route.Contact == nil {
		log.WarnContext(ctx, "dispatchRoute: route has no contact loaded",
			"routeUID", route.UID)

		return 0
	}

	switch route.Contact.Type {
	case models.UserContactTypeEmail:
		if !severityAllowsEmail(filter) {
			return 0
		}

		return r.sendEscalationEmail(
			ctx, jctx, log, incident,
			route.Contact.Value, route.UserUID,
			models.IncidentNotificationSourceEscalationUser,
		)
	case models.UserContactTypeSlackUser:
		return r.sendEscalationSlackDM(ctx, jctx, log, incident, route, filter)
	case models.UserContactTypeWebPush:
		if !severityAllowsWebPush(filter) {
			return 0
		}

		return r.sendWebPush(ctx, jctx, log, incident, route)
	case models.UserContactTypePhone:
		return r.pagePhone(ctx, jctx, log, incident, route, filter)
	case models.UserContactTypeWhatsApp:
		return r.pageWhatsApp(ctx, jctx, log, incident, route, filter)
	case models.UserContactTypeTelegram:
		return r.pageTelegram(ctx, jctx, log, incident, route, filter)
	default:
		log.WarnContext(ctx, "unknown contact type; skipping route",
			"type", route.Contact.Type,
			"contactUID", route.Contact.UID)

		return 0
	}
}

// sendEscalationSlackDM delivers an escalation DM to a per-user slack_user
// contact through the org's Slack connection. Extracted from dispatchRoute so
// that switch stays a routing table rather than an implementation.
func (r *EscalationStepJobRun) sendEscalationSlackDM(
	ctx context.Context, jctx *jobdef.JobContext, log *slog.Logger,
	incident *models.Incident, route *models.UserNotificationRoute, filter map[string]bool,
) int {
	if !severityAllowsEmail(filter) {
		// Slack DMs historically ride the email token.
		return 0
	}

	slackConn, connErr := jctx.DBService.GetSlackChannelForOrg(ctx, incident.OrganizationUID)
	if connErr != nil {
		log.WarnContext(ctx, "slack channel not found for org; skipping route",
			"orgUID", incident.OrganizationUID,
			"contactUID", route.Contact.UID,
			"error", connErr)

		return 0
	}

	settings, parseErr := models.SlackSettingsFromJSONMap(slackConn.Settings)
	if parseErr != nil || settings.AccessToken == "" {
		log.WarnContext(ctx, "slack access token not configured; skipping route",
			"orgUID", incident.OrganizationUID, "contactUID", route.Contact.UID)

		return 0
	}

	text := fmt.Sprintf(
		"[escalation] incident %s requires your attention. Open the dashboard to acknowledge or resolve.",
		incident.UID,
	)

	if err := postSlackDM(ctx, settings.AccessToken, route.Contact.Value, text); err != nil {
		log.WarnContext(ctx, "failed to send escalation Slack DM",
			"contactUID", route.Contact.UID,
			"userUID", route.UserUID,
			"error", err)

		return 0
	}

	return 1
}

// sendWebPush delivers a Web Push notification for a per-user webpush contact.
// Dead subscriptions (404/410) are pruned by soft-deleting the contact row.
func (r *EscalationStepJobRun) sendWebPush(
	ctx context.Context, jctx *jobdef.JobContext, log *slog.Logger,
	incident *models.Incident, route *models.UserNotificationRoute,
) int {
	if jctx.Services == nil || jctx.Services.WebPushOptions.VAPIDPublicKey == "" {
		log.WarnContext(ctx, "web push not configured; skipping route",
			"contactUID", route.Contact.UID)

		return 0
	}

	msg := webpush.Message{
		Title: fmt.Sprintf("[escalation] incident %s requires attention", incident.UID),
		Body:  "An incident requires your attention. Open the dashboard to acknowledge or resolve.",
		URL:   "",
	}

	err := webpush.Send(ctx, jctx.Services.WebPushOptions, route.Contact.Value, msg)
	if errors.Is(err, webpush.ErrSubscriptionGone) {
		log.InfoContext(ctx, "web push subscription gone; pruning contact",
			"contactUID", route.Contact.UID)

		if deleteErr := jctx.DBService.DeleteUserContact(ctx, route.Contact.UID); deleteErr != nil {
			log.WarnContext(ctx, "failed to prune gone web push contact",
				"contactUID", route.Contact.UID, "error", deleteErr)
		}

		return 0
	}

	if err != nil {
		log.WarnContext(ctx, "failed to send web push notification",
			"contactUID", route.Contact.UID, "error", err)

		return 0
	}

	stepUID := r.config.StepUID
	repeatIndex := r.config.RepeatIndex

	n := models.NewIncidentNotificationForUser(
		incident.OrganizationUID, incident.UID, string(models.EventTypeIncidentEscalated),
		models.IncidentNotificationSourceEscalationUser, route.UserUID, "webpush",
		&stepUID, &repeatIndex,
	)
	if auditErr := jctx.DBService.CreateIncidentNotification(ctx, n); auditErr != nil {
		log.WarnContext(ctx, "failed to create web push notification audit row", "error", auditErr)
	}

	_ = jctx.DBService.MarkIncidentNotificationSentByUID(ctx, n.UID, time.Now(), "")

	return 1
}

// pagePhone delivers to a per-user verified phone contact: an SMS when the
// severity requests it, and/or a voice call when the severity requests voice
// and the connection has a voice_from_number. An unverified number is never
// contacted, and a missing provider degrades to today's info-log skip.
func (r *EscalationStepJobRun) pagePhone(
	ctx context.Context, jctx *jobdef.JobContext, log *slog.Logger,
	incident *models.Incident, route *models.UserNotificationRoute, filter map[string]bool,
) int {
	contact := route.Contact

	if contact.VerifiedAt == nil {
		log.InfoContext(ctx, "phone contact not verified; skipping route",
			"contactUID", contact.UID, "userUID", route.UserUID)

		return 0
	}

	wantSMS := severityAllowsSMS(filter)
	wantVoice := severityAllowsVoice(filter)
	if !wantSMS && !wantVoice {
		return 0
	}

	resolution, wantSMS, wantVoice := resolvePhoneProviders(
		ctx, jctx, log, incident, contact, wantSMS, wantVoice,
	)
	if resolution == nil {
		return 0
	}

	baseURL := appBaseURL(jctx)
	orgSlug := r.orgSlugFor(ctx, jctx, log, incident.OrganizationUID)
	ackToken := r.buildPhoneAckToken(jctx, incident, contact.Value)

	sent := 0
	// ORDER IS LOAD-BEARING: the instance-spend guards run BEFORE the per-org
	// reservation. reservePhoneChannel consumes the hourly bucket and writes
	// the durable monthly counter, and there is no compensating release — so
	// reserving first and refusing second would charge the org monthly quota
	// for a message that was never sent. Silently, and on a metered plan.
	if wantSMS && r.reserveInstanceSMSSpend(ctx, jctx, log, incident, resolution, contact.Value) &&
		r.reservePhoneChannel(ctx, jctx, log, incident, contact.Value, models.UsageCounterKindSMS) &&
		r.sendPhoneSMS(ctx, jctx, log, incident, route, resolution, baseURL, orgSlug, ackToken) {
		sent++
	}

	if wantVoice && r.reservePhoneChannel(ctx, jctx, log, incident, contact.Value, models.UsageCounterKindVoice) &&
		r.placePhoneCall(ctx, jctx, log, incident, route, resolution, baseURL, ackToken) {
		sent++
	}

	return sent
}

// resolvePhoneProviders performs the two-step, per-capability resolution: the
// org's own Twilio integration first (bring-your-own), then the instance-level
// provider. An org with no integration of its own is NOT left resolving to
// nothing — that is the whole point of the server-provided default.
//
// Returns the resolution plus the capabilities that are actually deliverable,
// or a nil resolution when there is nothing to send at all. A missing provider
// degrades to an info-log skip; it never fails the escalation step.
func resolvePhoneProviders(
	ctx context.Context, jctx *jobdef.JobContext, log *slog.Logger,
	incident *models.Incident, contact *models.UserContact, wantSMS, wantVoice bool,
) (*smssvc.Resolution, bool, bool) {
	if jctx.Services == nil || jctx.Services.SMS == nil {
		log.InfoContext(ctx, "sms resolver not wired; skipping phone route",
			"contactUID", contact.UID, "orgUID", incident.OrganizationUID)

		return nil, false, false
	}

	resolution, err := jctx.Services.SMS.Resolve(ctx, incident.OrganizationUID)
	if err != nil {
		log.InfoContext(ctx, "could not resolve a phone provider; skipping phone route",
			"contactUID", contact.UID, "orgUID", incident.OrganizationUID, "error", err)

		return nil, false, false
	}

	if wantSMS && !resolution.SMSAvailable() {
		log.InfoContext(ctx, "sms requested but no provider available; skipping",
			"contactUID", contact.UID, "orgUID", incident.OrganizationUID)

		wantSMS = false
	}

	if wantVoice && !resolution.VoiceAvailable() {
		log.InfoContext(ctx, "voice requested but no caller ID configured; skipping call",
			"contactUID", contact.UID, "orgUID", incident.OrganizationUID)

		wantVoice = false
	}

	if !wantSMS && !wantVoice {
		return nil, false, false
	}

	return resolution, wantSMS, wantVoice
}

// reserveInstanceSMSSpend applies the INSTANCE-SPEND guards — the
// instance-wide hourly cap and the destination-country allow-list — and
// returns true when the send may proceed.
//
// It applies to server-credential sends ONLY. A bring-your-own send bills the
// customer's own account, so it neither consumes the shared cap nor is gated
// by an allow-list chosen for this instance's bill; the per-org runaway guard
// in reservePhoneChannel still applies to it, as today's safety belt.
//
// A breach logs loudly and records a skipped audit row: it must never fail
// silently, because what it silently drops is an alert.
func (r *EscalationStepJobRun) reserveInstanceSMSSpend(
	ctx context.Context, jctx *jobdef.JobContext, log *slog.Logger,
	incident *models.Incident, resolution *smssvc.Resolution, phone string,
) bool {
	if !resolution.InstanceCredentialsForSMS() {
		return true
	}

	if jctx.Services == nil || jctx.Services.Entitlements == nil {
		return true
	}

	err := jctx.Services.Entitlements.ReserveInstanceSMS(ctx, incident.OrganizationUID, phone)
	if err == nil {
		return true
	}

	jctx.Services.Entitlements.LogInstanceSMSBreach(ctx, log, incident.OrganizationUID, err)

	reason := "sms_instance_runaway"
	if errors.Is(err, entitlements.ErrCountryNotAllowed) {
		reason = "sms_country_not_allowed"
	}

	r.auditPhoneSkip(ctx, jctx, log, incident, reason)

	return false
}

// reservePhoneChannel enforces per-run dedup and the SMS/voice quota + runaway
// guard before a send. Returns true when the send may proceed. A quota denial
// records a skipped audit row but never fails the step.
func (r *EscalationStepJobRun) reservePhoneChannel(
	ctx context.Context, jctx *jobdef.JobContext, log *slog.Logger,
	incident *models.Incident, phone, kind string,
) bool {
	key := kind + ":" + phone
	if r.sentPhones[key] {
		// Same number+channel already handled in this job run (e.g. user matched
		// via both `user` and `all_admins`).
		return false
	}

	if jctx.Services != nil && jctx.Services.Entitlements != nil {
		var err error

		switch kind {
		case models.UsageCounterKindVoice:
			err = jctx.Services.Entitlements.ReserveCall(ctx, incident.OrganizationUID)
		case models.UsageCounterKindWhatsApp:
			err = jctx.Services.Entitlements.ReserveWhatsApp(ctx, incident.OrganizationUID)
		default:
			err = jctx.Services.Entitlements.ReserveSMS(ctx, incident.OrganizationUID)
		}

		if err != nil {
			log.InfoContext(ctx, "phone channel quota reached; skipping send",
				"kind", kind, "orgUID", incident.OrganizationUID, "error", err)
			r.auditPhoneSkip(ctx, jctx, log, incident, kind+"_quota_exhausted")

			return false
		}
	}

	if r.sentPhones == nil {
		r.sentPhones = make(map[string]bool)
	}
	r.sentPhones[key] = true

	return true
}

// auditPhoneSkip records a skipped incident_notifications row for a quota-denied
// SMS/voice send.
func (r *EscalationStepJobRun) auditPhoneSkip(
	ctx context.Context, jctx *jobdef.JobContext, log *slog.Logger,
	incident *models.Incident, reason string,
) {
	stepUID := r.config.StepUID
	repeatIndex := r.config.RepeatIndex
	n := models.NewSkippedIncidentNotification(
		incident.OrganizationUID, incident.UID, string(models.EventTypeIncidentEscalated),
		models.IncidentNotificationSourceEscalationUser, reason,
		&stepUID, &repeatIndex,
	)
	if auditErr := jctx.DBService.CreateIncidentNotification(ctx, n); auditErr != nil {
		log.WarnContext(ctx, "failed to create quota-skip audit row", "error", auditErr)
	}
}

// orgSlugFor resolves the org slug for building user-facing ack URLs. A
// missing org is logged but not fatal (the SMS just ships without an ack link).
func (r *EscalationStepJobRun) orgSlugFor(
	ctx context.Context, jctx *jobdef.JobContext, log *slog.Logger, orgUID string,
) string {
	return orgSlugForOrg(ctx, jctx, log, orgUID)
}

// orgSlugForOrg is the run-independent form of orgSlugFor, shared with the
// incident-resolution-notice job.
func orgSlugForOrg(
	ctx context.Context, jctx *jobdef.JobContext, log *slog.Logger, orgUID string,
) string {
	org, err := jctx.DBService.GetOrganization(ctx, orgUID)
	if err != nil || org == nil {
		log.WarnContext(ctx, "failed to load org slug for phone ack URL", "orgUid", orgUID, "error", err)

		return ""
	}

	return org.Slug
}

// buildPhoneAckToken signs the shared magic-link ack token (incident + phone
// number) reused by the SMS ack link and the voice TwiML flow.
func (r *EscalationStepJobRun) buildPhoneAckToken(
	jctx *jobdef.JobContext, incident *models.Incident, phone string,
) string {
	if jctx.AppConfig == nil {
		return ""
	}

	secret := []byte(jctx.AppConfig.Auth.JWTSecret)
	if len(secret) == 0 {
		return ""
	}

	exp := incident.StartedAt.Add(phoneAckTokenTTL)

	return incidentlinks.Sign(secret, incident.UID, phone, exp)
}

// phoneCheckName returns the incident's check name, falling back to the
// incident UID when the check has been deleted.
func (r *EscalationStepJobRun) phoneCheckName(
	ctx context.Context, jctx *jobdef.JobContext, incident *models.Incident,
) string {
	return incidentCheckName(ctx, jctx, incident)
}

// incidentCheckName is the run-independent form of phoneCheckName, shared with
// the incident-resolution-notice job.
func incidentCheckName(
	ctx context.Context, jctx *jobdef.JobContext, incident *models.Incident,
) string {
	check, err := jctx.DBService.GetCheck(ctx, incident.OrganizationUID, incident.CheckUID)
	if err == nil && check != nil && check.Name != nil && *check.Name != "" {
		return *check.Name
	}

	return incident.UID
}

// twilioStatusCallbackURL builds the delivery-status callback URL for a Twilio
// send. connID is the `cid` the callback must carry — the per-org integration
// UID for a bring-your-own send, or smssvc.InstanceCallbackID for a
// server-credential one — and orgUID is echoed as `oid`, exactly like the TwiML
// URL built next to it. Both parameters sit inside the URL Twilio signs, so a
// forged `oid` cannot pass the callback handler's signature check.
//
// Returns "" when there is no reachable base URL (nothing could call us back)
// or no cid to name the credentials the callback must be validated against.
func twilioStatusCallbackURL(baseURL, orgUID, connID string) string {
	if baseURL == "" || connID == "" {
		return ""
	}

	return fmt.Sprintf("%s/api/v1/integrations/twilio/status?cid=%s&oid=%s",
		strings.TrimRight(baseURL, "/"),
		url.QueryEscape(connID), url.QueryEscape(orgUID))
}

// smsStatusCallbackURL returns the delivery-status callback URL for one
// escalation SMS, or "" when this send can produce no Twilio status callback.
//
// A bring-your-own send is signed with the org connection's own auth token, so
// the callback carries that connection's UID. A server-credential send carries
// the instance sentinel — but ONLY when the instance SMS provider is Twilio:
// OVH's API has no per-job callback field (it ignores StatusCallback by design,
// see sms.ovhSender.SendSMS) and its receipts arrive on the service-level DLR
// endpoint with its own token, so asking for a Twilio callback there would be
// requesting a receipt nothing can ever deliver or validate.
func smsStatusCallbackURL(
	cfg *config.Config, baseURL, orgUID string, resolution *smssvc.Resolution,
) string {
	if resolution.SMSMode == smssvc.ModeBringYourOwn && resolution.Conn != nil {
		return twilioStatusCallbackURL(baseURL, orgUID, resolution.Conn.UID)
	}

	if cfg == nil || cfg.SMS.ResolvedProvider() != config.SMSProviderTwilio {
		return ""
	}

	return twilioStatusCallbackURL(baseURL, orgUID, smssvc.InstanceCallbackID)
}

// voiceStatusCallbackURL returns the delivery-status callback URL for one
// escalation call. It reuses voiceCallbackConnID so the status URL and the
// TwiML URL of the same call can never disagree on `cid` — an org whose Twilio
// integration configures no voice_from_number places its calls on the
// INSTANCE's credentials, so both URLs must carry the sentinel rather than the
// connection UID. Voice is Twilio-only by construction, so there is no
// provider check to make here.
func voiceStatusCallbackURL(baseURL, orgUID string, resolution *smssvc.Resolution) string {
	return twilioStatusCallbackURL(baseURL, orgUID, voiceCallbackConnID(resolution))
}

// sendPhoneSMS sends one escalation SMS to the contact's number. Returns true
// on success.
func (r *EscalationStepJobRun) sendPhoneSMS(
	ctx context.Context, jctx *jobdef.JobContext, log *slog.Logger,
	incident *models.Incident, route *models.UserNotificationRoute,
	resolution *smssvc.Resolution, baseURL, orgSlug, ackToken string,
) bool {
	name := r.phoneCheckName(ctx, jctx, incident)
	body := fmt.Sprintf("[SolidPing] %s: %s needs attention (escalated).", orgSlug, name)
	if baseURL != "" && orgSlug != "" && ackToken != "" {
		body += fmt.Sprintf(" Ack: %s/api/v1/orgs/%s/incidents/%s/ack?token=%s",
			strings.TrimRight(baseURL, "/"), orgSlug, incident.UID, ackToken)
	}

	body += twilio.OptOutFooter

	res, err := resolution.Sender.SendSMS(ctx, &smssvc.SendParams{
		To:             route.Contact.Value,
		Body:           body,
		StatusCallback: smsStatusCallbackURL(jctx.AppConfig, baseURL, incident.OrganizationUID, resolution),
	})
	if err != nil {
		log.WarnContext(ctx, "failed to send escalation SMS",
			"contactUID", route.Contact.UID, "mode", string(resolution.SMSMode), "error", err)

		return false
	}

	r.auditPhoneSend(ctx, jctx, log, incident, route.UserUID, "sms", providerMessageID(res))

	return true
}

// placePhoneCall places one outbound voice call that fetches the ack TwiML.
// Returns true on success.
func (r *EscalationStepJobRun) placePhoneCall(
	ctx context.Context, jctx *jobdef.JobContext, log *slog.Logger,
	incident *models.Incident, route *models.UserNotificationRoute,
	resolution *smssvc.Resolution, baseURL, ackToken string,
) bool {
	if baseURL == "" {
		log.WarnContext(ctx, "no app base URL configured; cannot place voice call",
			"contactUID", route.Contact.UID)

		return false
	}

	twimlURL := fmt.Sprintf("%s/api/v1/integrations/twilio/voice?cid=%s&oid=%s&iid=%s&token=%s",
		strings.TrimRight(baseURL, "/"),
		url.QueryEscape(voiceCallbackConnID(resolution)),
		url.QueryEscape(incident.OrganizationUID),
		url.QueryEscape(incident.UID), url.QueryEscape(ackToken))

	res, err := resolution.Voice.PlaceCall(ctx, &smssvc.CallParams{
		To:             route.Contact.Value,
		TwiMLURL:       twimlURL,
		StatusCallback: voiceStatusCallbackURL(baseURL, incident.OrganizationUID, resolution),
	})
	if err != nil {
		log.WarnContext(ctx, "failed to place escalation voice call",
			"contactUID", route.Contact.UID, "mode", string(resolution.VoiceMode), "error", err)

		return false
	}

	r.auditPhoneSend(ctx, jctx, log, incident, route.UserUID, "voice", providerMessageID(res))

	return true
}

// voiceCallbackConnID returns the `cid` the TwiML callback should carry: the
// org integration's UID for a bring-your-own call, or the instance sentinel
// for a server-credential one. The callback middleware uses it to pick which
// auth token validates Twilio's signature.
func voiceCallbackConnID(resolution *smssvc.Resolution) string {
	if resolution.VoiceMode == smssvc.ModeBringYourOwn && resolution.Conn != nil {
		return resolution.Conn.UID
	}

	return smssvc.InstanceCallbackID
}

// auditPhoneSend records a sent incident_notifications row for an SMS/voice
// escalation delivery.
func (r *EscalationStepJobRun) auditPhoneSend(
	ctx context.Context, jctx *jobdef.JobContext, log *slog.Logger,
	incident *models.Incident, userUID, channel, messageID string,
) {
	stepUID := r.config.StepUID
	repeatIndex := r.config.RepeatIndex

	n := models.NewIncidentNotificationForUser(
		incident.OrganizationUID, incident.UID, string(models.EventTypeIncidentEscalated),
		models.IncidentNotificationSourceEscalationUser, userUID, channel,
		&stepUID, &repeatIndex,
	)
	if auditErr := jctx.DBService.CreateIncidentNotification(ctx, n); auditErr != nil {
		log.WarnContext(ctx, "failed to create phone notification audit row", "error", auditErr)

		return
	}

	_ = jctx.DBService.MarkIncidentNotificationSentByUID(ctx, n.UID, time.Now(), messageID)
}

// providerMessageID safely extracts the provider's message identifier (a
// Twilio SID, an OVH job id) from a send result.
func providerMessageID(res *smssvc.SendResult) string {
	if res == nil {
		return ""
	}

	return res.ProviderMessageID
}

// postSlackDM sends a DM to slackUserID using the org's Slack bot token.
// The integrations/slack package has no internal dependencies so importing it
// here is safe (no import cycle).
func postSlackDM(ctx context.Context, accessToken, slackUserID, text string) error {
	if accessToken == "" {
		return errEmptySlackToken
	}

	client := slackclient.NewClient(accessToken)

	msg := &slackclient.MessageResponse{Text: text}

	_, err := client.PostMessage(ctx, slackclient.PostMessageOptions{
		Channel: slackUserID,
		Message: msg,
	})

	return err
}

// pageSchedule resolves who is on call right now and pages them via
// email. Empty schedules emit `incident.escalation_failed` (logged) but
// do not abort subsequent steps.
func (r *EscalationStepJobRun) pageSchedule(
	ctx context.Context, jctx *jobdef.JobContext, log *slog.Logger,
	incident *models.Incident, scheduleUID *string, filter map[string]bool,
) int {
	if scheduleUID == nil {
		return 0
	}

	resolver := jctx.Services
	if resolver == nil {
		return 0
	}

	resolveNow := time.Now()
	if jctx.Services != nil && jctx.Services.Clock != nil {
		resolveNow = jctx.Services.Clock.Now()
	}

	user, err := resolveOnCallUser(ctx, jctx, *scheduleUID, resolveNow)
	if err != nil {
		log.WarnContext(ctx, "on-call schedule resolution failed",
			"scheduleUid", *scheduleUID, "error", err)
		r.emitEscalationFailed(ctx, jctx, incident, "schedule_resolve_failed", err.Error())

		// Audit: record a skipped row for this schedule target.
		stepUID := r.config.StepUID
		repeatIndex := r.config.RepeatIndex
		n := models.NewSkippedIncidentNotification(
			incident.OrganizationUID, incident.UID, string(models.EventTypeIncidentEscalated),
			models.IncidentNotificationSourceEscalationSchedule, "schedule_resolve_failed",
			&stepUID, &repeatIndex,
		)
		if auditErr := jctx.DBService.CreateIncidentNotification(ctx, n); auditErr != nil {
			log.WarnContext(ctx, "failed to create skipped notification audit row", "error", auditErr)
		}

		return 0
	}

	// Walk all enabled notification routes for the on-call user so that
	// webpush (and any future contact type) is also paged.
	routes, routesErr := jctx.DBService.ListUserContactsWithRoutes(ctx, user.UID, incident.OrganizationUID)
	if routesErr != nil || len(routes) == 0 {
		// Fallback: no routes — email directly, but only when the severity
		// permits email.
		if !severityAllowsEmail(filter) {
			return 0
		}

		return r.sendEscalationEmail(
			ctx, jctx, log, incident, user.Email, user.UID, models.IncidentNotificationSourceEscalationSchedule,
		)
	}

	sent := 0
	for _, route := range routes {
		if !route.Enabled {
			continue
		}

		sent += r.dispatchRoute(ctx, jctx, log, incident, route, filter)
	}

	if sent == 0 && severityAllowsEmail(filter) {
		// All routes were disabled/unknown/filtered out — fall back to direct
		// email, but only when the severity permits it.
		sent = r.sendEscalationEmail(
			ctx, jctx, log, incident, user.Email, user.UID, models.IncidentNotificationSourceEscalationSchedule,
		)
	}

	return sent
}

// pageAllAdmins emails every admin member of the incident's org.
func (r *EscalationStepJobRun) pageAllAdmins(
	ctx context.Context, jctx *jobdef.JobContext, log *slog.Logger,
	incident *models.Incident, filter map[string]bool,
) int {
	members, err := jctx.DBService.ListMembersByOrg(ctx, incident.OrganizationUID)
	if err != nil {
		log.WarnContext(ctx, "list org members failed", "error", err)

		return 0
	}

	count := 0
	for _, member := range members {
		if !member.Role.AtLeast(models.MemberRoleAdmin) || member.User == nil {
			continue
		}

		count += r.pageAdminMember(ctx, jctx, log, incident, member, filter)
	}

	if count == 0 {
		// Audit: record a skipped row when no admins were found to page.
		stepUID := r.config.StepUID
		repeatIndex := r.config.RepeatIndex
		n := models.NewSkippedIncidentNotification(
			incident.OrganizationUID, incident.UID, string(models.EventTypeIncidentEscalated),
			models.IncidentNotificationSourceEscalationAllAdmins, "no_admins",
			&stepUID, &repeatIndex,
		)
		if auditErr := jctx.DBService.CreateIncidentNotification(ctx, n); auditErr != nil {
			log.WarnContext(ctx, "failed to create skipped notification audit row", "error", auditErr)
		}
	}

	return count
}

// pageAdminMember pages one admin member: walks their enabled routes (severity
// filtered) and falls back to a direct email when no route delivered and the
// severity permits email. Returns the number of pages produced.
func (r *EscalationStepJobRun) pageAdminMember(
	ctx context.Context, jctx *jobdef.JobContext, log *slog.Logger,
	incident *models.Incident, member *models.OrganizationMember, filter map[string]bool,
) int {
	routes, routesErr := jctx.DBService.ListUserContactsWithRoutes(ctx, member.UserUID, incident.OrganizationUID)
	if routesErr != nil || len(routes) == 0 {
		return r.adminFallbackEmail(ctx, jctx, log, incident, member, filter)
	}

	sent := 0
	for _, route := range routes {
		if route.Enabled {
			sent += r.dispatchRoute(ctx, jctx, log, incident, route, filter)
		}
	}

	if sent == 0 {
		return r.adminFallbackEmail(ctx, jctx, log, incident, member, filter)
	}

	return sent
}

// adminFallbackEmail sends a direct escalation email to an admin member when
// the severity permits email and the member has an address.
func (r *EscalationStepJobRun) adminFallbackEmail(
	ctx context.Context, jctx *jobdef.JobContext, log *slog.Logger,
	incident *models.Incident, member *models.OrganizationMember, filter map[string]bool,
) int {
	if member.User.Email == "" || !severityAllowsEmail(filter) {
		return 0
	}

	return r.sendEscalationEmail(
		ctx, jctx, log, incident, member.User.Email, member.UserUID,
		models.IncidentNotificationSourceEscalationAllAdmins,
	)
}

// sendEscalationEmail sends the escalation-policy email directly via the
// email service, rendered through the shared formatter (escalation.html) so
// it gets the same branding/HTML part as every other transactional email and
// addresses the incident by its human-facing #N reference instead of the raw
// UUID. Returns 1 on success, 0 otherwise.
func (r *EscalationStepJobRun) sendEscalationEmail(
	ctx context.Context, jctx *jobdef.JobContext, log *slog.Logger,
	incident *models.Incident, recipient, userUID, source string,
) int {
	if jctx.Services == nil || jctx.Services.EmailSender == nil ||
		jctx.Services.EmailFormatter == nil || recipient == "" {
		return 0
	}

	stepUID := r.config.StepUID
	repeatIndex := r.config.RepeatIndex

	n := models.NewIncidentNotificationForUser(
		incident.OrganizationUID, incident.UID, string(models.EventTypeIncidentEscalated),
		source, userUID, "email", &stepUID, &repeatIndex,
	)
	if auditErr := jctx.DBService.CreateIncidentNotification(ctx, n); auditErr != nil {
		log.WarnContext(ctx, "failed to create notification audit row", "error", auditErr)
	}

	viewModel := r.buildEscalationEmailViewModel(ctx, jctx, incident)

	subject, htmlBody, textBody, err := jctx.Services.EmailFormatter.Format("escalation.html", viewModel)
	if err != nil {
		log.WarnContext(ctx, "failed to format escalation email", "error", err)
		_ = jctx.DBService.MarkIncidentNotificationFailedByUID(ctx, n.UID, time.Now(), err.Error())

		return 0
	}

	msg := &email.Message{
		Recipients:       email.Recipients{To: []string{recipient}},
		Subject:          subject,
		HTML:             htmlBody,
		Text:             textBody,
		SupportReplyable: email.SupportReplyable("escalation.html"),
	}

	result, err := jctx.Services.EmailSender.Send(ctx, msg)
	if err != nil {
		log.WarnContext(ctx, "failed to send escalation email",
			"recipient", recipient, "error", err)
		_ = jctx.DBService.MarkIncidentNotificationFailedByUID(ctx, n.UID, time.Now(), err.Error())

		return 0
	}

	messageID := ""
	if result != nil {
		messageID = result.MessageID
	}
	_ = jctx.DBService.MarkIncidentNotificationSentByUID(ctx, n.UID, time.Now(), messageID)

	return 1
}

// buildEscalationEmailViewModel assembles the escalation.html view-model.
// Check/org lookups are best-effort (mirrors emitEscalationFailed): a check
// deleted since the incident opened must not block the page, it just loses
// its name/link.
func (r *EscalationStepJobRun) buildEscalationEmailViewModel(
	ctx context.Context, jctx *jobdef.JobContext, incident *models.Incident,
) map[string]any {
	baseURL := appBaseURL(jctx)

	orgSlug := ""
	if org, err := jctx.DBService.GetOrganization(ctx, incident.OrganizationUID); err == nil && org != nil {
		orgSlug = org.Slug
	}

	checkName := escalationUnknownCheckName
	var checkURL string

	check, checkErr := jctx.DBService.GetCheck(ctx, incident.OrganizationUID, incident.CheckUID)
	if checkErr == nil && check != nil {
		checkName = escalationCheckName(check)
		checkURL = escalationCheckURL(baseURL, orgSlug, check)
	}

	return map[string]any{
		viewModelKeyCheckName: checkName,
		"CheckURL":            checkURL,
		"IncidentNumber":      incident.Number,
		"IncidentURL":         escalationIncidentURL(baseURL, orgSlug, incident),
		"StartedAt":           incident.StartedAt.Format("2006-01-02 15:04:05"),
		"FailureCount":        incident.FailureCount,
		"DashboardURL":        escalationDashboardRootURL(baseURL),
		"DocsURL":             escalationDocsURL(baseURL),
	}
}

// escalationUnknownCheckName is the fallback check name when the check has
// been deleted since the incident opened (or has neither Name nor Slug).
const escalationUnknownCheckName = "Unknown check"

// viewModelKeyCheckName is the escalation.html view-model key for the check
// name — pulled into a constant alongside escalationUnknownCheckName so
// goconst doesn't flag the literal (job_email_test.go uses the same "CheckName"
// string in unrelated template-data fixtures elsewhere in this package).
const viewModelKeyCheckName = "CheckName"

// escalationCheckName returns the check name from Name or Slug, falling back
// to escalationUnknownCheckName — mirrors notifications.getCheckName,
// duplicated locally to avoid an import cycle (notifications already imports
// jobtypes' sibling packages) for one small helper.
func escalationCheckName(check *models.Check) string {
	if check == nil {
		return escalationUnknownCheckName
	}

	if check.Name != nil && *check.Name != "" {
		return *check.Name
	}

	if check.Slug != nil && *check.Slug != "" {
		return *check.Slug
	}

	return escalationUnknownCheckName
}

// escalationCheckURL builds the dashboard URL for a check's detail page.
// Returns "" when any required component is missing.
func escalationCheckURL(baseURL, orgSlug string, check *models.Check) string {
	if baseURL == "" || orgSlug == "" || check == nil || check.Slug == nil {
		return ""
	}

	return fmt.Sprintf("%s/dash0/orgs/%s/checks/%s", strings.TrimRight(baseURL, "/"), orgSlug, *check.Slug)
}

// escalationIncidentURL builds the dashboard URL for an incident's detail
// page. Returns "" when any required component is missing.
func escalationIncidentURL(baseURL, orgSlug string, incident *models.Incident) string {
	if baseURL == "" || orgSlug == "" || incident == nil || incident.UID == "" {
		return ""
	}

	return fmt.Sprintf("%s/dash0/orgs/%s/incidents/%s", strings.TrimRight(baseURL, "/"), orgSlug, incident.UID)
}

// escalationDashboardRootURL returns the dash0 root URL for the email footer
// link, or "" when baseURL is empty.
func escalationDashboardRootURL(baseURL string) string {
	baseURL = strings.TrimRight(baseURL, "/")
	if baseURL == "" {
		return ""
	}

	return baseURL + "/dash0"
}

// escalationDocsURL returns the documentation root URL for the email footer
// link, or "" when baseURL is empty.
func escalationDocsURL(baseURL string) string {
	baseURL = strings.TrimRight(baseURL, "/")
	if baseURL == "" {
		return ""
	}

	return baseURL + "/docs"
}

// emitEscalationFailed records a soft failure for the timeline.
func (r *EscalationStepJobRun) emitEscalationFailed(
	ctx context.Context, jctx *jobdef.JobContext, incident *models.Incident, reason, detail string,
) {
	event := models.NewEvent(incident.OrganizationUID, models.EventTypeIncidentEscalationFailed, models.ActorTypeSystem)
	event.IncidentUID = &incident.UID
	event.CheckUID = &incident.CheckUID
	event.Payload = models.JSONMap{
		"reason":    reason,
		"detail":    detail,
		"check_uid": incident.CheckUID,
	}
	// Best-effort: the check may have been deleted since the incident opened;
	// don't fail the escalation-failure record over a missing slug/name.
	check, chkErr := jctx.DBService.GetCheck(ctx, incident.OrganizationUID, incident.CheckUID)
	if chkErr == nil && check != nil {
		event.Payload["check_slug"] = check.Slug
		event.Payload["check_name"] = check.Name
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
	if policy.RepeatAfterSeconds == nil {
		return nil
	}

	steps, err := jctx.DBService.ListEscalationPolicySteps(ctx, policy.UID)
	if err != nil {
		return err
	}

	scheduleNow := time.Now()
	if jctx.Services != nil && jctx.Services.Clock != nil {
		scheduleNow = jctx.Services.Clock.Now()
	}

	startAt := scheduleNow.Add(time.Duration(*policy.RepeatAfterSeconds) * time.Second)

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
// repeat cycle. Step 0's delay_seconds is applied to startAt; subsequent
// steps stack their delay_seconds onto the previous fire time.
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
		cumulative = cumulative.Add(time.Duration(step.DelaySeconds) * time.Second)
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
