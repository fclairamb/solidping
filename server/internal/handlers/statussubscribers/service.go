// Package statussubscribers provides public-facing status-page subscription
// management (double opt-in email subscriptions + Atom feed) kept separate from
// the authenticated status-page CRUD so the PII surface (subscriber emails) is
// isolated.
package statussubscribers

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"net/mail"
	"strings"
	"time"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/crypto/credentials"
	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/egress"
	"github.com/fclairamb/solidping/server/internal/handlers/statuspageassets"
	"github.com/fclairamb/solidping/server/internal/statuspagelock"
)

// nowUTC returns the current time in UTC. Wrapped so tests can reason about it.
func nowUTC() time.Time { return time.Now().UTC() }

// Service errors.
var (
	// ErrOrganizationNotFound is returned when the organization does not exist.
	ErrOrganizationNotFound = errors.New("organization not found")
	// ErrStatusPageNotFound is returned when the status page does not exist or is not public.
	ErrStatusPageNotFound = errors.New("status page not found")
	// ErrInvalidEmail is returned when the email address is empty or malformed.
	ErrInvalidEmail = errors.New("invalid email address")
	// ErrInvalidScope is returned when scope is not 'page' or 'incident'.
	ErrInvalidScope = errors.New("invalid scope")
	// ErrIncidentRequired is returned when scope is 'incident' but no incidentUid is given.
	ErrIncidentRequired = errors.New("incidentUid is required for incident scope")
	// ErrIncidentNotFound is returned when the referenced incident does not exist.
	ErrIncidentNotFound = errors.New("incident not found")
	// ErrInvalidToken is returned when a confirm/unsubscribe token does not match a row.
	ErrInvalidToken = errors.New("invalid or expired token")
	// ErrSubscriberNotFound is returned when an admin subscriber lookup fails.
	ErrSubscriberNotFound = errors.New("subscriber not found")
)

// tokenBytes is the size of the random token before base64url encoding.
// 32 bytes = 256 bits, comfortably above the ≥128-bit requirement.
const tokenBytes = 32

// maxEmailLen caps the accepted email length to avoid storing absurd input.
const maxEmailLen = 320

// Service provides subscription business logic.
type Service struct {
	db db.Service
	// creds encrypts the delivery endpoints of webhook/slack subscriptions at
	// rest. nil is legal (self-hosted with no master key) and falls back to the
	// documented plaintext envelope — see sealEndpoint.
	creds credentials.Service
	// guard is the process's egress policy. A delivery URL is checked against
	// it at save time, so a non-public endpoint is refused with a 400 rather
	// than failing at every delivery. nil means "no policy": every https URL
	// is accepted (tests that do not exercise the policy).
	guard *egress.Guard
	// budget caps the double opt-in mails of the public subscribe endpoint
	// (spec 2026-10-28-02). Defaults to config.DefaultStatusSubscribersConfig.
	budget config.StatusSubscribersConfig
}

// Option configures the subscribers service.
type Option func(*Service)

// WithEgressGuard installs the egress policy webhook/Slack delivery URLs are
// validated against when a subscription is created. It is the same policy
// every other notification sender follows (SP_EGRESS_ALLOW_PRIVATE): an
// enforcing guard refuses loopback, private, CGNAT, link-local and metadata
// targets, including hostnames that resolve to them.
func WithEgressGuard(guard *egress.Guard) Option {
	return func(s *Service) { s.guard = guard }
}

// WithConfirmBudget sets the per-recipient, per-page and per-IP hourly budgets
// of the confirm mail. A budget of 0 is not enforced.
func WithConfirmBudget(budget config.StatusSubscribersConfig) Option {
	return func(s *Service) { s.budget = budget }
}

// confirmBudgetWindow is the sliding window every confirm-mail budget counts on.
const confirmBudgetWindow = time.Hour

// confirmCounterPrefix namespaces the confirm-mail counters in the global
// state entries. The recipient key carries a hash of the address, never the
// address itself (PII).
const confirmCounterPrefix = "status_subscriber_confirm:"

