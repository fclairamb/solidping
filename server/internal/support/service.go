// Package support implements the instance support inbox: capture of inbound
// human messages our bots cannot parse, and replies back through the channel
// they arrived on (spec 2026-08-22-02).
//
// Two invariants govern everything here:
//
//  1. CAPTURE IS THE INVARIANT. If a human sent it and the system could not act
//     on it, it is recorded. Nothing here may be allowed to change that.
//  2. A CAPTURE FAILURE MUST NEVER BREAK THE CHANNEL IT CAME FROM. The webhook
//     still returns its normal 2xx and the alerting path is untouched. Capture
//     is best-effort *for the request* — but a failure is logged at WARN and
//     counted in solidping_support_capture_total, never swallowed silently.
package support

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/uptrace/bun"

	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/email"
	"github.com/fclairamb/solidping/server/internal/prommetrics"
)

// Capture outcomes, used as the `outcome` metric label.
const (
	outcomeCaptured    = "captured"
	outcomeDeduplicate = "deduplicated"
	outcomeThrottled   = "throttled"
	outcomeFailed      = "failed"
)

// Errors returned by the service.
var (
	// ErrThreadNotFound is returned when no live thread matches the uid.
	ErrThreadNotFound = errors.New("support thread not found")
	// ErrReplyWindowClosed is returned when the channel will not accept a
	// free-form reply right now. It is a REFUSAL WE MAKE, deliberately, rather
	// than a provider error surfaced after the fact.
	ErrReplyWindowClosed = errors.New("the reply window for this thread has closed")
	// ErrNoReplier is returned when no adapter is registered for the channel.
	ErrNoReplier = errors.New("this channel cannot be replied to from the support inbox")
	// ErrEmptyReply is returned for a blank reply body.
	ErrEmptyReply = errors.New("reply body is empty")
)

// Inbound is one captured message, in channel-neutral terms.
type Inbound struct {
	// Channel is one of the models.SupportChannel* values.
	Channel string
	// Identity threads the conversation: an E.164 number, a Telegram chat id, a
	// Slack user id, a Discord user id.
	Identity string
	// ExternalID is the provider's message id. Meta and Twilio both retry on
	// any non-2xx, so a replay is guaranteed, not an edge case — this is what
	// makes capture idempotent.
	ExternalID string
	// Body is the message text. Non-text messages carry a placeholder here and
	// the real kind in RawType.
	Body string
	// RawType is one of the models.SupportRawType* values.
	RawType string
	// SenderLabel is a human-readable name for the subject line, when the
	// channel offers one.
	SenderLabel string
	// Context carries whatever the reply adapter needs that is not the identity
	// (a Slack IM channel id, a Discord DM channel id, a WhatsApp phone id).
	Context map[string]any
	// At is the provider's timestamp; zero means "now".
	At time.Time
}

// ReplyFunc sends an outbound reply through the thread's originating channel
// and returns the provider message id.
//
// Adapters are registered rather than imported so this package stays a leaf:
// the Slack and Discord integrations already depend on handlers, and importing
// them here would close an import cycle. server.go, which imports everything
// anyway, is where the wiring lives.
type ReplyFunc func(ctx context.Context, thread *models.SupportThread, body string) (string, error)

// Mailer is the subset of the email stack the mirror notification needs.
type Mailer interface {
	Send(ctx context.Context, msg *email.Message) (*email.SendResult, error)
}

// Service is the support inbox.
type Service struct {
	db      db.Service
	mailer  Mailer
	log     *slog.Logger
	now     func() time.Time
	baseURL string
	// replyTo is the instance support mailbox. Empty disables the mirror
	// notification entirely — the feature stays off as a whole, deliberately.
	replyTo  string
	repliers map[string]ReplyFunc

	messagesPerThread  *windowCounter
	threadsPerIdentity *windowCounter
	mirrorsPerHour     *windowCounter
	mirrorFoldWindow   time.Duration
}

// Options configures a Service.
type Options struct {
	Mailer  Mailer
	Logger  *slog.Logger
	BaseURL string
	ReplyTo string
	Now     func() time.Time
}

