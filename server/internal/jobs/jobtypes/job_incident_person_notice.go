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

// The shared machinery behind the two "tell the people who were PAGED" jobs
// added by spec 2026-08-28-07: incident_unack_notice and
// incident_comment_notice.
//
// Person contacts (Telegram today) are only ever reached by an escalation
// step, whose contract is "fire while the incident is unhandled". Everything
// that happens to the incident afterwards is therefore invisible to them
// unless a job goes and tells them — which is why incident_resolution_notice
// and incident_ack_notice already exist, and why these two had to.
//
// One traversal, two renderings: the thread anchors are the record of which
// chats were paged, the audit trail is the fallback when an anchor has expired,
// a per-chat marker makes retries safe, and failure policy is per chat so one
// blocked user cannot silence the other four.

const (
	// telegramUnackedStatePrefix namespaces the per-incident, per-chat marker
	// proving a retraction notice already went out to that chat.
	//
	// Incident-scoped, exactly like telegramAckedStatePrefix and for the same
	// reason: one retraction per incident per chat is the fact worth
	// delivering. An ack → unack → re-ack → unack sequence therefore announces
	// the FIRST withdrawal and stays quiet afterwards, which mirrors what the
	// ack notice does with the same marker discipline.
	telegramUnackedStatePrefix = "telegram_unacked:"
	// telegramCommentedStatePrefix namespaces the per-COMMENT, per-chat
	// marker.
	//
	// Comment-scoped, NOT incident-scoped — and that difference is the whole
	// feature. Comments are forwarded per comment, immediately, with no
	// batching and no coalescing window (decision 2026-08-28), so an
	// incident-scoped marker would suppress every comment after the first and
	// silently reduce the feature to "forward one comment per incident".
	telegramCommentedStatePrefix = "telegram_commented:"
	// personNoticeMarkerTTL matches the thread-anchor TTL.
	personNoticeMarkerTTL = 7 * 24 * time.Hour
	// personNoticeAuditScanLimit bounds the audit-row fallback scan, mirroring
	// ackAuditScanLimit.
	personNoticeAuditScanLimit = 500
	// personNoticeCommentPreviewMax caps a forwarded comment body. This lands
	// on a phone, not in a log.
	personNoticeCommentPreviewMax = 300
	// noticeActorUnknown is what an unattributed actor renders as. Shared by
	// every person-notice job so an unresolved name never becomes a blank line.
	noticeActorUnknown = "Someone"
	// noticeMarkerNotifiedAtKey is the timestamp field inside a per-chat
	// delivery marker.
	noticeMarkerNotifiedAtKey = "notifiedAt"
)

// ErrPersonNoticeIncomplete marks a run where at least one chat failed with a
// network-class error. Wrapped in a RetryableError so the job re-runs; chats
// that were notified kept their marker, so the re-run cannot duplicate.
var ErrPersonNoticeIncomplete = errors.New("incident person notice: some chats could not be notified")

// personNoticeKind selects the rendering and the marker discipline.
type personNoticeKind string

const (
	personNoticeKindUnack   personNoticeKind = "unack"
	personNoticeKindComment personNoticeKind = "comment"
)

// IncidentUnackNoticeJobConfig identifies the incident whose acknowledgment has
// been WITHDRAWN, and by whom.
//
// The actor rides on the config rather than being re-read from the event row,
// for the same reason IncidentAckNoticeJobConfig carries it: the notice then
// names exactly the person who withdrew, even if the incident is re-acked
// before the job runs.
type IncidentUnackNoticeJobConfig struct {
	OrganizationUID string `json:"organizationUid"`
	IncidentUID     string `json:"incidentUid"`
	// ActorName is the human label of whoever withdrew the acknowledgment.
	// Empty degrades to a neutral "Someone", never to a blank line.
	ActorName string `json:"actorName,omitempty"`
	// Via is the channel the unacknowledgment came from ("web" today — no chat
	// platform ships an unacknowledge button).
	Via string `json:"via,omitempty"`
}

