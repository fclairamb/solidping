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
	"encoding/json"
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

// deliveryStatusKey is the key under which an outbound reply's delivery state
// lives in support_messages.delivery.
const deliveryStatusKey = "status"

// Delivery states stored under deliveryStatusKey.
const (
	deliveryStatusSent   = "sent"
	deliveryStatusFailed = "failed"
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
	// ErrNoReplyRoute is returned when the channel HAS an adapter but this
	// particular thread cannot be routed back through it — a Slack workspace
	// with no stored connection, a Discord thread with no channel id, an org
	// with no SMS sender.
	//
	// It is the per-thread sibling of ErrNoReplier, and like it, it is a refusal
	// we make BEFORE touching the provider. Nothing is stored on this path: the
	// send was never attempted, so there is no attempt to record.
	ErrNoReplyRoute = errors.New("this thread cannot be routed back to its channel")
	// ErrMessageNotFound is returned when a resend names a message that is not
	// in the thread.
	ErrMessageNotFound = errors.New("support message not found in this thread")
	// ErrNotResendable is returned when a resend names a message that is not a
	// failed outbound reply.
	ErrNotResendable = errors.New("only an outbound reply whose delivery failed can be resent")
	// ErrEmptyReply is returned for a blank reply body.
	ErrEmptyReply = errors.New("reply body is empty")
	// ErrUnknownChannel is returned for an inbound message on a channel the
	// support inbox does not model.
	ErrUnknownChannel = errors.New("unknown support channel")
	// ErrNoIdentity is returned when an inbound message carries nothing to
	// thread the conversation on.
	ErrNoIdentity = errors.New("inbound message has no channel identity")
	// ErrTooManyThreads is returned when one identity has opened more new
	// threads today than the abuse ceiling allows.
	ErrTooManyThreads = errors.New("identity opened too many support threads today")
	// ErrInvalidStatus is returned for a status outside open/pending/closed.
	ErrInvalidStatus = errors.New("invalid support status")
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

// ReplyRoute is the PER-THREAD answer to "can we answer this person right now?".
//
// It is deliberately not a bare bool: an operator staring at a disabled reply
// box needs to know whether the workspace was never installed, the thread lost
// its channel id, or the instance has no SMS at all — three very different
// things to go and fix.
type ReplyRoute struct {
	// CanReply is false when the adapter has no usable route for this thread.
	CanReply bool
	// Reason explains a false CanReply in operator-facing terms. Empty when
	// CanReply is true.
	Reason string
}

// RouteFunc is the PRE-FLIGHT companion to a ReplyFunc: it answers, for one
// specific thread, whether the adapter could route a reply — without sending
// anything.
//
// It is a separate function rather than a dry-run flag on ReplyFunc precisely
// because its type cannot send: it returns an answer and a reason, not a
// provider message id, so there is nowhere for an accidental send to put its
// result. A dry-run boolean would leave every adapter one missed branch away
// from posting a message to a customer while merely rendering a list.
//
// It must NOT call the provider. It resolves local routing state — is there a
// stored connection, a channel id, an SMS sender — and nothing more: this runs
// once per thread on every inbox render, and a provider that is merely slow
// must never start reading as "cannot reply".
type RouteFunc func(ctx context.Context, thread *models.SupportThread) ReplyRoute

// replier pairs a channel's outbound adapter with its optional pre-flight.
type replier struct {
	send ReplyFunc
	// route is nil for channels whose reachability is decided entirely by
	// instance config at registration time (WhatsApp, Telegram): there is
	// nothing per-thread left to resolve, and a stub that always said yes would
	// only be one more thing to keep in sync.
	route RouteFunc
}

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
	repliers map[string]replier

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
		repliers:           make(map[string]replier),
		messagesPerThread:  newWindowCounter(time.Hour, DefaultMessagesPerThreadPerHour),
		threadsPerIdentity: newWindowCounter(24*time.Hour, DefaultThreadsPerIdentityPerDay),
		mirrorsPerHour:     newWindowCounter(time.Hour, DefaultMirrorsPerHour),
		mirrorFoldWindow:   DefaultMirrorFoldWindow,
	}
}