// NewService builds the support inbox service.
func NewService(dbSvc db.Service, opts Options) *Service {
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}

	nowFn := opts.Now
	if nowFn == nil {
		nowFn = time.Now
	}

	return &Service{
		db:                 dbSvc,
		mailer:             opts.Mailer,
		log:                logger,
		now:                nowFn,
		baseURL:            strings.TrimRight(opts.BaseURL, "/"),
		replyTo:            opts.ReplyTo,
		repliers:           make(map[string]ReplyFunc),
		messagesPerThread:  newWindowCounter(time.Hour, DefaultMessagesPerThreadPerHour),
		threadsPerIdentity: newWindowCounter(24*time.Hour, DefaultThreadsPerIdentityPerDay),
		mirrorsPerHour:     newWindowCounter(time.Hour, DefaultMirrorsPerHour),
		mirrorFoldWindow:   DefaultMirrorFoldWindow,
	}
}

// RegisterReplier wires an outbound adapter for one channel. Calling it twice
// for the same channel replaces the previous adapter.
func (s *Service) RegisterReplier(channel string, fn ReplyFunc) {
	if s == nil || fn == nil {
		return
	}

	s.repliers[channel] = fn
}

// CanReply reports whether an outbound adapter exists for a channel.
func (s *Service) CanReply(channel string) bool {
	if s == nil {
		return false
	}

	_, ok := s.repliers[channel]

	return ok
}

// CaptureSafe records an inbound message and NEVER returns an error or panics.
//
// This is the entry point every webhook uses. It exists so a capture failure —
// a DB outage, a panic in a parser, an abuse ceiling — cannot turn into a
// non-2xx on a provider webhook, which would make the provider retry, which
// would eventually make the provider disable the subscription. Losing one
// message is bad; losing the channel is worse.
func (s *Service) CaptureSafe(ctx context.Context, in *Inbound) {
	if s == nil || in == nil {
		return
	}

	defer func() {
		if rec := recover(); rec != nil {
			prommetrics.SupportCapture.WithLabelValues(in.Channel, outcomeFailed).Inc()
			s.log.WarnContext(ctx, "support capture panicked",
				"channel", in.Channel, "panic", fmt.Sprint(rec))
		}
	}()

	if _, _, err := s.Capture(ctx, in); err != nil {
		s.log.WarnContext(ctx, "support capture failed",
			"channel", in.Channel, "error", err)
	}
}

// Capture records an inbound message, creating or continuing a thread.
//
// Returns the thread and the message. A duplicate (same channel + external id)
// returns the existing rows and no error — a webhook retry is a normal event,
// not a fault.
func (s *Service) Capture(
	ctx context.Context, in *Inbound,
) (*models.SupportThread, *models.SupportMessage, error) {
	if !models.ValidSupportChannel(in.Channel) {
		prommetrics.SupportCapture.WithLabelValues(in.Channel, outcomeFailed).Inc()

		return nil, nil, fmt.Errorf("unknown support channel %q", in.Channel)
	}

	if in.Identity == "" {
		prommetrics.SupportCapture.WithLabelValues(in.Channel, outcomeFailed).Inc()

		return nil, nil, errors.New("inbound message has no channel identity")
	}

	at := in.At
	if at.IsZero() {
		at = s.now()
	}

	// Idempotency, first pass. The unique index is the real guarantee; this
	// check just avoids the round trip in the common case.
	if in.ExternalID != "" {
		existing, err := s.findMessageByExternalID(ctx, in.Channel, in.ExternalID)
		if err != nil {
			prommetrics.SupportCapture.WithLabelValues(in.Channel, outcomeFailed).Inc()

			return nil, nil, err
		}

		if existing != nil {
			prommetrics.SupportCapture.WithLabelValues(in.Channel, outcomeDeduplicate).Inc()

			thread, terr := s.GetThread(ctx, existing.ThreadUID)
			if terr != nil {
				return nil, existing, nil //nolint:nilerr // dedup path: the message is what matters
			}

			return thread, existing, nil
		}
	}

	thread, created, err := s.resolveThread(ctx, in, at)
	if err != nil {
		prommetrics.SupportCapture.WithLabelValues(in.Channel, outcomeFailed).Inc()

		return nil, nil, err
	}

	if !s.messagesPerThread.allow(thread.UID, at) {
		prommetrics.SupportCapture.WithLabelValues(in.Channel, outcomeThrottled).Inc()
		s.log.WarnContext(ctx, "support thread exceeded its hourly message ceiling; message dropped",
			"channel", in.Channel, "threadUid", thread.UID, "ceiling", DefaultMessagesPerThreadPerHour)

		return thread, nil, nil
	}

	msg := models.NewSupportMessage(thread.UID, in.Channel, models.SupportDirectionInbound, in.Body, at)
	if in.RawType != "" {
		msg.RawType = in.RawType
	}

	if in.ExternalID != "" {
		externalID := in.ExternalID
		msg.ExternalID = &externalID
	}

	if err := s.insertMessage(ctx, msg); err != nil {
		// A unique violation here means a concurrent replica captured the same
		// retry. That is the index doing its job, not a failure.
		if in.ExternalID != "" {
			if existing, lookupErr := s.findMessageByExternalID(ctx, in.Channel, in.ExternalID); lookupErr == nil &&
				existing != nil {
				prommetrics.SupportCapture.WithLabelValues(in.Channel, outcomeDeduplicate).Inc()

				return thread, existing, nil
			}
		}

		prommetrics.SupportCapture.WithLabelValues(in.Channel, outcomeFailed).Inc()

		return nil, nil, err
	}

	if err := s.touchThreadInbound(ctx, thread, at); err != nil {
		// The message is stored. A stale counter is not worth losing it over.
		s.log.WarnContext(ctx, "failed to update support thread after capture",
			"threadUid", thread.UID, "error", err)
	}

	prommetrics.SupportCapture.WithLabelValues(in.Channel, outcomeCaptured).Inc()

	// Notification is best-effort by construction: the message is already
	// stored, and a bounced notification is a smaller problem than a lost
	// message.
	s.mirror(ctx, thread, msg, created)

	return thread, msg, nil
}

