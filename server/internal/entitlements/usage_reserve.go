package entitlements

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

// Runaway-guard hourly caps, independent of the billing-driven monthly quota.
// These bound a broken escalation loop even for self-hosted (unlimited-quota)
// orgs so a bug can't run up an unbounded Twilio bill.
const (
	defaultSMSRunawayPerHour  = 30
	defaultCallRunawayPerHour = 10
)

// ReserveSMS reserves one SMS send for the org: first the per-org hourly
// runaway guard, then the monthly entitlement cap (persistent counter). Returns
// a *QuotaError when either denies, nil when reserved.
func (s *Service) ReserveSMS(ctx context.Context, orgUID string) error {
	return s.reserveUsage(ctx, orgUID, models.UsageCounterKindSMS)
}

// ReserveCall reserves one voice call for the org (runaway guard + monthly cap).
func (s *Service) ReserveCall(ctx context.Context, orgUID string) error {
	return s.reserveUsage(ctx, orgUID, models.UsageCounterKindVoice)
}

func (s *Service) reserveUsage(ctx context.Context, orgUID, kind string) error {
	// 1. Runaway guard first (in-memory, no persistence) so a denial here never
	//    increments the durable monthly counter.
	capacity := runawayCapFor(kind)
	if !s.runawayBucketFor(orgUID, kind, capacity).allow(s.now()) {
		return &QuotaError{LimitName: kind + "_runaway_per_hour", Limit: capacity, CurrentUsage: capacity}
	}

	// 2. Monthly entitlement cap.
	resolved, err := s.Resolve(ctx, orgUID)
	if err != nil {
		return fmt.Errorf("resolve entitlements: %w", err)
	}

	limit := monthlyLimitFor(resolved.Limits, kind)
	if limit == nil {
		return nil // unlimited (self-hosted, bring-your-own Twilio)
	}

	if *limit <= 0 {
		return &QuotaError{LimitName: monthlyLimitName(kind), Limit: *limit, CurrentUsage: 0}
	}

	reserved, err := s.db.ReserveMonthlyUsage(ctx, orgUID, kind, monthStart(s.now()), *limit)
	if err != nil {
		return fmt.Errorf("reserve monthly usage: %w", err)
	}

	if !reserved {
		return &QuotaError{LimitName: monthlyLimitName(kind), Limit: *limit, CurrentUsage: *limit}
	}

	return nil
}

func runawayCapFor(kind string) int {
	if kind == models.UsageCounterKindVoice {
		return defaultCallRunawayPerHour
	}

	return defaultSMSRunawayPerHour
}

func monthlyLimitFor(limits Limits, kind string) *int {
	if kind == models.UsageCounterKindVoice {
		return limits.MaxCallsPerMonth
	}

	return limits.MaxSmsPerMonth
}

func monthlyLimitName(kind string) string {
	if kind == models.UsageCounterKindVoice {
		return "MaxCallsPerMonth"
	}

	return "MaxSmsPerMonth"
}

// monthStart returns the first day of the UTC month of t as an ISO date string.
func monthStart(t time.Time) string {
	u := t.UTC()

	return time.Date(u.Year(), u.Month(), 1, 0, 0, 0, 0, time.UTC).Format("2006-01-02")
}

func (s *Service) runawayBucketFor(orgUID, kind string, capacity int) *hourlyBucket {
	s.runawayMu.Lock()
	defer s.runawayMu.Unlock()

	key := orgUID + ":" + kind
	b, ok := s.runawayBuckets[key]
	if !ok {
		b = newHourlyBucket(capacity, s.now())
		s.runawayBuckets[key] = b
	}

	return b
}

// hourlyBucket is a per-org token bucket that refills its full capacity over one
// hour (capacity/3600 tokens per second). Mirrors the tokenBucket shape used by
// ReserveCheckExecution but with an hourly window.
type hourlyBucket struct {
	mu       sync.Mutex
	capacity int
	tokens   float64
	last     time.Time
}

func newHourlyBucket(capacity int, now time.Time) *hourlyBucket {
	return &hourlyBucket{capacity: capacity, tokens: float64(capacity), last: now}
}

// allow consumes one token if available.
func (b *hourlyBucket) allow(now time.Time) bool {
	b.mu.Lock()
	defer b.mu.Unlock()

	rate := float64(b.capacity) / 3600.0
	if elapsed := now.Sub(b.last).Seconds(); elapsed > 0 {
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
