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
	"github.com/fclairamb/solidping/server/internal/notifications"
)

const (
	// telegramResolvedStatePrefix namespaces the per-incident, per-chat marker
	// proving a resolution notice already went out to that chat. It is what
	// makes the job idempotent across retries AND across a reopen → re-resolve
	// cycle.
	telegramResolvedStatePrefix = "telegram_resolved:"
	// telegramResolvedMarkerTTL matches the thread-anchor TTL: past a week the
	// incident is long gone and so is any chance of a duplicate notice.
	telegramResolvedMarkerTTL = 7 * 24 * time.Hour
	// resolutionAuditScanLimit bounds the audit-row fallback scan. The rows are
	// one per paged person per escalation repeat, so this is generous.
	resolutionAuditScanLimit = 500
)

var (
	// ErrMissingOrganizationUID is returned when organizationUid is absent from
	// a job config that needs it to scope its lookups.
	ErrMissingOrganizationUID = errors.New("organizationUid is required")
	// ErrResolutionNoticeIncomplete marks a run where at least one chat failed
	// with a network-class error. Wrapped in a RetryableError so the job re-runs;
	// chats that were notified kept their marker, so the re-run cannot duplicate.
	ErrResolutionNoticeIncomplete = errors.New("incident resolution notice: some chats could not be notified")
)

// IncidentResolutionNoticeJobConfig identifies the incident whose resolution
// has to be announced to the people who were paged for it.
//
// Deliberately channel-generic: V1 delivers Telegram only, but WhatsApp has the
// same dormant resolved rendering and will hang off this same job rather than a
// second, parallel one.
type IncidentResolutionNoticeJobConfig struct {
	OrganizationUID string `json:"organizationUid"`
	IncidentUID     string `json:"incidentUid"`
}

// IncidentResolutionNoticeJobDefinition is the factory for resolution-notice jobs.
type IncidentResolutionNoticeJobDefinition struct{}

// Type returns the job type identifier.
func (d *IncidentResolutionNoticeJobDefinition) Type() jobdef.JobType {
	return jobdef.JobTypeIncidentResolutionNotice
}

// CreateJobRun builds a new resolution-notice run instance.
//
//nolint:ireturn // Factory pattern requires interface return
func (d *IncidentResolutionNoticeJobDefinition) CreateJobRun(config json.RawMessage) (jobdef.JobRunner, error) {
	var cfg IncidentResolutionNoticeJobConfig
	if err := json.Unmarshal(config, &cfg); err != nil {
		return nil, fmt.Errorf("parsing incident resolution notice config: %w", err)
	}

	if cfg.OrganizationUID == "" {
		return nil, ErrMissingOrganizationUID
	}

	if cfg.IncidentUID == "" {
		return nil, ErrMissingIncidentUID
	}

	return &IncidentResolutionNoticeJobRun{config: cfg}, nil
}

// IncidentResolutionNoticeJobRun closes the loop on a paged incident: everyone
// who received a page hears that it is over, in the same thread, and the
// original red alert stops looking live.
type IncidentResolutionNoticeJobRun struct {
	config IncidentResolutionNoticeJobConfig
	// handled dedupes chats within one run — a chat can appear both as a thread
	// anchor and (through its owner) as an audit row.
	handled map[string]bool
	// transient records that at least one chat failed for a network-class
	// reason, which is the ONLY thing that makes this job worth retrying.
	transient bool
}

// Run notifies every chat that was paged for this incident.
//
// Failure policy is per-chat, never per-job: one blocked user must not stop the
// other four from hearing the all-clear. Only a network-class failure asks for a
// retry, and a retry is safe because every notified chat lost its anchor and
// kept its marker.
func (r *IncidentResolutionNoticeJobRun) Run(ctx context.Context, jctx *jobdef.JobContext) error {
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

	params := resolutionAlertParams(ctx, jctx, log, incident)
	r.handled = make(map[string]bool)

	anchored := r.notifyAnchoredChats(ctx, jctx, log, client, incident, params)
	r.notifyAuditedChats(ctx, jctx, log, client, incident, params, anchored)

	if r.transient {
		return jobdef.NewRetryableError(ErrResolutionNoticeIncomplete)
	}

	return nil
}