// resolveThread finds the live thread for this identity or opens a new one.
func (s *Service) resolveThread(
	ctx context.Context, in *Inbound, at time.Time,
) (*models.SupportThread, bool, error) {
	thread, err := s.findLiveThread(ctx, in.Channel, in.Identity)
	if err != nil {
		return nil, false, err
	}

	if thread != nil {
		// Reply-routing context can change between messages (a Slack IM channel
		// is stable, a WhatsApp phone number id is not necessarily). Keep the
		// freshest one so a reply is never sent through stale routing.
		if len(in.Context) > 0 {
			thread.ChannelContext = models.JSONMap(in.Context)
			if uerr := s.updateThreadContext(ctx, thread); uerr != nil {
				s.log.WarnContext(ctx, "failed to refresh support thread context",
					"threadUid", thread.UID, "error", uerr)
			}
		}

		return thread, false, nil
	}

	if !s.threadsPerIdentity.allow(in.Channel+"\x00"+in.Identity, at) {
		return nil, false, fmt.Errorf(
			"identity opened more than %d threads today", DefaultThreadsPerIdentityPerDay)
	}

	thread = models.NewSupportThread(in.Channel, in.Identity, at)
	thread.Subject = buildSubject(in)

	if len(in.Context) > 0 {
		thread.ChannelContext = models.JSONMap(in.Context)
	}

	s.attribute(ctx, thread)

	if err := s.insertThread(ctx, thread); err != nil {
		// Lost the race against another replica opening the same thread. Use
		// theirs — the partial unique index guarantees there is exactly one.
		if existing, lookupErr := s.findLiveThread(ctx, in.Channel, in.Identity); lookupErr == nil &&
			existing != nil {
			return existing, false, nil
		}

		return nil, false, err
	}

	return thread, true, nil
}

// attribute resolves the sender to a verified user contact, when one exists.
//
// Attribution is a HINT FOR THE OPERATOR, never an access-control boundary: it
// records who we think this is, and it does not grant the org visibility of the
// thread. A sender that resolves to nobody is the normal case, not the broken
// one.
func (s *Service) attribute(ctx context.Context, thread *models.SupportThread) {
	contactType, ok := contactTypeForChannel(thread.Channel)
	if !ok {
		return
	}

	for _, candidate := range identityCandidates(thread.Channel, thread.ChannelIdentity) {
		contacts, err := s.db.ListUserContactsByTypeValue(ctx, contactType, candidate)
		if err != nil {
			s.log.WarnContext(ctx, "support attribution lookup failed",
				"channel", thread.Channel, "error", err)

			return
		}

		for _, contact := range contacts {
			if contact.VerifiedAt == nil {
				continue
			}

			userUID := contact.UserUID
			orgUID := contact.OrganizationUID
			thread.UserUID = &userUID
			thread.OrganizationUID = &orgUID

			return
		}
	}
}

