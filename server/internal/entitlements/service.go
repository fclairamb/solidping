package entitlements

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
)

// Errors returned by the entitlements service.
var (
	// ErrEntitlementExceeded is returned when a request would breach a
	// numeric quota (MaxUsers / MaxChecksPerMinute).
	ErrEntitlementExceeded = errors.New("entitlement exceeded")
)

// QuotaError carries the precise numbers a frontend needs to render a
// "you're at 30/30 users" message. Returned from CheckMembership
// and ReserveCheckExecution.
type QuotaError struct {
	LimitName    string
	Limit        int
	CurrentUsage int
}

// Error implements error.
func (e *QuotaError) Error() string {
	return fmt.Sprintf(
		"entitlement exceeded: %s limit is %d, current usage is %d",
		e.LimitName, e.Limit, e.CurrentUsage,
	)
}

// Unwrap allows errors.Is(err, ErrEntitlementExceeded) to match.
func (e *QuotaError) Unwrap() error { return ErrEntitlementExceeded }

// Service exposes Resolve / Set, plus the two enforcement methods
// CheckMembership and ReserveCheckExecution. The token bucket used
// by ReserveCheckExecution is process-local — multi-replica
// deployments need a shared store (Redis / PG advisory) which is a
// follow-up.
type Service struct {
	db         db.Service
	defaults   Entitlements
	staleAfter time.Duration
	now        func() time.Time

	// Per-org rate limiters for ReserveCheckExecution. Lazily built on
	// first use; cleared on Set so admin overrides take effect
	// immediately. Keyed by org UID.
	limitersMu sync.Mutex
	limiters   map[string]*tokenBucket

	// Per-org hourly runaway buckets for SMS/voice, independent of the monthly
	// entitlement cap. Keyed by "orgUID:kind".
	runawayMu      sync.Mutex
	runawayBuckets map[string]*hourlyBucket
	// Runaway caps (per org per hour), config-overridable via WithRunawayCaps.
	smsRunawayPerHour  int
	callRunawayPerHour int
	// whatsAppRunawayPerHour bounds a broken escalation loop on the WhatsApp
	// channel, independently of the billing-driven monthly quota.
	whatsAppRunawayPerHour int
	// telegramRunawayPerHour bounds a broken escalation loop on the Telegram
	// channel. Telegram has NO monthly quota (the channel is free), so this is
	// the only bound that applies to it.
	telegramRunawayPerHour int

	// instanceSMS holds the INSTANCE-SPEND guards: the instance-wide hourly cap
	// and the destination-country allow-list. They are scoped to sends made on
	// the server's own credentials, so they live apart from the per-org buckets
	// above — a bring-your-own send bills the customer and must neither consume
	// the shared cap nor be gated by the allow-list.
	instanceSMS instanceSMSGuards
}

// Option customizes a Service at construction.
type Option func(*Service)

// WithRunawayCaps overrides the per-org hourly SMS/voice runaway caps. A
// non-positive value leaves the default in place.
func WithRunawayCaps(smsPerHour, callsPerHour int) Option {
	return func(s *Service) {
		if smsPerHour > 0 {
			s.smsRunawayPerHour = smsPerHour
		}
		if callsPerHour > 0 {
			s.callRunawayPerHour = callsPerHour
		}
	}
}

// WithWhatsAppRunawayCap overrides the per-org hourly WhatsApp runaway cap. A
// non-positive value leaves the default in place.
func WithWhatsAppRunawayCap(perHour int) Option {
	return func(s *Service) {
		if perHour > 0 {
			s.whatsAppRunawayPerHour = perHour
		}
	}
}

// WithTelegramRunawayCap overrides the per-org hourly Telegram runaway cap. A
// non-positive value leaves the default in place.
func WithTelegramRunawayCap(perHour int) Option {
	return func(s *Service) {
		if perHour > 0 {
			s.telegramRunawayPerHour = perHour
		}
	}
}

