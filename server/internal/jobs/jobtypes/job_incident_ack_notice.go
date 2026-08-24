package jobtypes

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/entitlements"
	"github.com/fclairamb/solidping/server/internal/integrations/telegram"
	"github.com/fclairamb/solidping/server/internal/jobs/jobdef"
)

const (
	// telegramAckedStatePrefix namespaces the per-incident, per-chat marker
	// proving an acknowledgment notice already went out to that chat. It is
	// what makes the job idempotent across retries AND across an
	// ack → unack → re-ack cycle within the same incident.
	telegramAckedStatePrefix = "telegram_acked:"
	// telegramAckedMarkerTTL matches the thread-anchor TTL: past a week the
	// incident is long gone and so is any chance of a duplicate notice.
	telegramAckedMarkerTTL = 7 * 24 * time.Hour
	// ackAuditScanLimit bounds the audit-row fallback scan, mirroring
	// resolutionAuditScanLimit.
	ackAuditScanLimit = 500
)

// ErrAckNoticeIncomplete marks a run where at least one chat failed with a
// network-class error. Wrapped in a RetryableError so the job re-runs; chats
// that were notified kept their marker, so the re-run cannot duplicate.
var ErrAckNoticeIncomplete = errors.New("incident ack notice: some chats could not be notified")

// IncidentAckNoticeJobConfig identifies the incident whose acknowledgment has
// to be announced to the people who were paged for it, and by whom.
//
// The actor is carried on the config rather than re-read from the event row so
// the notice names exactly the person who acked, even if the incident is
// unacknowledged (or re-acknowledged by somebody else) before the job runs —
// the same reason NotificationJobConfig embeds its comment.
type IncidentAckNoticeJobConfig struct {
	OrganizationUID string `json:"organizationUid"`
	IncidentUID     string `json:"incidentUid"`
	// ActorName is the human label of whoever acknowledged. Empty degrades to
	// a neutral "Someone", never to a blank line.
	ActorName string `json:"actorName,omitempty"`
	// Via is the channel the acknowledgment came from ("web", "slack", …).
	Via string `json:"via,omitempty"`
}

// IncidentAckNoticeJobDefinition is the factory for ack-notice jobs.
type IncidentAckNoticeJobDefinition struct{}

// Type returns the job type identifier.
func (d *IncidentAckNoticeJobDefinition) Type() jobdef.JobType {
	return jobdef.JobTypeIncidentAckNotice
}

// CreateJobRun builds a new ack-notice run instance.
//
//nolint:ireturn // Factory pattern requires interface return
func (d *IncidentAckNoticeJobDefinition) CreateJobRun(config json.RawMessage) (jobdef.JobRunner, error) {
	var cfg IncidentAckNoticeJobConfig
	if err := json.Unmarshal(config, &cfg); err != nil {
		return nil, fmt.Errorf("parsing incident ack notice config: %w", err)
	}

	if cfg.OrganizationUID == "" {
		return nil, ErrMissingOrganizationUID
	}

	if cfg.IncidentUID == "" {
		return nil, ErrMissingIncidentUID
	}

	return &IncidentAckNoticeJobRun{config: cfg}, nil
}

// IncidentAckNoticeJobRun tells everyone who was paged for an incident that a
// colleague has claimed it, so four people do not each start debugging the
// same outage in parallel.
type IncidentAckNoticeJobRun struct {
	config IncidentAckNoticeJobConfig
	// handled dedupes chats within one run — a chat can appear both as a
	// thread anchor and (through its owner) as an audit row.
	handled map[string]bool
	// transient records that at least one chat failed for a network-class
	// reason, which is the ONLY thing that makes this job worth retrying.
	transient bool
}