// IncidentCommentNoticeJobConfig carries ONE comment to the people the
// escalation policy paged.
//
// The comment body travels on the config rather than being re-read at send
// time, matching NotificationJobConfig: the message that lands is the comment
// that was written, even if it is edited or the incident moves on in between.
type IncidentCommentNoticeJobConfig struct {
	OrganizationUID string `json:"organizationUid"`
	IncidentUID     string `json:"incidentUid"`
	// CommentEventUID scopes the per-chat marker to this one comment. Without
	// it, per-comment forwarding collapses into one-comment-per-incident.
	CommentEventUID string `json:"commentEventUid"`
	// AuthorName is the human label of the comment's author. Empty degrades to
	// a neutral fallback.
	AuthorName string `json:"authorName,omitempty"`
	// Text is the comment body as written.
	Text string `json:"text"`
}

// IncidentUnackNoticeJobDefinition is the factory for unack-notice jobs.
type IncidentUnackNoticeJobDefinition struct{}

// Type returns the job type identifier.
func (d *IncidentUnackNoticeJobDefinition) Type() jobdef.JobType {
	return jobdef.JobTypeIncidentUnackNotice
}

// CreateJobRun builds a new unack-notice run instance.
//
//nolint:ireturn // Factory pattern requires interface return
func (d *IncidentUnackNoticeJobDefinition) CreateJobRun(config json.RawMessage) (jobdef.JobRunner, error) {
	var cfg IncidentUnackNoticeJobConfig
	if err := json.Unmarshal(config, &cfg); err != nil {
		return nil, fmt.Errorf("parsing incident unack notice config: %w", err)
	}

	if cfg.OrganizationUID == "" {
		return nil, ErrMissingOrganizationUID
	}

	if cfg.IncidentUID == "" {
		return nil, ErrMissingIncidentUID
	}

	return &IncidentPersonNoticeJobRun{notice: personNotice{
		kind:            personNoticeKindUnack,
		organizationUID: cfg.OrganizationUID,
		incidentUID:     cfg.IncidentUID,
		actorName:       cfg.ActorName,
		via:             cfg.Via,
	}}, nil
}

// IncidentCommentNoticeJobDefinition is the factory for comment-notice jobs.
type IncidentCommentNoticeJobDefinition struct{}

// Type returns the job type identifier.
func (d *IncidentCommentNoticeJobDefinition) Type() jobdef.JobType {
	return jobdef.JobTypeIncidentCommentNotice
}

// CreateJobRun builds a new comment-notice run instance.
//
//nolint:ireturn // Factory pattern requires interface return
func (d *IncidentCommentNoticeJobDefinition) CreateJobRun(config json.RawMessage) (jobdef.JobRunner, error) {
	var cfg IncidentCommentNoticeJobConfig
	if err := json.Unmarshal(config, &cfg); err != nil {
		return nil, fmt.Errorf("parsing incident comment notice config: %w", err)
	}

	if cfg.OrganizationUID == "" {
		return nil, ErrMissingOrganizationUID
	}

	if cfg.IncidentUID == "" {
		return nil, ErrMissingIncidentUID
	}

	return &IncidentPersonNoticeJobRun{notice: personNotice{
		kind:            personNoticeKindComment,
		organizationUID: cfg.OrganizationUID,
		incidentUID:     cfg.IncidentUID,
		commentEventUID: cfg.CommentEventUID,
		authorName:      cfg.AuthorName,
		text:            cfg.Text,
	}}, nil
}

// personNotice is the decoded, kind-tagged payload both job types share.
type personNotice struct {
	kind            personNoticeKind
	organizationUID string
	incidentUID     string
	actorName       string
	via             string
	commentEventUID string
	authorName      string
	text            string
}

