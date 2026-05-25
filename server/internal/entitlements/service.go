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
	// numeric quota (MaxSSOUsers / MaxChecksPerMinute).
	ErrEntitlementExceeded = errors.New("entitlement exceeded")
)

// QuotaError carries the precise numbers a frontend needs to render a
// "you're at 30/30 SSO users" message. Returned from CheckSSOMembership
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
// CheckSSOMembership and ReserveCheckExecution. The token bucket used
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
}

// NewService builds an entitlements service with the given defaults.
// staleAfter of zero disables stale fallback (self-hosted default).
//
// defaults is held by value for the service's lifetime; passing by
// pointer would invite mutation.
func NewService(
	dbService db.Service, defaults Entitlements, staleAfter time.Duration,
) *Service {
	return &Service{
		db:         dbService,
		defaults:   defaults,
		staleAfter: staleAfter,
		now:        time.Now,
		limiters:   make(map[string]*tokenBucket),
	}
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

// Set replaces the entitlement row and writes an audit entry in the same
// transaction. Pass empty actor for unattended writes.
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

	slog.InfoContext(ctx, "entitlements written",
		"orgUID", orgUID, "source", input.Source, "actor", actor)

	return nil
}

// CheckSSOMembership returns ErrEntitlementExceeded (wrapped in
// QuotaError) when the org has reached its MaxSSOUsers quota. Counts
// distinct organization members linked to any user_providers row.
// nil cap = unlimited.
//
// Race window: the count + caller's insert are not atomic, so a tight
// race may slip a 31st user in. Acceptable for a 30-seat self-hosted
// guard; tighten with a transactional lock if it ever matters.
func (s *Service) CheckSSOMembership(ctx context.Context, orgUID string) error {
	resolved, err := s.Resolve(ctx, orgUID)
	if err != nil {
		return fmt.Errorf("resolve entitlements: %w", err)
	}

	if resolved.Limits.MaxSSOUsers == nil {
		return nil
	}

	limit := *resolved.Limits.MaxSSOUsers

	count, err := s.db.CountSSOMembersForOrg(ctx, orgUID)
	if err != nil {
		return fmt.Errorf("count sso members: %w", err)
	}

	if count >= limit {
		return &QuotaError{
			LimitName:    "MaxSSOUsers",
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
		Limits: s.defaults.Limits,
		Source: models.EntitlementSourceDefault,
	}

	if row == nil || stale {
		return out
	}

	out.Source = row.Payload.Source
	out.DisplayName = row.Payload.DisplayName
	out.ExpiresAt = row.ExpiresAt
	out.LastSyncedAt = row.LastSyncedAt

	limits := row.Payload.Limits
	if limits.MaxChecks != nil {
		out.Limits.MaxChecks = limits.MaxChecks
	}
	if limits.MaxSSOUsers != nil {
		out.Limits.MaxSSOUsers = limits.MaxSSOUsers
	}
	if limits.MaxChecksPerMinute != nil {
		out.Limits.MaxChecksPerMinute = limits.MaxChecksPerMinute
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
func toModel(
	orgUID string, input Entitlements, previous *models.OrgEntitlements,
) *models.OrgEntitlements {
	row := models.NewOrgEntitlements(orgUID, input.Source)
	if previous != nil {
		row.UID = previous.UID
		row.CreatedAt = previous.CreatedAt
	}

	row.Payload = models.EntitlementsPayload{
		Version:     models.EntitlementsPayloadVersion,
		Source:      input.Source,
		Limits:      input.Limits,
		DisplayName: input.DisplayName,
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