// NewService builds an entitlements service with the given defaults.
// staleAfter of zero disables stale fallback (self-hosted default).
//
// defaults is held by value for the service's lifetime; passing by
// pointer would invite mutation.
//
//nolint:gocritic // defaults held by value intentionally; passing by pointer would invite mutation
func NewService(
	dbService db.Service, defaults Entitlements, staleAfter time.Duration, opts ...Option,
) *Service {
	svc := &Service{
		db:                     dbService,
		defaults:               defaults,
		staleAfter:             staleAfter,
		now:                    time.Now,
		limiters:               make(map[string]*tokenBucket),
		runawayBuckets:         make(map[string]*hourlyBucket),
		smsRunawayPerHour:      defaultSMSRunawayPerHour,
		callRunawayPerHour:     defaultCallRunawayPerHour,
		whatsAppRunawayPerHour: defaultWhatsAppRunawayPerHour,
		telegramRunawayPerHour: defaultTelegramRunawayPerHour,
	}

	for _, opt := range opts {
		opt(svc)
	}

	return svc
}

// Resolve merges the stored row with defaults. Falls back to defaults
// entirely when the row is stale (last_synced_at older than staleAfter
// and source == billing-service); admin overrides do not go stale.
func (s *Service) Resolve(ctx context.Context, orgUID string) (Resolved, error) {
	row, err := s.db.GetOrgEntitlements(ctx, orgUID)
	if err != nil {
		return Resolved{}, fmt.Errorf("get org entitlements: %w", err)
	}

	stale := s.isStale(row)
	merged := s.merge(row, stale)
	merged.Stale = stale

	return merged, nil
}

// WhiteLabelAllowed reports whether the org may drop the "powered by
// SolidPing" badge from its status pages (spec 2026-08-21-07).
//
// It FAILS CLOSED: a resolve error returns false rather than an error, because
// every caller is on a status-page render path where the honest fallback is
// "show the badge". Losing the badge for a moment because the entitlements
// table hiccuped would be a silent revenue leak; showing it for a moment is a
// cosmetic annoyance.
func (s *Service) WhiteLabelAllowed(ctx context.Context, orgUID string) bool {
	if s == nil {
		return false
	}

	resolved, err := s.Resolve(ctx, orgUID)
	if err != nil {
		slog.WarnContext(ctx, "white-label entitlement lookup failed; keeping the branding",
			"error", err, "orgUID", orgUID)

		return false
	}

	return resolved.Limits.WhiteLabel != nil && *resolved.Limits.WhiteLabel
}

// Set replaces the entitlement row and writes an audit entry in the same
// transaction. Pass empty actor for unattended writes.
//
//nolint:gocritic // input is the wire shape, passed by value to match the API contract
func (s *Service) Set(
	ctx context.Context, orgUID string, input Entitlements, actor, reason string,
) error {
	previous, err := s.db.GetOrgEntitlements(ctx, orgUID)
	if err != nil {
		return fmt.Errorf("load previous entitlements: %w", err)
	}

	row := toModel(orgUID, input, previous)

	var beforeJSON models.JSONMap
	if previous != nil {
		beforeJSON, err = modelToJSON(previous)
		if err != nil {
			return fmt.Errorf("snapshot before: %w", err)
		}
	}

	afterJSON, err := modelToJSON(row)
	if err != nil {
		return fmt.Errorf("snapshot after: %w", err)
	}

	var reasonPtr *string
	if reason != "" {
		r := reason
		reasonPtr = &r
	}

	audit := models.NewOrgEntitlementAudit(
		orgUID, string(input.Source), actor, beforeJSON, afterJSON, reasonPtr,
	)

	if err := s.db.UpsertOrgEntitlements(ctx, row, audit); err != nil {
		return fmt.Errorf("upsert entitlements: %w", err)
	}

	// Drop the cached limiter so the new cap (if any) takes effect on
	// the next ReserveCheckExecution call.
	s.limitersMu.Lock()
	delete(s.limiters, orgUID)
	s.limitersMu.Unlock()

	// Propagate the (possibly changed) plan weight to the org's denormalized
	// check_jobs so the cost-aware scheduler sees the new tier without waiting
	// for a per-check reconcile (spec 2026-06-30-09). Best-effort: a failure
	// here does not undo the entitlement write — the periodic reconcile path
	// is the backstop — so it is logged, not returned.
	s.denormalizePlanWeight(ctx, orgUID, input.Source)

	slog.InfoContext(ctx, "entitlements written",
		"orgUID", orgUID, "source", input.Source, "actor", actor)

	return nil
}