// incidentWantsNotice is the gate: a rolled-up child was never paged, and an
// incident that relapsed before this job ran is not resolved any more. Both
// keep their anchors, so a later genuine resolution still notifies.
func (r *IncidentResolutionNoticeJobRun) incidentWantsNotice(
	ctx context.Context, log *slog.Logger, incident *models.Incident,
) bool {
	if incident.PagingSuppressed {
		log.InfoContext(ctx, "incident resolution notice skipped — paging is suppressed for this incident")

		return false
	}

	if incident.State != models.IncidentStateResolved {
		log.InfoContext(ctx, "incident resolution notice skipped — incident is not resolved any more",
			"state", incident.State)

		return false
	}

	return true
}

// telegramClientFor builds the instance bot client, or reports that this
// instance cannot send. Configured(), not Active(): reaching an already
// connected chat needs the token, never the bot @username.
func telegramClientFor(
	ctx context.Context, jctx *jobdef.JobContext, log *slog.Logger,
) (*telegram.Client, bool) {
	if jctx.AppConfig == nil || !jctx.AppConfig.Telegram.Configured() {
		log.InfoContext(ctx, "telegram not configured on this instance; no resolution notice to send")

		return nil, false
	}

	client, err := telegram.NewClientFromConfig(&jctx.AppConfig.Telegram)
	if err != nil {
		log.InfoContext(ctx, "telegram client unavailable; skipping resolution notice", "error", err)

		return nil, false
	}

	return client, true
}

// resolutionAlertParams renders the RESOLVED message values. Same builder as a
// page — only the detail line differs, because "resolved after 12m" is the one
// fact a resolution carries that an alert does not.
func resolutionAlertParams(
	ctx context.Context, jctx *jobdef.JobContext, log *slog.Logger, incident *models.Incident,
) *telegram.AlertParams {
	params := telegramAlertParams(ctx, jctx, log, incident)
	params.State = telegram.StateResolved

	if detail := telegramResolvedDetail(incident); detail != "" {
		params.Detail = detail
	}

	return params
}

// telegramResolvedDetail renders how long the incident lasted, or "" when the
// resolution timestamp is missing (the caller then keeps the alert detail).
func telegramResolvedDetail(incident *models.Incident) string {
	if incident.ResolvedAt == nil {
		return ""
	}

	elapsed := incident.ResolvedAt.Sub(incident.StartedAt)
	if elapsed < 0 {
		return ""
	}

	rounded := elapsed.Round(time.Minute)
	if elapsed < time.Minute {
		rounded = elapsed.Round(time.Second)
	}

	return "resolved after " + rounded.String()
}

