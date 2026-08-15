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
// orgs so a bug can't run up an unbounded provider bill. They are the defaults;
// deployments override them via config (WithRunawayCaps).
//
// They are PER ORG and apply to every send whoever pays for it. The
// instance-wide cap and country allow-list that guard the SERVER's own spend
// live in instance_sms_guard.go and deliberately do not apply to a
// bring-your-own send.
const (
	defaultSMSRunawayPerHour  = 30
	defaultCallRunawayPerHour = 10
	// defaultWhatsAppRunawayPerHour matches the SMS guard: WhatsApp is the
	// cheaper channel but a runaway loop is just as unbounded.
	defaultWhatsAppRunawayPerHour = 30
	// defaultTelegramRunawayPerHour is double the WhatsApp default. Telegram
	// messages are free, so this guard is about sanity (a flapping check, a
	// dispatch loop) rather than cost — hence the looser bound.
	defaultTelegramRunawayPerHour = 60
)

// RunawayKindTelegram is the runaway-bucket key for the Telegram channel. It is
// deliberately NOT a models.UsageCounterKind*: Telegram is free, so it has no
// monthly entitlement and no org_usage_counter row. Metering a free channel
// would be metering for its own sake.
const RunawayKindTelegram = "telegram"

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

// ReserveWhatsApp reserves one outbound WhatsApp template message for the org
// (runaway guard + monthly cap).
func (s *Service) ReserveWhatsApp(ctx context.Context, orgUID string) error {
	return s.reserveUsage(ctx, orgUID, models.UsageCounterKindWhatsApp)
}

// ReserveTelegram reserves one outbound Telegram message for the org. Unlike
// SMS/voice/WhatsApp this is the hourly runaway guard ONLY — there is no monthly
// entitlement and no persistent counter, because the channel costs nothing per
// message. Returns a *QuotaError when the guard denies, nil when reserved.
func (s *Service) ReserveTelegram(_ context.Context, orgUID string) error {
	capacity := s.telegramRunawayPerHour
	if !s.runawayBucketFor(orgUID, RunawayKindTelegram, capacity).allow(s.now()) {
		return &QuotaError{
			LimitName:    RunawayKindTelegram + "_runaway_per_hour",
			Limit:        capacity,
			CurrentUsage: capacity,
		}
	}

	return nil
}

func (s *Service) reserveUsage(ctx context.Context, orgUID, kind string) error {
	// 1. Runaway guard first (in-memory, no persistence) so a denial here never
	//    increments the durable monthly counter.
	capacity := s.runawayCapFor(kind)
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
		return nil // unlimited (self-hosted: the operator's own credentials pay)
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

func (s *Service) runawayCapFor(kind string) int {
	switch kind {
	case models.UsageCounterKindVoice:
		return s.callRunawayPerHour
	case models.UsageCounterKindWhatsApp:
		return s.whatsAppRunawayPerHour
	default:
		return s.smsRunawayPerHour
	}
}

func monthlyLimitFor(limits Limits, kind string) *int {
	switch kind {
	case models.UsageCounterKindVoice:
		return limits.MaxCallsPerMonth
	case models.UsageCounterKindWhatsApp:
		return limits.MaxWhatsappPerMonth
	default:
		return limits.MaxSmsPerMonth
	}
}

func monthlyLimitName(kind string) string {
	switch kind {
	case models.UsageCounterKindVoice:
		return "MaxCallsPerMonth"
	case models.UsageCounterKindWhatsApp:
		return "MaxWhatsappPerMonth"
	default:
		return "MaxSmsPerMonth"
	}
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
	bucket, ok := s.runawayBuckets[key]
	if !ok {
		bucket = newHourlyBucket(capacity, s.now())
		s.runawayBuckets[key] = bucket
	}

	return bucket
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