// denormalizePlanWeight bulk-updates check_jobs.plan_weight for an org after an
// entitlement change so paid/free tiering is reflected immediately in the
// scheduler. Derived from the just-written source rather than re-resolving, so
// the weight matches the row we just persisted.
func (s *Service) denormalizePlanWeight(ctx context.Context, orgUID string, source models.EntitlementSource) {
	weight := PlanWeightFree
	if source == models.EntitlementSourceBilling || source == models.EntitlementSourceAdmin {
		weight = PlanWeightPaid
	}

	if _, err := s.db.DB().NewUpdate().
		Model((*models.CheckJob)(nil)).
		Set("plan_weight = ?", weight).
		Where("organization_uid = ?", orgUID).
		Where("plan_weight <> ?", weight). // skip the write when nothing changes
		Exec(ctx); err != nil {
		slog.WarnContext(ctx, "failed to denormalize plan weight onto check_jobs; reconcile will backfill",
			"orgUID", orgUID, "weight", weight, "error", err)
	}
}

// CheckMembership returns ErrEntitlementExceeded (wrapped in
// QuotaError) when the org has reached its MaxUsers quota. Counts every
// organization member regardless of how they joined (SSO, invitation,
// email). nil cap = unlimited.
//
// Race window: the count + caller's insert are not atomic, so a tight
// race may slip a 31st user in. Acceptable for a 30-seat self-hosted
// guard; tighten with a transactional lock if it ever matters.
func (s *Service) CheckMembership(ctx context.Context, orgUID string) error {
	resolved, err := s.Resolve(ctx, orgUID)
	if err != nil {
		return fmt.Errorf("resolve entitlements: %w", err)
	}

	if resolved.Limits.MaxUsers == nil {
		return nil
	}

	limit := *resolved.Limits.MaxUsers

	count, err := s.db.CountMembersForOrg(ctx, orgUID)
	if err != nil {
		return fmt.Errorf("count members: %w", err)
	}

	if count >= limit {
		return &QuotaError{
			LimitName:    "MaxUsers",
			Limit:        limit,
			CurrentUsage: count,
		}
	}

	return nil
}

// ReserveCheckExecution consults a per-org token bucket. Each call
// consumes one token; a depleted bucket returns ErrEntitlementExceeded
// (wrapped in QuotaError). Refill rate is `cap / 60` tokens/sec and
// burst is `cap`, so a freshly-resolved org starts full and recovers
// linearly.
//
// nil cap = unlimited (skip lookup, no allocation).
func (s *Service) ReserveCheckExecution(ctx context.Context, orgUID string) error {
	resolved, err := s.Resolve(ctx, orgUID)
	if err != nil {
		return fmt.Errorf("resolve entitlements: %w", err)
	}

	if resolved.Limits.MaxChecksPerMinute == nil {
		return nil
	}

	limit := *resolved.Limits.MaxChecksPerMinute
	if limit <= 0 {
		// 0 means "no executions allowed at all"; surface a quota error
		// so the caller can defer the job. Negative values are clamped
		// to the same behavior.
		return &QuotaError{LimitName: "MaxChecksPerMinute", Limit: limit, CurrentUsage: 0}
	}

	bucket := s.limiterFor(orgUID, limit)
	if !bucket.allow(s.now()) {
		return &QuotaError{
			LimitName:    "MaxChecksPerMinute",
			Limit:        limit,
			CurrentUsage: limit,
		}
	}

	return nil
}