// Run notifies every chat that was paged for this incident.
//
// Failure policy is per-chat, never per-job: one blocked user must not stop
// the other four from hearing that the incident is claimed.
func (r *IncidentAckNoticeJobRun) Run(ctx context.Context, jctx *jobdef.JobContext) error {
	log := jctx.Logger.With(
		"incidentUid", r.config.IncidentUID,
		"orgUid", r.config.OrganizationUID,
	)

	incident, err := jctx.DBService.GetIncident(ctx, r.config.OrganizationUID, r.config.IncidentUID)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrIncidentNotFound, err)
	}

	if !r.incidentWantsNotice(ctx, log, incident) {
		return nil
	}

	client, ok := telegramClientFor(ctx, jctx, log)
	if !ok {
		return nil
	}

	params := ackAlertParams(ctx, jctx, log, incident, &r.config)
	r.handled = make(map[string]bool)

	anchored := r.notifyAnchoredChats(ctx, jctx, log, client, incident, params)
	r.notifyAuditedChats(ctx, jctx, log, client, incident, params, anchored)

	if r.transient {
		return jobdef.NewRetryableError(ErrAckNoticeIncomplete)
	}

	return nil
}

// incidentWantsNotice is the gate. Three ways an ack notice becomes wrong
// between the enqueue and the run, and all three must silence it:
//
//   - a rolled-up child was never paged in the first place;
//   - the acknowledgment was WITHDRAWN (unack), so announcing it would stop
//     other people from picking the incident up — the worst possible outcome
//     for a message whose entire purpose is coordination;
//   - the incident resolved, which the resolution notice announces instead.
func (r *IncidentAckNoticeJobRun) incidentWantsNotice(
	ctx context.Context, log *slog.Logger, incident *models.Incident,
) bool {
	if incident.PagingSuppressed {
		log.InfoContext(ctx, "incident ack notice skipped — paging is suppressed for this incident")

		return false
	}

	if incident.AcknowledgedAt == nil {
		log.InfoContext(ctx, "incident ack notice skipped — the acknowledgment was withdrawn")

		return false
	}

	if incident.State == models.IncidentStateResolved {
		log.InfoContext(ctx, "incident ack notice skipped — the incident is already resolved")

		return false
	}

	return true
}

// ackAlertParams renders the ACKNOWLEDGED message values. Same builder as a
// page — only the state and the detail line differ, because "acknowledged by
// alice via Slack" is the one fact an acknowledgment carries that an alert
// does not.
func ackAlertParams(
	ctx context.Context, jctx *jobdef.JobContext, log *slog.Logger,
	incident *models.Incident, cfg *IncidentAckNoticeJobConfig,
) *telegram.AlertParams {
	params := telegramAlertParams(ctx, jctx, log, incident)
	params.State = telegram.StateAcknowledged
	params.Detail = telegramAckDetail(cfg)

	return params
}

// telegramAckDetail renders who took the incident and from where.
func telegramAckDetail(cfg *IncidentAckNoticeJobConfig) string {
	actor := strings.TrimSpace(cfg.ActorName)
	if actor == "" {
		actor = "Someone"
	}

	detail := "acknowledged by " + actor

	if via := telegramAckViaLabel(cfg.Via); via != "" {
		detail += " " + via
	}

	return detail
}

// telegramAckViaLabel renders the originating channel, or "" for the
// unremarkable dashboard case (nobody needs to read "via web").
func telegramAckViaLabel(via string) string {
	switch strings.ToLower(strings.TrimSpace(via)) {
	case "slack":
		return "via Slack"
	case "discord":
		return "via Discord"
	case "telegram":
		return "via Telegram"
	case "email":
		return "via email"
	case "phone":
		return "via phone"
	default:
		return ""
	}
}