// contactTypeForChannel maps a support channel to the user_contacts type that
// can identify its sender. Channels with no contact vocabulary (Discord today)
// simply never attribute.
func contactTypeForChannel(channel string) (string, bool) {
	switch channel {
	case models.SupportChannelWhatsApp:
		return models.UserContactTypeWhatsApp, true
	case models.SupportChannelTelegram:
		return models.UserContactTypeTelegram, true
	case models.SupportChannelSMS:
		return models.UserContactTypePhone, true
	case models.SupportChannelSlack:
		return models.UserContactTypeSlackUser, true
	case models.SupportChannelEmail:
		return models.UserContactTypeEmail, true
	default:
		return "", false
	}
}

// identityCandidates handles the one normalization that actually bites: WhatsApp
// delivers `From` without a leading '+', while a stored phone/WhatsApp contact
// is E.164 *with* one. Trying both is cheaper than guessing.
func identityCandidates(channel, identity string) []string {
	switch channel {
	case models.SupportChannelWhatsApp, models.SupportChannelSMS:
		if strings.HasPrefix(identity, "+") {
			return []string{identity, strings.TrimPrefix(identity, "+")}
		}

		return []string{identity, "+" + identity}
	default:
		return []string{identity}
	}
}

// buildSubject gives the thread a readable title. Kept short and derived from
// the first message, because an operator scanning a list needs to tell threads
// apart, not read them.
func buildSubject(in *Inbound) string {
	label := strings.TrimSpace(in.SenderLabel)
	if label == "" {
		label = in.Identity
	}

	body := strings.TrimSpace(in.Body)
	body = strings.ReplaceAll(body, "\n", " ")

	if body == "" {
		return label
	}

	const maxSubject = 60
	if runes := []rune(body); len(runes) > maxSubject {
		body = string(runes[:maxSubject]) + "…"
	}

	return label + ": " + body
}

// Reply sends an outbound reply through the thread's channel and records it.
//
// The window check happens HERE, before the send, so an expired WhatsApp thread
// produces our own explanatory refusal rather than a provider error the operator
// has to decode.
func (s *Service) Reply(
	ctx context.Context, threadUID, body, authorUID string,
) (*models.SupportMessage, error) {
	body = strings.TrimSpace(body)
	if body == "" {
		return nil, ErrEmptyReply
	}

	thread, err := s.GetThread(ctx, threadUID)
	if err != nil {
		return nil, err
	}

	replier, ok := s.repliers[thread.Channel]
	if !ok {
		return nil, ErrNoReplier
	}

	if window := thread.ReplyWindow(s.now()); !window.Open {
		return nil, fmt.Errorf("%w: %s", ErrReplyWindowClosed, window.Reason)
	}

	externalID, sendErr := replier(ctx, thread, body)

	at := s.now()
	msg := models.NewSupportMessage(thread.UID, thread.Channel, models.SupportDirectionOutbound, body, at)

	if authorUID != "" {
		author := authorUID
		msg.AuthorUID = &author
	}

	if externalID != "" {
		id := externalID
		msg.ExternalID = &id
	}

	// The delivery record is written whether or not the send worked. A reply
	// that failed and left no trace is how an operator ends up answering the
	// same person twice.
	if sendErr != nil {
		msg.Delivery = models.JSONMap{"status": "failed", "error": sendErr.Error()}
	} else {
		msg.Delivery = models.JSONMap{"status": "sent"}
	}

	if err := s.insertMessage(ctx, msg); err != nil {
		return nil, err
	}

	if err := s.touchThreadOutbound(ctx, thread, at); err != nil {
		s.log.WarnContext(ctx, "failed to update support thread after reply",
			"threadUid", thread.UID, "error", err)
	}

	if sendErr != nil {
		return msg, fmt.Errorf("sending reply on %s: %w", thread.Channel, sendErr)
	}

	return msg, nil
}

// ---------------------------------------------------------------------------
// Store
// ---------------------------------------------------------------------------

func (s *Service) bun() *bun.DB { return s.db.DB() }

func (s *Service) findLiveThread(
	ctx context.Context, channel, identity string,
) (*models.SupportThread, error) {
	thread := new(models.SupportThread)

	err := s.bun().NewSelect().Model(thread).
		Where("channel = ?", channel).
		Where("channel_identity = ?", identity).
		Where("status <> ?", models.SupportStatusClosed).
		Where("deleted_at is null").
		Limit(1).
		Scan(ctx)
	if err != nil {
		if errors.Is(err, sqlNoRows) {
			return nil, nil //nolint:nilnil // "no live thread" is a normal answer
		}

		return nil, err
	}

	return thread, nil
}