// notifyAnchoredChats is the primary pass: the thread anchors ARE the record of
// which chats received a page for this incident, with the message id to thread
// the notice under. Returns the set of chats an anchor covered, so the audit
// fallback only has to deal with the ones it did not.
func (r *IncidentResolutionNoticeJobRun) notifyAnchoredChats(
	ctx context.Context, jctx *jobdef.JobContext, log *slog.Logger,
	client *telegram.Client, incident *models.Incident, params *telegram.AlertParams,
) map[string]bool {
	orgUID := incident.OrganizationUID
	prefix := telegramThreadStatePrefix + incident.UID + ":"

	entries, err := jctx.DBService.ListStateEntries(ctx, &orgUID, prefix)
	if err != nil {
		// A DB hiccup here would silently drop every notice for the incident, so
		// it is exactly the case a retry exists for.
		log.WarnContext(ctx, "could not list telegram thread anchors for the resolution notice", "error", err)
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
//
// KNOWN RESIDUAL WINDOW (documented, not fixed here). The anchor pass claims
// each chat with an atomic delete, so two concurrently running notice jobs for
// the same incident (resolve → reopen → re-resolve queues a second one) can
// never both send to an anchored chat. This pass has no such claim: the marker
// is a plain read-then-write, so two jobs racing on a chat whose anchor already
// expired past its 7-day TTL could both send. Closing it needs a store CAS that
// treats a soft-deleted row as absent — SetStateEntryIfNotExists cannot serve,
// because DeleteStateEntry only soft-deletes while the (organization_uid, key)
// uniqueness still covers the dead row, so a released reservation could never
// be re-taken and one throttled send would gag that chat forever. Adding that
// primitive is its own change; the worst case here is a duplicate all-clear in
// a chat nobody has been paged in for a week.
func (r *IncidentResolutionNoticeJobRun) notifyAuditedChats(
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
// The scan is restricted to `incident.escalated` rows on purpose: that is what a
// page is audited as, while this job's own sends are audited as
// `incident.resolved`. Without that split the fallback would feed on its own
// output and re-notify on every retry.
func (r *IncidentResolutionNoticeJobRun) pagedUserUIDs(
	ctx context.Context, jctx *jobdef.JobContext, log *slog.Logger, incident *models.Incident,
) []string {
	rows, err := jctx.DBService.ListIncidentNotifications(ctx, incident.OrganizationUID,
		db.ListIncidentNotificationsFilter{
			IncidentUID: incident.UID,
			Status:      models.IncidentNotificationStatusSent,
			Limit:       resolutionAuditScanLimit,
		})
	if err != nil {
		log.WarnContext(ctx, "could not read the notification audit trail for the resolution notice",
			"error", err)

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

// telegramChatsForUser resolves a user's currently verified Telegram chats in
// the org. A user who unlinked (or was un-verified after blocking the bot)
// resolves to nothing and is simply skipped.
func telegramChatsForUser(
	ctx context.Context, jctx *jobdef.JobContext, log *slog.Logger, userUID, orgUID string,
) []string {
	routes, err := jctx.DBService.ListUserContactsWithRoutes(ctx, userUID, orgUID)
	if err != nil {
		log.WarnContext(ctx, "could not load contacts for a paged user", "userUid", userUID, "error", err)

		return nil
	}

	out := make([]string, 0, len(routes))

	for _, route := range routes {
		contact := route.Contact
		if contact == nil || contact.Type != models.UserContactTypeTelegram ||
			contact.VerifiedAt == nil || contact.Value == "" {
			continue
		}

		out = append(out, contact.Value)
	}

	return out
}

// notifyChat sends one resolution notice to a chat, guarding it with the anchor
// claim, the per-chat marker and the hourly runaway guard. anchorMsgID is the
// message to thread under, or 0 for a chat reached through the audit fallback.
//
// Two guards, deliberately different in kind:
//
//   - The ANCHOR CLAIM (`DeleteStateEntry`) is a real compare-and-set on both
//     drivers: `UPDATE … WHERE deleted_at IS NULL` reports whether a live row
//     existed, so of two jobs racing on the same chat exactly one wins. That
//     matters because resolve → reopen → re-resolve queues a SECOND notice job
//     while the first may still be in flight. The anchor goes back if the notice
//     does not go out, so a claim is never a lost message.
//   - The MARKER is written AFTER a delivery, never reserved before one. A
//     reserve-then-release scheme cannot work on this store: DeleteStateEntry
//     only soft-deletes, while the (organization_uid, key) uniqueness still
//     covers the dead row — so a released marker could never be re-claimed and
//     one throttled send would gag that chat forever. It is what stops a re-run
//     and the audit fallback from repeating a delivered notice.
func (r *IncidentResolutionNoticeJobRun) notifyChat(
	ctx context.Context, jctx *jobdef.JobContext, log *slog.Logger,
	client *telegram.Client, incident *models.Incident, params *telegram.AlertParams,
	chatID string, anchorMsgID int64,
) {
	if r.handled[chatID] {
		return
	}

	r.handled[chatID] = true

	if anchorMsgID != 0 && !claimTelegramThreadAnchor(ctx, jctx, log, incident, chatID) {
		log.InfoContext(ctx, "another runner already claimed this chat's resolution notice",
			"chatId", chatID)

		return
	}

	if alreadyToldAboutResolution(ctx, jctx, log, incident, chatID) {
		// The anchor we just consumed was stale — a delivery already happened.
		// Leaving it deleted is the correct end state.
		return
	}

	if !reserveTelegramSend(ctx, jctx, log, incident) {
		restoreTelegramThreadAnchor(ctx, jctx, log, incident, chatID, anchorMsgID)
		auditResolutionSkip(ctx, jctx, log, incident, entitlements.RunawayKindTelegram+"_runaway_guard")

		return
	}

	messageID, err := sendTelegramAlertTo(ctx, jctx, log, client, incident, chatID, params, anchorMsgID)
	if err != nil {
		restoreTelegramThreadAnchor(ctx, jctx, log, incident, chatID, anchorMsgID)

		if telegramFailureIsTransient(err) {
			r.transient = true
		}

		log.WarnContext(ctx, "failed to send the telegram resolution notice",
			"chatId", chatID, "reason", telegram.FailureReason(err), "error", err)

		return
	}

	markResolutionTold(ctx, jctx, log, incident, chatID)
	auditResolutionSend(ctx, jctx, log, incident, chatID, messageID)
}

// claimTelegramThreadAnchor takes exclusive ownership of a chat's notice by
// consuming its thread anchor. Reports false when another runner got there
// first.
//
// Consuming the anchor here — rather than after a successful send — is also
// what stops a later reopen → re-resolve from announcing the end of a relapse
// the person was never paged about.
func claimTelegramThreadAnchor(
	ctx context.Context, jctx *jobdef.JobContext, log *slog.Logger,
	incident *models.Incident, chatID string,
) bool {
	orgUID := incident.OrganizationUID

	claimed, err := jctx.DBService.DeleteStateEntry(ctx, &orgUID, telegramThreadKey(incident.UID, chatID))
	if err != nil {
		// Without a claim there is no exclusion, so skip rather than risk two
		// jobs sending the same all-clear.
		log.WarnContext(ctx, "could not claim the telegram thread anchor; skipping chat",
			"chatId", chatID, "error", err)

		return false
	}

	return claimed
}

// restoreTelegramThreadAnchor puts a claimed anchor back when the notice did not
// go out, so the retry still threads under the original message instead of
// dropping a context-free standalone one. A no-op for the audit fallback, which
// never held an anchor.
func restoreTelegramThreadAnchor(
	ctx context.Context, jctx *jobdef.JobContext, log *slog.Logger,
	incident *models.Incident, chatID string, messageID int64,
) {
	if messageID == 0 {
		return
	}

	orgUID := incident.OrganizationUID
	ttl := telegramThreadTTL
	value := &models.JSONMap{telegramThreadMessageIDField: messageID}

	if err := jctx.DBService.SetStateEntry(
		ctx, &orgUID, telegramThreadKey(incident.UID, chatID), value, &ttl,
	); err != nil {
		log.WarnContext(ctx, "could not restore the telegram thread anchor",
			"chatId", chatID, "error", err)
	}
}

// telegramResolvedKey is the marker proving one chat already heard about this
// incident's resolution.
func telegramResolvedKey(incidentUID, chatID string) string {
	return telegramResolvedStatePrefix + incidentUID + ":" + chatID
}

// alreadyToldAboutResolution reports whether this chat has already received the
// notice. A read failure answers "yes": skipping a notice is a missed message,
// sending a second one is a bug the user sees.
func alreadyToldAboutResolution(
	ctx context.Context, jctx *jobdef.JobContext, log *slog.Logger,
	incident *models.Incident, chatID string,
) bool {
	orgUID := incident.OrganizationUID

	entry, err := jctx.DBService.GetStateEntry(ctx, &orgUID, telegramResolvedKey(incident.UID, chatID))
	if err != nil {
		log.WarnContext(ctx, "could not read the telegram resolution marker; skipping chat",
			"chatId", chatID, "error", err)

		return true
	}

	return entry != nil
}

// markResolutionTold records that this chat heard the all-clear. SetStateEntry
// (not SetStateEntryIfNotExists) on purpose: it resurrects a soft-deleted or
// expired row, so the marker is always actually in place afterwards.
func markResolutionTold(
	ctx context.Context, jctx *jobdef.JobContext, log *slog.Logger,
	incident *models.Incident, chatID string,
) {
	orgUID := incident.OrganizationUID
	ttl := telegramResolvedMarkerTTL
	value := &models.JSONMap{noticeMarkerNotifiedAtKey: time.Now().UTC().Format(time.RFC3339)}

	if err := jctx.DBService.SetStateEntry(
		ctx, &orgUID, telegramResolvedKey(incident.UID, chatID), value, &ttl,
	); err != nil {
		log.WarnContext(ctx, "could not record the telegram resolution marker",
			"chatId", chatID, "error", err)
	}
}

// reserveTelegramSend applies the per-org hourly runaway guard. A mass recovery
// (a network blip heals and forty incidents resolve at once) must not turn into
// an unbounded burst of sends.
func reserveTelegramSend(
	ctx context.Context, jctx *jobdef.JobContext, log *slog.Logger, incident *models.Incident,
) bool {
	if jctx.Services == nil || jctx.Services.Entitlements == nil {
		return true
	}

	if err := jctx.Services.Entitlements.ReserveTelegram(ctx, incident.OrganizationUID); err != nil {
		log.InfoContext(ctx, "telegram runaway guard reached; skipping resolution notice",
			"orgUID", incident.OrganizationUID, "error", err)

		return false
	}

	return true
}

// telegramFailureIsTransient reports whether a failed send is worth retrying the
// whole job for. A blocked user or a dead chat never becomes deliverable, and
// retrying those would only burn the runaway guard.
func telegramFailureIsTransient(err error) bool {
	return errors.Is(err, telegram.ErrRateLimited) ||
		errors.Is(err, telegram.ErrRequestFailed) ||
		notifications.IsNetworkError(err)
}

// auditResolutionSend records the delivered notice on the incident's
// notification trail, attributed to the contact's owner when the chat still
// resolves to one.
func auditResolutionSend(
	ctx context.Context, jctx *jobdef.JobContext, log *slog.Logger,
	incident *models.Incident, chatID string, messageID int64,
) {
	userUID := telegramContactOwner(ctx, jctx, incident.OrganizationUID, chatID)
	if userUID == "" {
		// Nothing to attribute the row to (the contact was deleted between the
		// page and the resolution); the delivery itself already happened.
		return
	}

	n := models.NewIncidentNotificationForUser(
		incident.OrganizationUID, incident.UID, string(models.EventTypeIncidentResolved),
		models.IncidentNotificationSourceEscalationUser, userUID,
		models.UserContactTypeTelegram, nil, nil,
	)
	if err := jctx.DBService.CreateIncidentNotification(ctx, n); err != nil {
		log.WarnContext(ctx, "failed to create resolution notice audit row", "error", err)

		return
	}

	_ = jctx.DBService.MarkIncidentNotificationSentByUID(
		ctx, n.UID, time.Now(), strconv.FormatInt(messageID, 10),
	)
}

// auditResolutionSkip records a notice that was deliberately not sent.
func auditResolutionSkip(
	ctx context.Context, jctx *jobdef.JobContext, log *slog.Logger,
	incident *models.Incident, reason string,
) {
	n := models.NewSkippedIncidentNotification(
		incident.OrganizationUID, incident.UID, string(models.EventTypeIncidentResolved),
		models.IncidentNotificationSourceEscalationUser, reason, nil, nil,
	)
	if err := jctx.DBService.CreateIncidentNotification(ctx, n); err != nil {
		log.WarnContext(ctx, "failed to create resolution notice skip audit row", "error", err)
	}
}

// telegramContactOwner resolves which user in this org owns a chat id, or ""
// when the chat is no longer linked here.
func telegramContactOwner(
	ctx context.Context, jctx *jobdef.JobContext, orgUID, chatID string,
) string {
	contacts, err := jctx.DBService.ListUserContactsByTypeValue(ctx, models.UserContactTypeTelegram, chatID)
	if err != nil {
		return ""
	}

	for _, contact := range contacts {
		if contact.OrganizationUID == orgUID {
			return contact.UserUID
		}
	}

	return ""
}