// notifyAnchoredChats is the primary pass: the thread anchors ARE the record
// of which chats received a page for this incident, with the message id to
// thread the notice under.
//
// Unlike the resolution notice, the anchors are READ and left in place. The
// incident is still open, so the resolution notice still has to find them —
// consuming one here would silently cost that chat its all-clear. Exclusion
// between concurrent runs therefore rests on the per-chat marker alone, which
// is why the marker is written after every delivery.
func (r *IncidentAckNoticeJobRun) notifyAnchoredChats(
	ctx context.Context, jctx *jobdef.JobContext, log *slog.Logger,
	client *telegram.Client, incident *models.Incident, params *telegram.AlertParams,
) map[string]bool {
	orgUID := incident.OrganizationUID
	prefix := telegramThreadStatePrefix + incident.UID + ":"

	entries, err := jctx.DBService.ListStateEntries(ctx, &orgUID, prefix)
	if err != nil {
		// A DB hiccup here would silently drop every notice for the incident,
		// so it is exactly the case a retry exists for.
		log.WarnContext(ctx, "could not list telegram thread anchors for the ack notice", "error", err)
		r.transient = true

		return nil
	}

	anchored := make(map[string]bool, len(entries))

	for _, entry := range entries {
		chatID := strings.TrimPrefix(entry.Key, prefix)
		if chatID == "" {
			continue
		}

		anchored[chatID] = true

		r.notifyChat(ctx, jctx, log, client, incident, params, chatID, telegramAnchorMessageID(entry))
	}

	return anchored
}

// notifyAuditedChats is the fallback for chats whose anchor is gone — a long
// incident that outlived the 7-day TTL, or an anchor write that failed. The
// audit trail still remembers the person was paged; their CURRENT verified
// contact says where to reach them now.
func (r *IncidentAckNoticeJobRun) notifyAuditedChats(
	ctx context.Context, jctx *jobdef.JobContext, log *slog.Logger,
	client *telegram.Client, incident *models.Incident, params *telegram.AlertParams,
	anchored map[string]bool,
) {
	for _, userUID := range r.pagedUserUIDs(ctx, jctx, log, incident) {
		for _, chatID := range telegramChatsForUser(ctx, jctx, log, userUID, incident.OrganizationUID) {
			if anchored[chatID] {
				continue
			}

			r.notifyChat(ctx, jctx, log, client, incident, params, chatID, 0)
		}
	}
}

// pagedUserUIDs lists the distinct users this incident actually paged over
// Telegram, oldest row first.
//
// Restricted to `incident.escalated` rows for the same reason the resolution
// notice restricts its scan: that is what a page is audited as, while this
// job's own sends are audited as `incident.acknowledged`. Without the split
// the fallback would feed on its own output and re-notify on every retry.
func (r *IncidentAckNoticeJobRun) pagedUserUIDs(
	ctx context.Context, jctx *jobdef.JobContext, log *slog.Logger, incident *models.Incident,
) []string {
	rows, err := jctx.DBService.ListIncidentNotifications(ctx, incident.OrganizationUID,
		db.ListIncidentNotificationsFilter{
			IncidentUID: incident.UID,
			Status:      models.IncidentNotificationStatusSent,
			Limit:       ackAuditScanLimit,
		})
	if err != nil {
		log.WarnContext(ctx, "could not read the notification audit trail for the ack notice", "error", err)

		return nil
	}

	seen := make(map[string]bool, len(rows))
	out := make([]string, 0, len(rows))

	for _, row := range rows {
		if row.ChannelType != models.UserContactTypeTelegram ||
			row.EventType != string(models.EventTypeIncidentEscalated) ||
			row.UserUID == nil || *row.UserUID == "" || seen[*row.UserUID] {
			continue
		}

		seen[*row.UserUID] = true

		out = append(out, *row.UserUID)
	}

	return out
}

// notifyChat sends one acknowledgment notice to a chat, guarded by the
// per-chat marker and the hourly runaway guard. anchorMsgID is the message to
// thread under, or 0 for a chat reached through the audit fallback.
func (r *IncidentAckNoticeJobRun) notifyChat(
	ctx context.Context, jctx *jobdef.JobContext, log *slog.Logger,
	client *telegram.Client, incident *models.Incident, params *telegram.AlertParams,
	chatID string, anchorMsgID int64,
) {
	if r.handled[chatID] {
		return
	}

	r.handled[chatID] = true

	if alreadyToldAboutAck(ctx, jctx, log, incident, chatID) {
		return
	}

	if !reserveTelegramSend(ctx, jctx, log, incident) {
		auditAckSkip(ctx, jctx, log, incident, entitlements.RunawayKindTelegram+"_runaway_guard")

		return
	}

	messageID, err := sendTelegramAlertTo(ctx, jctx, log, client, incident, chatID, params, anchorMsgID)
	if err != nil {
		if telegramFailureIsTransient(err) {
			r.transient = true
		}

		log.WarnContext(ctx, "failed to send the telegram ack notice",
			"chatId", chatID, "reason", telegram.FailureReason(err), "error", err)

		return
	}

	markAckTold(ctx, jctx, log, incident, chatID)
	auditAckSend(ctx, jctx, log, incident, chatID, messageID)
}