func (s *Service) findMessageByExternalID(
	ctx context.Context, channel, externalID string,
) (*models.SupportMessage, error) {
	msg := new(models.SupportMessage)

	err := s.bun().NewSelect().Model(msg).
		Where("channel = ?", channel).
		Where("external_id = ?", externalID).
		Limit(1).
		Scan(ctx)
	if err != nil {
		if errors.Is(err, sqlNoRows) {
			return nil, nil //nolint:nilnil // "not seen before" is a normal answer
		}

		return nil, err
	}

	return msg, nil
}

func (s *Service) insertThread(ctx context.Context, thread *models.SupportThread) error {
	_, err := s.bun().NewInsert().Model(thread).Exec(ctx)

	return err
}

func (s *Service) updateThreadContext(ctx context.Context, thread *models.SupportThread) error {
	_, err := s.bun().NewUpdate().Model(thread).
		Column("channel_context", "updated_at").
		Set("updated_at = ?", s.now()).
		WherePK().
		Exec(ctx)

	return err
}

func (s *Service) insertMessage(ctx context.Context, msg *models.SupportMessage) error {
	_, err := s.bun().NewInsert().Model(msg).Exec(ctx)

	return err
}

func (s *Service) touchThreadInbound(
	ctx context.Context, thread *models.SupportThread, at time.Time,
) error {
	thread.LastMessageAt = at
	thread.LastInboundAt = &at
	thread.UnreadCount++
	thread.UpdatedAt = at

	// A closed thread that receives a message is reopened: the partial unique
	// index only covers live threads, so a closed one can only be found here by
	// uid, but the status must still reflect reality.
	if thread.Status == models.SupportStatusClosed {
		thread.Status = models.SupportStatusOpen
	}

	_, err := s.bun().NewUpdate().Model(thread).
		Column("last_message_at", "last_inbound_at", "unread_count", "status", "updated_at").
		WherePK().
		Exec(ctx)

	return err
}

func (s *Service) touchThreadOutbound(
	ctx context.Context, thread *models.SupportThread, at time.Time,
) error {
	thread.LastMessageAt = at
	thread.UpdatedAt = at
	thread.UnreadCount = 0

	// Answering moves the thread to "pending" (waiting on them), never to
	// closed — closing is an operator decision.
	if thread.Status == models.SupportStatusOpen {
		thread.Status = models.SupportStatusPending
	}

	_, err := s.bun().NewUpdate().Model(thread).
		Column("last_message_at", "unread_count", "status", "updated_at").
		WherePK().
		Exec(ctx)

	return err
}

// GetThread loads one thread by uid.
func (s *Service) GetThread(ctx context.Context, uid string) (*models.SupportThread, error) {
	thread := new(models.SupportThread)

	err := s.bun().NewSelect().Model(thread).
		Where("uid = ?", uid).
		Where("deleted_at is null").
		Limit(1).
		Scan(ctx)
	if err != nil {
		if errors.Is(err, sqlNoRows) {
			return nil, ErrThreadNotFound
		}

		return nil, err
	}

	return thread, nil
}

// ListThreads returns threads newest-activity-first.
func (s *Service) ListThreads(
	ctx context.Context, filter models.ListSupportThreadsFilter,
) ([]*models.SupportThread, error) {
	limit := filter.Limit
	if limit <= 0 {
		limit = 50
	}

	if limit > 500 {
		limit = 500
	}

	threads := make([]*models.SupportThread, 0, limit)

	query := s.bun().NewSelect().Model(&threads).
		Where("deleted_at is null").
		OrderExpr("last_message_at DESC").
		Limit(limit)

	if filter.Status != "" {
		query = query.Where("status = ?", filter.Status)
	}

	if filter.Channel != "" {
		query = query.Where("channel = ?", filter.Channel)
	}

	if filter.Query != "" {
		pattern := "%" + strings.ToLower(filter.Query) + "%"
		query = query.Where(
			"lower(channel_identity) LIKE ? OR lower(subject) LIKE ?", pattern, pattern)
	}

	if err := query.Scan(ctx); err != nil {
		return nil, err
	}

	return threads, nil
}