// RegisterReplier wires an outbound adapter for one channel, with no per-thread
// pre-flight: registered means routable. Calling it twice for the same channel
// replaces the previous adapter.
//
// Use it only where reachability is genuinely a property of the instance rather
// than of the thread — WhatsApp and Telegram, whose registration is already
// gated on their config being present.
func (s *Service) RegisterReplier(channel string, fn ReplyFunc) {
	s.RegisterRoutedReplier(channel, fn, nil)
}

// RegisterRoutedReplier wires an outbound adapter together with the pre-flight
// that decides, per thread, whether that adapter has a route.
//
// This is the form every channel whose reachability varies by thread must use —
// Slack (is there a stored connection for THIS workspace?), Discord (does the
// thread carry a channel id?), SMS (does this org resolve to a sender?).
// Registering such a channel with RegisterReplier is the exact bug this
// distinction exists to prevent.
func (s *Service) RegisterRoutedReplier(channel string, fn ReplyFunc, route RouteFunc) {
	if s == nil || fn == nil {
		return
	}

	s.repliers[channel] = replier{send: fn, route: route}
}

// ReplyRouteFor answers, for ONE thread, whether a reply can be routed.
//
// This replaced a channel-level CanReply(channel) lookup, which asked a
// per-thread question of a boot-time map and therefore reported every Slack
// thread as answerable whether or not its workspace had ever been connected.
// The channel-level form is deliberately gone rather than deprecated: leaving it
// callable is leaving the bug callable.
func (s *Service) ReplyRouteFor(ctx context.Context, thread *models.SupportThread) ReplyRoute {
	if s == nil || thread == nil {
		return ReplyRoute{}
	}

	entry, ok := s.repliers[thread.Channel]
	if !ok {
		return ReplyRoute{Reason: "this channel has no reply adapter on this instance"}
	}

	if entry.route == nil {
		return ReplyRoute{CanReply: true}
	}

	return entry.route(ctx, thread)
}

// ReplyRoutes answers the pre-flight for a whole listing, memoized on the inputs
// a route decision actually depends on.
//
// Without the memo an inbox page is an N+1: the Slack pre-flight is a connection
// lookup, and a 500-thread listing from one busy workspace would run 500
// identical ones. Threads sharing a channel, an attributed org and a channel
// context resolve identically by construction — that tuple is exactly what the
// registered RouteFuncs read.
func (s *Service) ReplyRoutes(
	ctx context.Context, threads []*models.SupportThread,
) map[string]ReplyRoute {
	routes := make(map[string]ReplyRoute, len(threads))
	if s == nil {
		return routes
	}

	memo := make(map[string]ReplyRoute, len(threads))

	for _, thread := range threads {
		if thread == nil {
			continue
		}

		key := routeMemoKey(thread)

		route, hit := memo[key]
		if !hit {
			route = s.ReplyRouteFor(ctx, thread)
			memo[key] = route
		}

		routes[thread.UID] = route
	}

	return routes
}

// routeMemoKey builds the memo key from everything a RouteFunc may read.
//
// A marshaling failure falls back to the thread uid, which simply defeats the
// memo for that one row — never shares an answer between threads that might not
// deserve it.
func routeMemoKey(thread *models.SupportThread) string {
	orgUID := ""
	if thread.OrganizationUID != nil {
		orgUID = *thread.OrganizationUID
	}

	// encoding/json sorts map keys, so this is stable across threads.
	encoded, err := json.Marshal(thread.ChannelContext)
	if err != nil {
		return "uid\x00" + thread.UID
	}

	return thread.Channel + "\x00" + orgUID + "\x00" + string(encoded)
}