// IncidentPersonNoticeJobRun delivers one notice (a withdrawn acknowledgment,
// or one comment) to every chat this incident actually paged.
type IncidentPersonNoticeJobRun struct {
	notice personNotice
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
// the other four from hearing.
func (r *IncidentPersonNoticeJobRun) Run(ctx context.Context, jctx *jobdef.JobContext) error {
	log := jctx.Logger.With(
		"incidentUid", r.notice.incidentUID,
		"orgUid", r.notice.organizationUID,
		"noticeKind", string(r.notice.kind),
	)

	incident, err := jctx.DBService.GetIncident(ctx, r.notice.organizationUID, r.notice.incidentUID)
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

	params := r.alertParams(ctx, jctx, log, incident)
	r.handled = make(map[string]bool)

	anchored := r.notifyAnchoredChats(ctx, jctx, log, client, incident, params)
	r.notifyAuditedChats(ctx, jctx, log, client, incident, params, anchored)

	if r.transient {
		return jobdef.NewRetryableError(ErrPersonNoticeIncomplete)
	}

	return nil
}

// incidentWantsNotice is the gate — the ways a notice becomes wrong between
// the enqueue and the run.
//
// Shared by both kinds: a rolled-up child was never paged, so nobody is owed
// anything; and a RESOLVED incident has already had its conversation closed by
// the resolution notice, so neither "this is unowned again" (false — it is
// over) nor a comment forwarded as if the incident were live belongs there.
//
// One gate is kind-specific: a retraction whose acknowledgment has come BACK
// (re-acked before this job ran) must not go out, because it would tell four
// people to pick up an incident somebody is already on — the exact inversion
// of the harm this job exists to prevent. A comment has no such state: a
// comment on an acknowledged incident is the normal case and the whole point
// of forwarding it.
func (r *IncidentPersonNoticeJobRun) incidentWantsNotice(
	ctx context.Context, log *slog.Logger, incident *models.Incident,
) bool {
	if incident.PagingSuppressed {
		log.InfoContext(ctx, "incident person notice skipped — paging is suppressed for this incident")

		return false
	}

	if incident.State == models.IncidentStateResolved {
		log.InfoContext(ctx, "incident person notice skipped — the incident is already resolved")

		return false
	}

	if r.notice.kind == personNoticeKindUnack && incident.AcknowledgedAt != nil {
		log.InfoContext(ctx, "incident unack notice skipped — the incident was acknowledged again")

		return false
	}

	return true
}

// alertParams renders the message values for this notice kind. Same builder as
// a page — only the detail line differs.
func (r *IncidentPersonNoticeJobRun) alertParams(
	ctx context.Context, jctx *jobdef.JobContext, log *slog.Logger, incident *models.Incident,
) *telegram.AlertParams {
	params := telegramAlertParams(ctx, jctx, log, incident)

	// State is deliberately left as telegramAlertParams computed it. For an
	// unack that is DOWN or ESCALATED — which is the truth, and which also
	// restores the Acknowledge button (telegramAlertKeyboard attaches it while
	// AcknowledgedAt is nil), so the reader can take the incident from the
	// message that just told them nobody has it.
	params.Detail = r.detailLine()

	return params
}

// detailLine renders the one-line body.
//
// The unack wording is load-bearing and its second clause is not decoration:
// escalation RESUMES (from the step the acknowledgment interrupted). Wording
// that stopped at "nobody has it" would leave the reader assuming paging had
// stopped, which is the option that was considered and NOT chosen.
func (r *IncidentPersonNoticeJobRun) detailLine() string {
	if r.notice.kind == personNoticeKindUnack {
		actor := strings.TrimSpace(r.notice.actorName)
		if actor == "" {
			actor = noticeActorUnknown
		}

		detail := "acknowledgment withdrawn by " + actor

		if via := telegramAckViaLabel(r.notice.via); via != "" {
			detail += " " + via
		}

		return detail + " — unowned again, escalation resumes"
	}

	author := strings.TrimSpace(r.notice.authorName)
	if author == "" {
		author = noticeActorUnknown
	}

	return author + " commented: " + truncateForChat(r.notice.text, personNoticeCommentPreviewMax)
}

// truncateForChat bounds a body to a single readable chunk.
func truncateForChat(text string, maxLen int) string {
	text = strings.Join(strings.Fields(text), " ")
	if len(text) <= maxLen {
		return text
	}

	return strings.TrimSpace(text[:maxLen]) + "…"
}

// notifyAnchoredChats is the primary pass: the thread anchors ARE the record
// of which chats received a page for this incident, with the message id to
// thread the notice under.
//
// The anchors are READ and left in place, never consumed: the incident is
// still open, so the resolution notice still has to find them.
func (r *IncidentPersonNoticeJobRun) notifyAnchoredChats(
	ctx context.Context, jctx *jobdef.JobContext, log *slog.Logger,
	client *telegram.Client, incident *models.Incident, params *telegram.AlertParams,
) map[string]bool {
	orgUID := incident.OrganizationUID
	prefix := telegramThreadStatePrefix + incident.UID + ":"

	entries, err := jctx.DBService.ListStateEntries(ctx, &orgUID, prefix)
	if err != nil {
		// A DB hiccup here would silently drop every notice for the incident,
		// so it is exactly the case a retry exists for.
		log.WarnContext(ctx, "could not list telegram thread anchors for a person notice", "error", err)
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
// incident that outlived the 7-day TTL, or an anchor write that failed.
func (r *IncidentPersonNoticeJobRun) notifyAuditedChats(
	ctx context.Context, jctx *jobdef.JobContext, log *slog.Logger,
	client *telegram.Client, incident *models.Incident, params *telegram.AlertParams,
	anchored map[string]bool,
) {
	for _, userUID := range pagedTelegramUserUIDs(ctx, jctx, log, incident) {
		for _, chatID := range telegramChatsForUser(ctx, jctx, log, userUID, incident.OrganizationUID) {
			if anchored[chatID] {
				continue
			}

			r.notifyChat(ctx, jctx, log, client, incident, params, chatID, 0)
		}
	}
}

// pagedTelegramUserUIDs lists the distinct users this incident actually paged
// over Telegram, oldest row first.
//
// Restricted to `incident.escalated` rows for the same reason the resolution
// and ack notices restrict their scans: that is what a PAGE is audited as,
// while these jobs' own sends are audited under their own event types. Without
// the split the fallback would feed on its own output and re-notify on every
// retry.
func pagedTelegramUserUIDs(
	ctx context.Context, jctx *jobdef.JobContext, log *slog.Logger, incident *models.Incident,
) []string {
	rows, err := jctx.DBService.ListIncidentNotifications(ctx, incident.OrganizationUID,
		db.ListIncidentNotificationsFilter{
			IncidentUID: incident.UID,
			Status:      models.IncidentNotificationStatusSent,
			Limit:       personNoticeAuditScanLimit,
		})
	if err != nil {
		log.WarnContext(ctx, "could not read the notification audit trail for a person notice", "error", err)

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

// notifyChat sends one notice to a chat, guarded by the per-chat marker and
// the hourly runaway guard. anchorMsgID is the message to thread under, or 0
// for a chat reached through the audit fallback.
func (r *IncidentPersonNoticeJobRun) notifyChat(
	ctx context.Context, jctx *jobdef.JobContext, log *slog.Logger,
	client *telegram.Client, incident *models.Incident, params *telegram.AlertParams,
	chatID string, anchorMsgID int64,
) {
	if r.handled[chatID] {
		return
	}

	r.handled[chatID] = true

	if r.alreadyTold(ctx, jctx, log, incident, chatID) {
		return
	}

	if !reserveTelegramSend(ctx, jctx, log, incident) {
		r.auditSkip(ctx, jctx, log, incident, entitlements.RunawayKindTelegram+"_runaway_guard")

		return
	}

	messageID, err := sendTelegramAlertTo(ctx, jctx, log, client, incident, chatID, params, anchorMsgID)
	if err != nil {
		if telegramFailureIsTransient(err) {
			r.transient = true
		}

		log.WarnContext(ctx, "failed to send a telegram person notice",
			"chatId", chatID, "reason", telegram.FailureReason(err), "error", err)

		return
	}

	r.markTold(ctx, jctx, log, incident, chatID)
	r.auditSend(ctx, jctx, log, incident, chatID, messageID)
}

// markerKey is the per-chat marker proving this chat already heard THIS
// notice. Incident-scoped for a retraction, comment-scoped for a comment —
// see the prefix constants for why the difference is deliberate.
func (r *IncidentPersonNoticeJobRun) markerKey(incidentUID, chatID string) string {
	if r.notice.kind == personNoticeKindUnack {
		return telegramUnackedStatePrefix + incidentUID + ":" + chatID
	}

	return telegramCommentedStatePrefix + incidentUID + ":" + r.notice.commentEventUID + ":" + chatID
}

// eventType is what this notice is audited as on the incident's notification
// trail. Distinct from `incident.escalated` on purpose — see
// pagedTelegramUserUIDs.
func (r *IncidentPersonNoticeJobRun) eventType() string {
	if r.notice.kind == personNoticeKindUnack {
		return string(models.EventTypeIncidentUnacknowledged)
	}

	return string(models.EventTypeIncidentComment)
}

// alreadyTold reports whether this chat has already received this notice. A
// read failure answers "yes": skipping a notice is a missed message, sending a
// second one is a bug the user sees.
func (r *IncidentPersonNoticeJobRun) alreadyTold(
	ctx context.Context, jctx *jobdef.JobContext, log *slog.Logger,
	incident *models.Incident, chatID string,
) bool {
	orgUID := incident.OrganizationUID

	entry, err := jctx.DBService.GetStateEntry(ctx, &orgUID, r.markerKey(incident.UID, chatID))
	if err != nil {
		log.WarnContext(ctx, "could not read the telegram person-notice marker; skipping chat",
			"chatId", chatID, "error", err)

		return true
	}

	return entry != nil
}

// markTold records that this chat heard the notice. SetStateEntry (not
// SetStateEntryIfNotExists) on purpose: it resurrects a soft-deleted or expired
// row, so the marker is always actually in place afterwards.
func (r *IncidentPersonNoticeJobRun) markTold(
	ctx context.Context, jctx *jobdef.JobContext, log *slog.Logger,
	incident *models.Incident, chatID string,
) {
	orgUID := incident.OrganizationUID
	ttl := personNoticeMarkerTTL
	value := &models.JSONMap{noticeMarkerNotifiedAtKey: time.Now().UTC().Format(time.RFC3339)}

	if err := jctx.DBService.SetStateEntry(
		ctx, &orgUID, r.markerKey(incident.UID, chatID), value, &ttl,
	); err != nil {
		log.WarnContext(ctx, "could not record the telegram person-notice marker",
			"chatId", chatID, "error", err)
	}
}

// auditSend records the delivered notice on the incident's notification trail,
// attributed to the contact's owner when the chat still resolves to one.
func (r *IncidentPersonNoticeJobRun) auditSend(
	ctx context.Context, jctx *jobdef.JobContext, log *slog.Logger,
	incident *models.Incident, chatID string, messageID int64,
) {
	userUID := telegramContactOwner(ctx, jctx, incident.OrganizationUID, chatID)
	if userUID == "" {
		// Nothing to attribute the row to (the contact was deleted between the
		// page and now); the delivery itself already happened.
		return
	}

	n := models.NewIncidentNotificationForUser(
		incident.OrganizationUID, incident.UID, r.eventType(),
		models.IncidentNotificationSourceEscalationUser, userUID,
		models.UserContactTypeTelegram, nil, nil,
	)
	if err := jctx.DBService.CreateIncidentNotification(ctx, n); err != nil {
		log.WarnContext(ctx, "failed to create person notice audit row", "error", err)

		return
	}

	_ = jctx.DBService.MarkIncidentNotificationSentByUID(
		ctx, n.UID, time.Now(), strconv.FormatInt(messageID, 10),
	)
}

// auditSkip records a notice that was deliberately not sent.
func (r *IncidentPersonNoticeJobRun) auditSkip(
	ctx context.Context, jctx *jobdef.JobContext, log *slog.Logger,
	incident *models.Incident, reason string,
) {
	n := models.NewSkippedIncidentNotification(
		incident.OrganizationUID, incident.UID, r.eventType(),
		models.IncidentNotificationSourceEscalationUser, reason, nil, nil,
	)
	if err := jctx.DBService.CreateIncidentNotification(ctx, n); err != nil {
		log.WarnContext(ctx, "failed to create person notice skip audit row", "error", err)
	}
}