// NewService creates a new status subscribers service.
func NewService(dbService db.Service, creds credentials.Service, opts ...Option) *Service {
	svc := &Service{db: dbService, creds: creds, budget: config.DefaultStatusSubscribersConfig()}
	for _, opt := range opts {
		opt(svc)
	}

	return svc
}

// SubscriberResponse is the public/admin representation of a subscriber.
// Email is included only in authed admin responses (handler decides).
//
// Endpoint is the MASKED delivery URL, never the real one: an
// incoming-webhook URL is a credential, and a list endpoint that echoed it
// would turn every admin read into a credential disclosure.
type SubscriberResponse struct {
	UID         string  `json:"uid"`
	Email       string  `json:"email,omitempty"`
	Channel     string  `json:"channel"`
	Endpoint    *string `json:"endpoint,omitempty"`
	Scope       string  `json:"scope"`
	IncidentUID *string `json:"incidentUid,omitempty"`
	Confirmed   bool    `json:"confirmed"`
	// FailureCount is the CONSECUTIVE delivery failure count; Disabled says the
	// circuit breaker tripped. Both are operator diagnostics for a webhook that
	// stopped working, which is why they are on the admin payload at all.
	FailureCount int    `json:"failureCount,omitempty"`
	Disabled     bool   `json:"disabled,omitempty"`
	CreatedAt    string `json:"createdAt"`
}

// CreateEndpointSubscriberResponse is the create-only shape. It is a SEPARATE
// type from SubscriberResponse, not that type with an extra optional field,
// precisely so the signing secret cannot leak from the list endpoint by
// someone later populating a shared struct: there is no field on the list
// shape that could carry it.
type CreateEndpointSubscriberResponse struct {
	SubscriberResponse

	// SigningSecret is returned EXACTLY ONCE, here, at creation. It is never
	// stored in a readable column and never returned again — a receiver that
	// loses it must have the subscription re-created. That is the standard
	// show-once contract for a shared secret, and it is what makes the HMAC on
	// every delivery actually verifiable: without it the operator would be
	// handed signed requests they could not check.
	SigningSecret string `json:"signingSecret"`
}

// CreateEndpointSubscriberRequest is the body of the AUTHENTICATED
// webhook/slack subscription endpoint. There is deliberately no public
// equivalent: a visitor pasting an incoming-webhook URL has no verification
// story, so the operator is the verification.
type CreateEndpointSubscriberRequest struct {
	Channel string `json:"channel"`
	URL     string `json:"url"`
	// SigningSecret is optional; when omitted one is generated and returned
	// once in the create response (see CreateEndpointSubscriberResponse).
	// Supplying your own is useful when the receiver already has a secret it
	// verifies other SolidPing calls with.
	SigningSecret *string `json:"signingSecret,omitempty"`
	Scope         *string `json:"scope,omitempty"`
	IncidentUID   *string `json:"incidentUid,omitempty"`
}

// SubscribeRequest is the body of a public subscribe call.
type SubscribeRequest struct {
	Email string `json:"email"`
	// Channel is accepted only so a client asking for a non-email channel gets
	// a clear refusal. Public self-serve is email-only.
	Channel     *string `json:"channel,omitempty"`
	Scope       *string `json:"scope,omitempty"`
	IncidentUID *string `json:"incidentUid,omitempty"`
}

// SubscribeResult carries what the caller needs to send the confirm mail.
type SubscribeResult struct {
	Subscriber *models.StatusPageSubscriber
	PageName   string
	PageSlug   string
	OrgSlug    string
	// PageLogoURL / PageHideBranding brand the confirm mail with the STATUS
	// PAGE's identity rather than the organization's — same rule as the
	// update fan-out: the recipient is subscribing to a page.
	PageLogoURL      *string
	PageHideBranding bool
	// AlreadyConfirmed: the address already holds a live, confirmed
	// subscription. Nothing changed and no mail is due.
	AlreadyConfirmed bool
	// Throttled: a confirm-mail budget is spent. Nothing changed and no mail
	// is due. Subscriber is nil when no row existed.
	Throttled bool
}