// CaptureSafe records an inbound message and NEVER returns an error or panics.
//
// This is the entry point every webhook uses. It exists so a capture failure —
// a DB outage, a panic in a parser, an abuse ceiling — cannot turn into a
// non-2xx on a provider webhook, which would make the provider retry, which
// would eventually make the provider disable the subscription. Losing one
// message is bad; losing the channel is worse.
func (s *Service) CaptureSafe(ctx context.Context, inbound *Inbound) {
	if s == nil || inbound == nil {
		return
	}

	defer func() {
		if rec := recover(); rec != nil {
			prommetrics.SupportCapture.WithLabelValues(inbound.Channel, outcomeFailed).Inc()
			s.log.WarnContext(ctx, "support capture panicked",
				"channel", inbound.Channel, "panic", fmt.Sprint(rec))
		}
	}()

	if _, _, err := s.Capture(ctx, inbound); err != nil {
		s.log.WarnContext(ctx, "support capture failed",
			"channel", inbound.Channel, "error", err)
	}
}

// Capture records an inbound message, creating or continuing a thread.
//
// Returns the thread and the message. A duplicate (same channel + external id)
// returns the existing rows and no error — a webhook retry is a normal event,
// not a fault.
func (s *Service) Capture(
	ctx context.Context, inbound *Inbound,
) (*models.SupportThread, *models.SupportMessage, error) {
	if !models.ValidSupportChannel(inbound.Channel) {
		prommetrics.SupportCapture.WithLabelValues(inbound.Channel, outcomeFailed).Inc()

		return nil, nil, fmt.Errorf("%w: %q", ErrUnknownChannel, inbound.Channel)
	}

	if inbound.Identity == "" {
		prommetrics.SupportCapture.WithLabelValues(inbound.Channel, outcomeFailed).Inc()

		return nil, nil, ErrNoIdentity
	}

	occurredAt := inbound.At
	if occurredAt.IsZero() {
		occurredAt = s.now()
	}

	if thread, existing, seen, err := s.alreadyCaptured(ctx, inbound); err != nil {
		return nil, nil, err
	} else if seen {
		return thread, existing, nil
	}

	thread, created, err := s.resolveThread(ctx, inbound, occurredAt)
	if err != nil {
		prommetrics.SupportCapture.WithLabelValues(inbound.Channel, captureOutcomeFor(err)).Inc()

		return nil, nil, err
	}

	if !s.messagesPerThread.allow(thread.UID, occurredAt) {
		prommetrics.SupportCapture.WithLabelValues(inbound.Channel, outcomeThrottled).Inc()
		s.log.WarnContext(ctx, "support thread exceeded its hourly message ceiling; message dropped",
			"channel", inbound.Channel, "threadUid", thread.UID, "ceiling", DefaultMessagesPerThreadPerHour)

		return thread, nil, nil
	}

	msg := models.NewSupportMessage(thread.UID, inbound.Channel, models.SupportDirectionInbound, inbound.Body, occurredAt)
	if inbound.RawType != "" {
		msg.RawType = inbound.RawType
	}

	if inbound.ExternalID != "" {
		externalID := inbound.ExternalID
		msg.ExternalID = &externalID
	}

	if err := s.insertMessage(ctx, msg); err != nil {
		// A unique violation here means a concurrent replica captured the same
		// retry. That is the index doing its job, not a failure.
		if inbound.ExternalID != "" {
			if existing, lookupErr := s.findMessageByExternalID(ctx, inbound.Channel, inbound.ExternalID); lookupErr == nil &&
				existing != nil {
				prommetrics.SupportCapture.WithLabelValues(inbound.Channel, outcomeDeduplicate).Inc()

				return thread, existing, nil
			}
		}

		prommetrics.SupportCapture.WithLabelValues(inbound.Channel, outcomeFailed).Inc()

		return nil, nil, err
	}

	if err := s.touchThreadInbound(ctx, thread, occurredAt); err != nil {
		// The message is stored. A stale counter is not worth losing it over.
		s.log.WarnContext(ctx, "failed to update support thread after capture",
			"threadUid", thread.UID, "error", err)
	}

	prommetrics.SupportCapture.WithLabelValues(inbound.Channel, outcomeCaptured).Inc()

	// Notification is best-effort by construction: the message is already
	// stored, and a bounced notification is a smaller problem than a lost
	// message.
	s.mirror(ctx, thread, msg, created)

	return thread, msg, nil
}