// Plan-weight tiers used by the cost-aware scheduler (spec 2026-06-30-09).
// Higher weight = more protected (reserved capacity + deadline credit under
// contention). Kept deliberately coarse: the OSS knows nothing about SKUs, so
// "paid" is simply "provisioned beyond the in-code defaults".
const (
	// PlanWeightFree is the default tier for orgs on in-code defaults.
	PlanWeightFree = 0
	// PlanWeightPaid is granted to orgs whose entitlements were explicitly
	// written by the billing service or an admin (i.e. a real plan).
	PlanWeightPaid = 1
)

// PlanWeight resolves an org's scheduling plan weight from its entitlements.
// An org whose resolved entitlement source is billing-service or admin counts
// as paid (provisioned beyond defaults); everything else is free. Resolution
// failures fall back to free so a transient DB hiccup never accidentally
// promotes an org. nil receiver returns free (entitlements disabled).
func (s *Service) PlanWeight(ctx context.Context, orgUID string) int {
	if s == nil {
		return PlanWeightFree
	}

	resolved, err := s.Resolve(ctx, orgUID)
	if err != nil {
		slog.WarnContext(ctx, "resolve entitlements for plan weight failed; defaulting to free",
			"orgUID", orgUID, "error", err)

		return PlanWeightFree
	}

	switch resolved.Source {
	case models.EntitlementSourceBilling, models.EntitlementSourceAdmin:
		return PlanWeightPaid
	case models.EntitlementSourceDefault, models.EntitlementSourceSelfHosted:
		return PlanWeightFree
	default:
		return PlanWeightFree
	}
}

// limiterFor returns (creating if needed) the token bucket for orgUID.
// Capacity is taken at first construction; later changes are honored by
// Set, which clears the cache.
func (s *Service) limiterFor(orgUID string, capacity int) *tokenBucket {
	s.limitersMu.Lock()
	defer s.limitersMu.Unlock()

	bucket, ok := s.limiters[orgUID]
	if !ok {
		bucket = newTokenBucket(capacity, s.now())
		s.limiters[orgUID] = bucket
	}

	return bucket
}

// Defaults returns the in-memory defaults for callers that need to
// render "X / Y" indicators when no row exists.
func (s *Service) Defaults() Entitlements {
	return s.defaults
}

// merge produces a Resolved by filling nulls from defaults. If stale is
// true, the entire payload is dropped in favor of defaults.
func (s *Service) merge(row *models.OrgEntitlements, stale bool) Resolved {
	out := Resolved{
		Limits:       s.defaults.Limits,
		Source:       models.EntitlementSourceDefault,
		DisplayName:  s.defaults.DisplayName,
		DisplayEmoji: s.defaults.DisplayEmoji,
	}

	if row == nil || stale {
		return out
	}

	out.Source = row.Payload.Source
	out.ExpiresAt = row.ExpiresAt
	out.LastSyncedAt = row.LastSyncedAt

	// Display identity: a row with its own name/emoji (e.g. billing-written)
	// keeps it; a row that never got one (e.g. an admin override that only
	// touched limits) inherits the default — same null-fill semantics as
	// the limits below.
	if row.Payload.DisplayName != nil {
		out.DisplayName = row.Payload.DisplayName
	}
	if row.Payload.DisplayEmoji != nil {
		out.DisplayEmoji = row.Payload.DisplayEmoji
	}

	limits := row.Payload.Limits
	if limits.MaxChecks != nil {
		out.Limits.MaxChecks = limits.MaxChecks
	}
	if limits.MaxUsers != nil {
		out.Limits.MaxUsers = limits.MaxUsers
	}
	if limits.MaxChecksPerMinute != nil {
		out.Limits.MaxChecksPerMinute = limits.MaxChecksPerMinute
	}
	if limits.MaxDeportedAgents != nil {
		out.Limits.MaxDeportedAgents = limits.MaxDeportedAgents
	}
	if limits.MaxCustomDomains != nil {
		out.Limits.MaxCustomDomains = limits.MaxCustomDomains
	}
	if limits.MaxSmsPerMonth != nil {
		out.Limits.MaxSmsPerMonth = limits.MaxSmsPerMonth
	}
	if limits.MaxCallsPerMonth != nil {
		out.Limits.MaxCallsPerMonth = limits.MaxCallsPerMonth
	}
	if limits.MaxWhatsappPerMonth != nil {
		out.Limits.MaxWhatsappPerMonth = limits.MaxWhatsappPerMonth
	}
	if limits.MaxSlos != nil {
		out.Limits.MaxSlos = limits.MaxSlos
	}
	if limits.WhiteLabel != nil {
		out.Limits.WhiteLabel = limits.WhiteLabel
	}

	return out
}