// NeedsConfirmMail reports whether the caller must send the confirm mail.
// The public endpoint answers the same 202 either way, so it reveals neither
// whether an address is subscribed nor whether a budget is spent.
func (r *SubscribeResult) NeedsConfirmMail() bool {
	return r != nil && r.Subscriber != nil && !r.AlreadyConfirmed && !r.Throttled
}

// generateToken returns a URL-safe, unguessable opaque token.
func generateToken() (string, error) {
	buf := make([]byte, tokenBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}

	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// normalizeEmail lower-cases and trims the address, then validates it.
func normalizeEmail(raw string) (string, error) {
	email := strings.ToLower(strings.TrimSpace(raw))
	if email == "" || len(email) > maxEmailLen {
		return "", ErrInvalidEmail
	}

	if _, err := mail.ParseAddress(email); err != nil {
		return "", ErrInvalidEmail
	}

	return email, nil
}

// Subscribe starts a (re)subscription on behalf of clientIP (trusted-proxy
// resolution; "" skips the per-IP budget). A new, unconfirmed or soft-deleted
// row gets a fresh confirm token and NeedsConfirmMail reports true. A live
// confirmed subscription is left untouched (AlreadyConfirmed), and so is
// everything once a confirm-mail budget is spent (Throttled).
//
//nolint:cyclop // validation of optional scope/incident fields requires several branches
func (s *Service) Subscribe(
	ctx context.Context, orgSlug, statusPageUID, clientIP string, req *SubscribeRequest,
) (*SubscribeResult, error) {
	// Public self-serve is email-only, explicitly. The channel field is
	// accepted so a mistaken client gets a clear error rather than silently
	// creating an email subscription it did not ask for.
	if req.Channel != nil && *req.Channel != "" && *req.Channel != string(models.SubscriberChannelEmail) {
		return nil, ErrPublicChannelNotAllowed
	}

	email, err := normalizeEmail(req.Email)
	if err != nil {
		return nil, err
	}

	scope := models.SubscriberScopePage
	if req.Scope != nil && *req.Scope != "" {
		scope = models.SubscriberScope(*req.Scope)
		if !scope.IsValid() {
			return nil, ErrInvalidScope
		}
	}

	var incidentUID *string

	if scope == models.SubscriberScopeIncident {
		if req.IncidentUID == nil || *req.IncidentUID == "" {
			return nil, ErrIncidentRequired
		}

		incidentUID = req.IncidentUID
	}

	org, err := s.db.GetOrganizationBySlug(ctx, orgSlug)
	if err != nil {
		return nil, ErrOrganizationNotFound
	}

	page, err := s.db.GetStatusPage(ctx, org.UID, statusPageUID)
	if err != nil || page == nil {
		return nil, ErrStatusPageNotFound
	}

	if !statuspagelock.Visible(page) {
		return nil, ErrStatusPageNotFound
	}

	// The subscribe FORM sits behind the unlock — a page shared with a
	// password should not be a mailing-list signup for the whole internet.
	// Already-confirmed subscribers are untouched by this: the fan-out in
	// notifier.go never consults visibility, so locking a page down does not
	// silently stop the notifications people already opted into.
	if !statuspagelock.Allows(ctx, page) {
		return nil, statuspagelock.ErrLocked
	}

	if incidentUID != nil {
		if _, incErr := s.db.GetIncident(ctx, org.UID, *incidentUID); incErr != nil {
			return nil, ErrIncidentNotFound
		}
	}

	result := &SubscribeResult{
		PageName:         page.Name,
		PageSlug:         page.Slug,
		OrgSlug:          org.Slug,
		PageLogoURL:      statuspageassets.PublicURL(page.Settings.LogoFileUID()),
		PageHideBranding: page.Settings.HideBranding(),
	}

	// Match any prior row for this tuple (including soft-deleted) so a returning
	// visitor revives their existing subscription rather than creating a
	// duplicate that would collide with the live unique index once confirmed.
	existing, err := s.db.FindAnySubscriber(ctx, page.UID, email, scope, incidentUID)
	if err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}

		existing = nil
	}

	// A live, confirmed subscription stays exactly as it is: re-subscribing it
	// would null confirmed_at and rotate the unsubscribe token, letting anyone
	// who knows an address silently stop its notifications.
	if existing != nil && existing.DeletedAt == nil && existing.ConfirmedAt != nil {
		result.Subscriber = existing
		result.AlreadyConfirmed = true

		return result, nil
	}

	// Over budget the row is left untouched too: rotating its confirm token
	// without mailing the new one would void the links already sent.
	allowed, err := s.spendConfirmBudget(ctx, page.UID, email, clientIP)
	if err != nil {
		return nil, err
	}

	if !allowed {
		result.Subscriber = existing
		result.Throttled = true

		return result, nil
	}

	confirmToken, err := generateToken()
	if err != nil {
		return nil, err
	}

	unsubToken, err := generateToken()
	if err != nil {
		return nil, err
	}

	sub, err := s.upsertSubscriber(
		ctx, existing, org.UID, page.UID, email, scope, incidentUID, confirmToken, unsubToken)
	if err != nil {
		return nil, err
	}

	result.Subscriber = sub

	return result, nil
}