// captureOutcomeFor classifies a capture error for the metric label.
//
// An abuse ceiling is a THROTTLE, not a fault. Counting it as `failed` would
// make a flood of new identities look like a capture outage and hide a real one
// behind the noise.
func captureOutcomeFor(err error) string {
	if errors.Is(err, ErrTooManyThreads) {
		return outcomeThrottled
	}

	return outcomeFailed
}

// alreadyCaptured is the idempotency pre-check. The unique index is the real
// guarantee; this only avoids the insert round trip in the common case, which
// IS the common case — Meta and Twilio both retry on any non-2xx.
//
// Returns seen=true when this provider message id has been recorded before.
func (s *Service) alreadyCaptured(
	ctx context.Context, inbound *Inbound,
) (*models.SupportThread, *models.SupportMessage, bool, error) {
	if inbound.ExternalID == "" {
		return nil, nil, false, nil
	}

	existing, err := s.findMessageByExternalID(ctx, inbound.Channel, inbound.ExternalID)
	if err != nil {
		prommetrics.SupportCapture.WithLabelValues(inbound.Channel, outcomeFailed).Inc()

		return nil, nil, false, err
	}

	if existing == nil {
		return nil, nil, false, nil
	}

	prommetrics.SupportCapture.WithLabelValues(inbound.Channel, outcomeDeduplicate).Inc()

	thread, err := s.GetThread(ctx, existing.ThreadUID)
	if err != nil {
		// The message is what matters; a thread we cannot re-read does not make
		// this a failure.
		return nil, existing, true, nil //nolint:nilerr // see above
	}

	return thread, existing, true, nil
}