// isStale returns true if the row is past its sync window. Only billing-
// service rows can go stale; admin overrides are deliberate and persist.
func (s *Service) isStale(row *models.OrgEntitlements) bool {
	if row == nil {
		return false
	}
	if s.staleAfter <= 0 {
		return false
	}
	if row.Payload.Source != models.EntitlementSourceBilling {
		return false
	}
	if row.LastSyncedAt == nil {
		return false
	}

	return s.now().Sub(*row.LastSyncedAt) > s.staleAfter
}

// toModel maps an Entitlements (input) onto an OrgEntitlements row. If
// previous is non-nil the existing UID is preserved; otherwise a fresh
// one is generated.
//
//nolint:gocritic // input is the wire shape, passed by value to match the API contract
func toModel(
	orgUID string, input Entitlements, previous *models.OrgEntitlements,
) *models.OrgEntitlements {
	row := models.NewOrgEntitlements(orgUID, input.Source)
	if previous != nil {
		row.UID = previous.UID
		row.CreatedAt = previous.CreatedAt
	}

	row.Payload = models.EntitlementsPayload{
		Version:      models.EntitlementsPayloadVersion,
		Source:       input.Source,
		Limits:       input.Limits,
		DisplayName:  input.DisplayName,
		DisplayEmoji: input.DisplayEmoji,
	}

	row.ExternalRef = input.ExternalRef
	if input.Metadata != nil {
		row.Metadata = models.JSONMap(input.Metadata)
	}
	row.ExpiresAt = input.ExpiresAt
	row.LastSyncedAt = input.LastSyncedAt

	return row
}

// modelToJSON serializes an OrgEntitlements into a JSON snapshot for the
// audit log.
//
//nolint:musttag // OrgEntitlements is bun-tagged; default JSON encoding is acceptable for the audit blob
func modelToJSON(row *models.OrgEntitlements) (models.JSONMap, error) {
	raw, err := json.Marshal(row)
	if err != nil {
		return nil, err
	}

	var out models.JSONMap
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}

	return out, nil
}

// tokenBucket is a minimal per-org token bucket. Tokens refill at
// rate (capacity / 60) tokens per second; capacity is the burst.
type tokenBucket struct {
	mu       sync.Mutex
	capacity int
	tokens   float64
	last     time.Time
}

func newTokenBucket(capacity int, now time.Time) *tokenBucket {
	return &tokenBucket{
		capacity: capacity,
		tokens:   float64(capacity),
		last:     now,
	}
}

// allow consumes one token if available. Returns false when the bucket
// is empty.
func (b *tokenBucket) allow(now time.Time) bool {
	b.mu.Lock()
	defer b.mu.Unlock()

	rate := float64(b.capacity) / 60.0
	elapsed := now.Sub(b.last).Seconds()
	if elapsed > 0 {
		b.tokens += elapsed * rate
		if b.tokens > float64(b.capacity) {
			b.tokens = float64(b.capacity)
		}
		b.last = now
	}

	if b.tokens >= 1 {
		b.tokens--

		return true
	}

	return false
}