// spendConfirmBudget counts one confirm mail against the per-client-IP,
// per-recipient and per-page budgets, in that order, and reports whether all
// of them still had room. The first spent budget stops the count, so a
// refused attempt does not eat into the later budgets. clientIP "" skips the
// IP budget.
func (s *Service) spendConfirmBudget(ctx context.Context, pageUID, email, clientIP string) (bool, error) {
	recipientHash := sha256.Sum256([]byte(email))

	budgets := []struct {
		key   string
		limit int
	}{
		{confirmCounterPrefix + "ip:" + clientIP, s.budget.ConfirmPerIPPerHour},
		{confirmCounterPrefix + "rcpt:" + hex.EncodeToString(recipientHash[:]), s.budget.ConfirmPerRecipientPerHour},
		{confirmCounterPrefix + "page:" + pageUID, s.budget.ConfirmPerPagePerHour},
	}

	for i, budget := range budgets {
		if budget.limit <= 0 || (i == 0 && clientIP == "") {
			continue
		}

		limited, err := s.db.BumpGlobalCounter(ctx, budget.key, budget.limit, confirmBudgetWindow)
		if err != nil {
			return false, err
		}

		if limited {
			return false, nil
		}
	}

	return true, nil
}

// upsertSubscriber revives existing (soft-deleted or unconfirmed: refreshed
// tokens, confirmation reset) or, when it is nil, inserts a new unconfirmed row.
func (s *Service) upsertSubscriber(
	ctx context.Context,
	existing *models.StatusPageSubscriber,
	orgUID, pageUID, email string,
	scope models.SubscriberScope,
	incidentUID *string,
	confirmToken, unsubToken string,
) (*models.StatusPageSubscriber, error) {
	if existing != nil {
		// Reuse the row: soft-undelete, refresh tokens, require re-confirmation.
		if reErr := s.db.ResubscribeSubscriber(ctx, existing.UID, confirmToken, unsubToken); reErr != nil {
			return nil, reErr
		}

		sentAt := nowUTC()
		existing.ConfirmToken = confirmToken
		existing.UnsubscribeToken = unsubToken
		existing.ConfirmSentAt = &sentAt
		existing.ConfirmedAt = nil
		existing.DeletedAt = nil

		return existing, nil
	}

	sub := models.NewStatusPageSubscriber(orgUID, pageUID, email, scope)
	sub.IncidentUID = incidentUID
	sub.ConfirmToken = confirmToken
	sub.UnsubscribeToken = unsubToken

	if createErr := s.db.CreateSubscriber(ctx, sub); createErr != nil {
		return nil, createErr
	}

	return sub, nil
}

// ConfirmResult is returned after a successful confirmation.
type ConfirmResult struct {
	OrgSlug  string
	PageSlug string
	PageName string
}