// resolveThread finds the live thread for this identity or opens a new one.
func (s *Service) resolveThread(
	ctx context.Context, inbound *Inbound, occurredAt time.Time,
) (*models.SupportThread, bool, error) {
	thread, err := s.findLiveThread(ctx, inbound.Channel, inbound.Identity)
	if err != nil {
		return nil, false, err
	}

	if thread != nil {
		// Reply-routing context can change between messages (a Slack IM channel
		// is stable, a WhatsApp phone number id is not necessarily). Keep the
		// freshest one so a reply is never sent through stale routing.
		if len(inbound.Context) > 0 {
			thread.ChannelContext = models.JSONMap(inbound.Context)
			if uerr := s.updateThreadContext(ctx, thread); uerr != nil {
				s.log.WarnContext(ctx, "failed to refresh support thread context",
					"threadUid", thread.UID, "error", uerr)
			}
		}

		return thread, false, nil
	}

	if !s.threadsPerIdentity.allow(inbound.Channel+"\x00"+inbound.Identity, occurredAt) {
		return nil, false, fmt.Errorf(
			"%w: ceiling is %d per day", ErrTooManyThreads, DefaultThreadsPerIdentityPerDay)
	}

	thread = models.NewSupportThread(inbound.Channel, inbound.Identity, occurredAt)
	thread.Subject = buildSubject(inbound)

	if len(inbound.Context) > 0 {
		thread.ChannelContext = models.JSONMap(inbound.Context)
	}

	s.attribute(ctx, thread)

	if err := s.insertThread(ctx, thread); err != nil {
		// Lost the race against another replica opening the same thread. Use
		// theirs — the partial unique index guarantees there is exactly one.
		if existing, lookupErr := s.findLiveThread(ctx, inbound.Channel, inbound.Identity); lookupErr == nil &&
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
func buildSubject(inbound *Inbound) string {
	label := strings.TrimSpace(inbound.SenderLabel)
	if label == "" {
		label = inbound.Identity
	}

	body := strings.TrimSpace(inbound.Body)
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

	entry, ok := s.repliers[thread.Channel]
	if !ok {
		return nil, ErrNoReplier
	}

	if window := thread.ReplyWindow(s.now()); !window.Open {
		return nil, fmt.Errorf("%w: %s", ErrReplyWindowClosed, window.Reason)
	}

	// The SAME pre-flight the dashboard rendered its disabled box from, run
	// again here. The UI check is a courtesy; this one is the rule, and it is
	// what stops a stale tab, a scripted caller or a race from storing an
	// outbound message that never had anywhere to go.
	//
	// Nothing is written below on this path: no message row, no thread touch.
	// An unroutable reply was never attempted, so there is no attempt to
	// record — unlike a send that reached the provider and was rejected, which
	// is stored with `Delivery failed` further down for the opposite reason.
	if route := s.routeFor(ctx, thread, entry); !route.CanReply {
		return nil, fmt.Errorf("%w: %s", ErrNoReplyRoute, route.Reason)
	}

	externalID, sendErr := entry.send(ctx, thread, body)

	occurredAt := s.now()
	msg := models.NewSupportMessage(thread.UID, thread.Channel, models.SupportDirectionOutbound, body, occurredAt)

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
		msg.Delivery = models.JSONMap{deliveryStatusKey: deliveryStatusFailed, "error": sendErr.Error()}
	} else {
		msg.Delivery = models.JSONMap{deliveryStatusKey: deliveryStatusSent}
	}

	if err := s.insertMessage(ctx, msg); err != nil {
		return nil, err
	}

	if err := s.touchThreadOutbound(ctx, thread, occurredAt); err != nil {
		s.log.WarnContext(ctx, "failed to update support thread after reply",
			"threadUid", thread.UID, "error", err)
	}

	if sendErr != nil {
		return msg, fmt.Errorf("sending reply on %s: %w", thread.Channel, sendErr)
	}

	return msg, nil
}

// routeFor runs one registered replier's pre-flight, or reports "routable" when
// the channel registered none.
func (s *Service) routeFor(
	ctx context.Context, thread *models.SupportThread, entry replier,
) ReplyRoute {
	if entry.route == nil {
		return ReplyRoute{CanReply: true}
	}

	return entry.route(ctx, thread)
}

// Resend re-attempts delivery of an outbound reply whose send failed, rewriting
// that message's delivery record in place.
//
// This is the way out of the state the pre-flight cannot undo: replies stored
// before a route existed (or during a provider outage) are the operator's own
// words, visibly kept and permanently unsent. Resending re-runs the full
// pre-flight — window, then route — so a message queued against a workspace
// that has since been connected simply goes, and one that still has no route is
// refused with the current reason rather than failing again at the provider.
//
// Only a FAILED OUTBOUND message qualifies. Resending a delivered reply would
// send a customer the same text twice, which is worse than the problem.
func (s *Service) Resend(
	ctx context.Context, threadUID, messageUID string,
) (*models.SupportMessage, error) {
	thread, err := s.GetThread(ctx, threadUID)
	if err != nil {
		return nil, err
	}

	msg, err := s.findMessage(ctx, threadUID, messageUID)
	if err != nil {
		return nil, err
	}

	if msg.Direction != models.SupportDirectionOutbound ||
		msg.Delivery[deliveryStatusKey] != deliveryStatusFailed {
		return nil, ErrNotResendable
	}

	entry, ok := s.repliers[thread.Channel]
	if !ok {
		return nil, ErrNoReplier
	}

	if window := thread.ReplyWindow(s.now()); !window.Open {
		return nil, fmt.Errorf("%w: %s", ErrReplyWindowClosed, window.Reason)
	}

	if route := s.routeFor(ctx, thread, entry); !route.CanReply {
		return nil, fmt.Errorf("%w: %s", ErrNoReplyRoute, route.Reason)
	}

	externalID, sendErr := entry.send(ctx, thread, msg.Body)

	if externalID != "" {
		id := externalID
		msg.ExternalID = &id
	}

	if sendErr != nil {
		msg.Delivery = models.JSONMap{deliveryStatusKey: deliveryStatusFailed, "error": sendErr.Error()}
	} else {
		msg.Delivery = models.JSONMap{deliveryStatusKey: deliveryStatusSent}
	}

	occurredAt := s.now()
	msg.UpdatedAt = occurredAt

	if _, updErr := s.bun().NewUpdate().Model(msg).
		Column("delivery", "external_id", "updated_at").
		WherePK().
		Exec(ctx); updErr != nil {
		// The send may already have reached the customer. Failing the request
		// here would invite the operator to resend and message the same person
		// twice, which is worse than a row whose delivery record is stale — so
		// this is logged loudly and the send's own outcome is what is reported.
		s.log.WarnContext(ctx, "failed to persist the delivery record after a support resend",
			"threadUid", thread.UID, "messageUid", msg.UID, "error", updErr)
	}

	if sendErr != nil {
		return msg, fmt.Errorf("resending reply on %s: %w", thread.Channel, sendErr)
	}

	if err := s.touchThreadOutbound(ctx, thread, occurredAt); err != nil {
		s.log.WarnContext(ctx, "failed to update support thread after resend",
			"threadUid", thread.UID, "error", err)
	}

	return msg, nil
}

// findMessage loads one message, scoped to its thread so a uid from another
// conversation cannot be resent through this one.
func (s *Service) findMessage(
	ctx context.Context, threadUID, messageUID string,
) (*models.SupportMessage, error) {
	msg := new(models.SupportMessage)

	err := s.bun().NewSelect().Model(msg).
		Where("uid = ?", messageUID).
		Where("thread_uid = ?", threadUID).
		Limit(1).
		Scan(ctx)
	if err != nil {
		if errors.Is(err, errNoRows) {
			return nil, ErrMessageNotFound
		}

		return nil, err
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
		if errors.Is(err, errNoRows) {
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
		if errors.Is(err, errNoRows) {
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
	ctx context.Context, thread *models.SupportThread, occurredAt time.Time,
) error {
	thread.LastMessageAt = occurredAt
	thread.LastInboundAt = &occurredAt
	thread.UnreadCount++
	thread.UpdatedAt = occurredAt

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
	ctx context.Context, thread *models.SupportThread, occurredAt time.Time,
) error {
	thread.LastMessageAt = occurredAt
	thread.UpdatedAt = occurredAt
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
		if errors.Is(err, errNoRows) {
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
			return nil, fmt.Errorf("%w: %q", ErrInvalidStatus, *status)
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
	// SQLite only honors it with foreign_keys=ON, and a purge that silently
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

	if _, delErr := s.bun().NewDelete().Model((*models.SupportMessage)(nil)).
		Where("thread_uid IN (?)", bun.List(uids)).
		Exec(ctx); delErr != nil {
		return 0, delErr
	}

	res, err := s.bun().NewDelete().Model((*models.SupportThread)(nil)).
		Where("uid IN (?)", bun.List(uids)).
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

// RecordDelivery updates the delivery status of an OUTBOUND support message
// from a provider callback.
//
// This deliberately reuses the same callbacks that already drive
// incident_notifications rather than inventing a second delivery pipeline: the
// WhatsApp webhook and the Twilio status callback both arrive keyed on the
// provider message id, which is exactly what support_messages.external_id
// stores.
//
// Best-effort and silent when nothing matches — the overwhelming majority of
// those callbacks are about an alert, not a support reply, so "no such message"
// is the normal case and must not be logged as a fault.
func (s *Service) RecordDelivery(ctx context.Context, channel, externalID, status string) {
	if s == nil || externalID == "" || status == "" {
		return
	}

	// Same containment as CaptureSafe, for the same reason and on the SAME
	// webhooks: this runs inside Meta's and Twilio's delivery callbacks, so a
	// panic here would unwind the handler, the provider would see a 5xx, retry,
	// and eventually disable the subscription. A support reply's delivery
	// status is never worth the channel.
	defer func() {
		if rec := recover(); rec != nil {
			prommetrics.SupportCapture.WithLabelValues(channel, outcomeFailed).Inc()
			s.log.WarnContext(ctx, "support delivery update panicked",
				"channel", channel, "panic", fmt.Sprint(rec))
		}
	}()

	delivery := models.JSONMap{deliveryStatusKey: status, "source": "provider"}

	if _, err := s.bun().NewUpdate().Model((*models.SupportMessage)(nil)).
		Set("delivery = ?", delivery).
		Set("updated_at = ?", s.now()).
		Where("channel = ?", channel).
		Where("external_id = ?", externalID).
		Where("direction = ?", models.SupportDirectionOutbound).
		Exec(ctx); err != nil {
		s.log.WarnContext(ctx, "failed to record support reply delivery status",
			"channel", channel, "error", err)
	}
}