// telegramAckedKey is the marker proving one chat already heard about this
// incident's acknowledgment.
func telegramAckedKey(incidentUID, chatID string) string {
	return telegramAckedStatePrefix + incidentUID + ":" + chatID
}

// alreadyToldAboutAck reports whether this chat has already received the
// notice. A read failure answers "yes": skipping a notice is a missed message,
// sending a second one is a bug the user sees.
func alreadyToldAboutAck(
	ctx context.Context, jctx *jobdef.JobContext, log *slog.Logger,
	incident *models.Incident, chatID string,
) bool {
	orgUID := incident.OrganizationUID

	entry, err := jctx.DBService.GetStateEntry(ctx, &orgUID, telegramAckedKey(incident.UID, chatID))
	if err != nil {
		log.WarnContext(ctx, "could not read the telegram ack marker; skipping chat",
			"chatId", chatID, "error", err)

		return true
	}

	return entry != nil
}

// markAckTold records that this chat heard the acknowledgment. SetStateEntry
// (not SetStateEntryIfNotExists) on purpose: it resurrects a soft-deleted or
// expired row, so the marker is always actually in place afterwards.
func markAckTold(
	ctx context.Context, jctx *jobdef.JobContext, log *slog.Logger,
	incident *models.Incident, chatID string,
) {
	orgUID := incident.OrganizationUID
	ttl := telegramAckedMarkerTTL
	value := &models.JSONMap{"notifiedAt": time.Now().UTC().Format(time.RFC3339)}

	if err := jctx.DBService.SetStateEntry(
		ctx, &orgUID, telegramAckedKey(incident.UID, chatID), value, &ttl,
	); err != nil {
		log.WarnContext(ctx, "could not record the telegram ack marker",
			"chatId", chatID, "error", err)
	}
}

// auditAckSend records the delivered notice on the incident's notification
// trail, attributed to the contact's owner when the chat still resolves to one.
func auditAckSend(
	ctx context.Context, jctx *jobdef.JobContext, log *slog.Logger,
	incident *models.Incident, chatID string, messageID int64,
) {
	userUID := telegramContactOwner(ctx, jctx, incident.OrganizationUID, chatID)
	if userUID == "" {
		// Nothing to attribute the row to (the contact was deleted between the
		// page and the acknowledgment); the delivery itself already happened.
		return
	}

	n := models.NewIncidentNotificationForUser(
		incident.OrganizationUID, incident.UID, string(models.EventTypeIncidentAcknowledged),
		models.IncidentNotificationSourceEscalationUser, userUID,
		models.UserContactTypeTelegram, nil, nil,
	)
	if err := jctx.DBService.CreateIncidentNotification(ctx, n); err != nil {
		log.WarnContext(ctx, "failed to create ack notice audit row", "error", err)

		return
	}

	_ = jctx.DBService.MarkIncidentNotificationSentByUID(
		ctx, n.UID, time.Now(), strconv.FormatInt(messageID, 10),
	)
}

// auditAckSkip records a notice that was deliberately not sent.
func auditAckSkip(
	ctx context.Context, jctx *jobdef.JobContext, log *slog.Logger,
	incident *models.Incident, reason string,
) {
	n := models.NewSkippedIncidentNotification(
		incident.OrganizationUID, incident.UID, string(models.EventTypeIncidentAcknowledged),
		models.IncidentNotificationSourceEscalationUser, reason, nil, nil,
	)
	if err := jctx.DBService.CreateIncidentNotification(ctx, n); err != nil {
		log.WarnContext(ctx, "failed to create ack notice skip audit row", "error", err)
	}
}