// Confirm activates a subscription via its confirm token (double opt-in). The
// token is consumed so it cannot be replayed.
func (s *Service) Confirm(ctx context.Context, token string) (*ConfirmResult, error) {
	if token == "" {
		return nil, ErrInvalidToken
	}

	sub, err := s.db.GetSubscriberByConfirmToken(ctx, token)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrInvalidToken
		}

		return nil, err
	}

	// An unconfirmed row past its confirm TTL is refused like an unknown
	// token; the state-cleanup job reaps it.
	if sub.ConfirmExpired(nowUTC()) {
		return nil, ErrInvalidToken
	}

	if err := s.db.ConfirmSubscriber(ctx, sub.UID, nowUTC()); err != nil {
		return nil, err
	}

	return s.pageContext(ctx, sub)
}

// Unsubscribe soft-deletes a subscription via its unsubscribe token.
func (s *Service) Unsubscribe(ctx context.Context, token string) (*ConfirmResult, error) {
	if token == "" {
		return nil, ErrInvalidToken
	}

	sub, err := s.db.GetSubscriberByUnsubToken(ctx, token)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrInvalidToken
		}

		return nil, err
	}

	if err := s.db.SoftDeleteSubscriber(ctx, sub.UID); err != nil {
		return nil, err
	}

	return s.pageContext(ctx, sub)
}

// pageContext resolves the org slug + page slug/name for redirect/landing pages.
func (s *Service) pageContext(
	ctx context.Context, sub *models.StatusPageSubscriber,
) (*ConfirmResult, error) {
	result := &ConfirmResult{}

	org, err := s.db.GetOrganization(ctx, sub.OrganizationUID)
	if err == nil && org != nil {
		result.OrgSlug = org.Slug
	}

	page, err := s.db.GetStatusPage(ctx, sub.OrganizationUID, sub.StatusPageUID)
	if err == nil && page != nil {
		result.PageSlug = page.Slug
		result.PageName = page.Name
	}

	return result, nil
}

// ListSubscribers returns the admin view (count + addresses) for a page,
// scoped to the org. Emails are included; the handler marks them redactable.
func (s *Service) ListSubscribers(
	ctx context.Context, orgSlug, statusPageUID string,
) ([]SubscriberResponse, error) {
	org, err := s.db.GetOrganizationBySlug(ctx, orgSlug)
	if err != nil {
		return nil, ErrOrganizationNotFound
	}

	page, err := s.db.GetStatusPage(ctx, org.UID, statusPageUID)
	if err != nil || page == nil {
		return nil, ErrStatusPageNotFound
	}

	subs, err := s.db.ListSubscribers(ctx, page.UID)
	if err != nil {
		return nil, err
	}

	out := make([]SubscriberResponse, len(subs))
	for i, sub := range subs {
		out[i] = SubscriberResponse{
			UID:          sub.UID,
			Email:        sub.EmailAddress(),
			Channel:      string(sub.Channel),
			Endpoint:     sub.EndpointHint,
			Scope:        string(sub.Scope),
			IncidentUID:  sub.IncidentUID,
			Confirmed:    sub.ConfirmedAt != nil,
			FailureCount: sub.FailureCount,
			Disabled:     sub.DisabledAt != nil,
			CreatedAt:    sub.CreatedAt.UTC().Format("2006-01-02T15:04:05Z07:00"),
		}
	}

	return out, nil
}

// RemoveSubscriber soft-deletes a subscriber as an admin action, scoped to the
// org + page.
func (s *Service) RemoveSubscriber(ctx context.Context, orgSlug, statusPageUID, uid string) error {
	org, err := s.db.GetOrganizationBySlug(ctx, orgSlug)
	if err != nil {
		return ErrOrganizationNotFound
	}

	page, err := s.db.GetStatusPage(ctx, org.UID, statusPageUID)
	if err != nil || page == nil {
		return ErrStatusPageNotFound
	}

	sub, err := s.db.GetSubscriber(ctx, page.UID, uid)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrSubscriberNotFound
		}

		return err
	}

	return s.db.SoftDeleteSubscriber(ctx, sub.UID)
}