// ListMessages returns a thread's messages in chronological order.
func (s *Service) ListMessages(
	ctx context.Context, threadUID string, limit int,
) ([]*models.SupportMessage, error) {
	if limit <= 0 {
		limit = 200
	}

	if limit > 1000 {
		limit = 1000
	}

	messages := make([]*models.SupportMessage, 0, limit)

	err := s.bun().NewSelect().Model(&messages).
		Where("thread_uid = ?", threadUID).
		OrderExpr("created_at ASC").
		Limit(limit).
		Scan(ctx)
	if err != nil {
		return nil, err
	}

	return messages, nil
}

// UpdateThread applies an operator's status/subject change.
func (s *Service) UpdateThread(
	ctx context.Context, uid string, status, subject *string,
) (*models.SupportThread, error) {
	thread, err := s.GetThread(ctx, uid)
	if err != nil {
		return nil, err
	}

	columns := []string{"updated_at"}

	if status != nil {
		if !models.ValidSupportStatus(*status) {
			return nil, fmt.Errorf("invalid support status %q", *status)
		}

		thread.Status = *status
		columns = append(columns, "status")

		// Closing marks it read: an operator closing a thread has, by
		// definition, dealt with it.
		if *status == models.SupportStatusClosed {
			thread.UnreadCount = 0
			columns = append(columns, "unread_count")
		}
	}

	if subject != nil {
		thread.Subject = strings.TrimSpace(*subject)
		columns = append(columns, "subject")
	}

	thread.UpdatedAt = s.now()

	if _, err := s.bun().NewUpdate().Model(thread).Column(columns...).WherePK().Exec(ctx); err != nil {
		return nil, err
	}

	return thread, nil
}

// MarkRead zeroes a thread's unread counter.
func (s *Service) MarkRead(ctx context.Context, uid string) error {
	_, err := s.bun().NewUpdate().Model((*models.SupportThread)(nil)).
		Set("unread_count = 0").
		Set("updated_at = ?", s.now()).
		Where("uid = ?", uid).
		Exec(ctx)

	return err
}

// PurgeClosedBefore deletes closed threads last touched before `before`, and
// their messages by cascade. Returns how many threads went.
//
// Message bodies are PERSONAL DATA — this is the retention half of the privacy
// obligation the feature carries, and DetachOrganization below is the erasure
// half.
func (s *Service) PurgeClosedBefore(ctx context.Context, before time.Time, batch int) (int64, error) {
	if batch <= 0 {
		batch = 1000
	}

	// Messages first and explicitly: `on delete cascade` is declared, but
	// SQLite only honours it with foreign_keys=ON, and a purge that silently
	// leaves the bodies behind would be the one failure mode that matters here.
	uids := make([]string, 0, batch)

	err := s.bun().NewSelect().Model((*models.SupportThread)(nil)).
		Column("uid").
		Where("status = ?", models.SupportStatusClosed).
		Where("updated_at < ?", before).
		Where("deleted_at is null").
		Limit(batch).
		Scan(ctx, &uids)
	if err != nil {
		return 0, err
	}

	if len(uids) == 0 {
		return 0, nil
	}

	if _, err := s.bun().NewDelete().Model((*models.SupportMessage)(nil)).
		Where("thread_uid IN (?)", bun.In(uids)).
		Exec(ctx); err != nil {
		return 0, err
	}

	res, err := s.bun().NewDelete().Model((*models.SupportThread)(nil)).
		Where("uid IN (?)", bun.In(uids)).
		Exec(ctx)
	if err != nil {
		return 0, err
	}

	affected, err := res.RowsAffected()
	if err != nil {
		return int64(len(uids)), nil //nolint:nilerr // driver without RowsAffected support
	}

	return affected, nil
}

// DetachOrganization strips attribution from every thread pointing at a deleted
// organization.
//
// The thread itself SURVIVES. Attribution is a hint about who we think wrote in,
// not ownership: deleting an org must not delete a stranger's support
// conversation, and it must not leave a dangling reference either.
func (s *Service) DetachOrganization(ctx context.Context, orgUID string) (int64, error) {
	res, err := s.bun().NewUpdate().Model((*models.SupportThread)(nil)).
		Set("organization_uid = NULL").
		Set("user_uid = NULL").
		Set("updated_at = ?", s.now()).
		Where("organization_uid = ?", orgUID).
		Exec(ctx)
	if err != nil {
		return 0, err
	}

	affected, err := res.RowsAffected()
	if err != nil {
		return 0, nil //nolint:nilerr // driver without RowsAffected support
	}

	return affected, nil
}
