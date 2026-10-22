package entitlements

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db/models"
)

// Runaway-guard hourly caps, independent of the billing-driven monthly quota.
// These bound a broken escalation loop even for self-hosted (unlimited-quota)
// orgs so a bug can't run up an unbounded provider bill. They are the defaults;
// deployments override them via config (WithRunawayCaps).
//
// They are PER ORG and apply to every send whoever pays for it. Everything else
// is scoped to sends made on the SERVER's own credentials: the monthly
// entitlement cap (ReserveSMS / ReserveCall), and the instance-wide cap and
// country allow-list in instance_sms_guard.go. A bring-your-own send bills the
// customer's own provider account, so it goes through
// ReserveSMSOwnCredentials / ReserveCallOwnCredentials, which keep the runaway
// guard only and write no monthly counter.
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

// Exported views of the per-org runaway defaults, so a test can compare them
// with the documented values.
const (
	DefaultWhatsAppRunawayPerHour = defaultWhatsAppRunawayPerHour
	DefaultTelegramRunawayPerHour = defaultTelegramRunawayPerHour
)

// RunawayKindTelegram is the runaway-bucket key for the Telegram channel. It is
// deliberately NOT a models.UsageCounterKind*: Telegram is free, so it has no
// monthly entitlement and no org_usage_counter row. Metering a free channel
// would be metering for its own sake.
const RunawayKindTelegram = "telegram"

// ReserveSMS reserves one SMS send made on the instance's credentials: first
// the per-org hourly runaway guard, then the monthly entitlement cap
// (persistent counter). Returns a *QuotaError when either denies, nil when
// reserved.
func (s *Service) ReserveSMS(ctx context.Context, orgUID string) error {
	return s.reserveUsage(ctx, orgUID, models.UsageCounterKindSMS)
}

// ReserveCall reserves one voice call made on the instance's credentials
// (runaway guard + monthly cap).
func (s *Service) ReserveCall(ctx context.Context, orgUID string) error {
	return s.reserveUsage(ctx, orgUID, models.UsageCounterKindVoice)
}

// ReserveSMSOwnCredentials reserves one SMS sent through the org's own
// (bring-your-own) provider account. The org pays for it, so the monthly
// entitlement cap does not apply and no counter row is written: only the
// per-org hourly runaway guard, which bounds a broken escalation loop whoever
// pays. It shares the bucket of ReserveSMS, so mixing modes cannot double the
// hourly allowance.
func (s *Service) ReserveSMSOwnCredentials(_ context.Context, orgUID string) error {
	return s.reserveRunaway(orgUID, models.UsageCounterKindSMS)
}

// ReserveCallOwnCredentials is ReserveSMSOwnCredentials for a voice call.
func (s *Service) ReserveCallOwnCredentials(_ context.Context, orgUID string) error {
	return s.reserveRunaway(orgUID, models.UsageCounterKindVoice)
}

// IsMonthlyCapRefusal reports whether err is a refusal by a monthly
// entitlement cap (MaxSmsPerMonth, MaxCallsPerMonth, MaxWhatsappPerMonth), as
// opposed to the hourly runaway guard. Only the former means the org ran out
// of its plan's allowance.
func IsMonthlyCapRefusal(err error) bool {
	var quotaErr *QuotaError
	if !errors.As(err, &quotaErr) {
		return false
	}

	switch quotaErr.LimitName {
	case monthlyLimitName(models.UsageCounterKindSMS),
		monthlyLimitName(models.UsageCounterKindVoice),
		monthlyLimitName(models.UsageCounterKindWhatsApp):
		return true
	default:
		return false
	}
}

// ClaimMonthlyCapNotice returns true exactly once per org, per kind, per UTC
// month: the first caller that sees the monthly cap refuse a send gets to tell
// the org's admins. The claim is the atomic upsert of org_usage_counters with a
// limit of one, so two concurrent refusals (or two replicas) cannot both win.
func (s *Service) ClaimMonthlyCapNotice(ctx context.Context, orgUID, kind string) (bool, error) {
	claimed, err := s.db.ReserveMonthlyUsage(ctx, orgUID, capNoticeKind(kind), monthStart(s.now()), 1)
	if err != nil {
		return false, fmt.Errorf("claim monthly cap notice: %w", err)
	}

	return claimed, nil
}

// capNoticeKind is the org_usage_counters kind that records "the admins were
// told this month that <kind> ran out".
func capNoticeKind(kind string) string { return kind + "_cap_notice" }

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

// reserveRunaway applies the per-org hourly runaway guard alone.
func (s *Service) reserveRunaway(orgUID, kind string) error {
	capacity := s.runawayCapFor(kind)
	if !s.runawayBucketFor(orgUID, kind, capacity).allow(s.now()) {
		return &QuotaError{LimitName: kind + "_runaway_per_hour", Limit: capacity, CurrentUsage: capacity}
	}

	return nil
}

func (s *Service) reserveUsage(ctx context.Context, orgUID, kind string) error {
	// 1. Runaway guard first (in-memory, no persistence) so a denial here never
	//    increments the durable monthly counter.
	if err := s.reserveRunaway(orgUID, kind); err != nil {
		return err
	}

	// 2. Monthly entitlement cap.
	resolved, err := s.Resolve(ctx, orgUID)
	if err != nil {
		return fmt.Errorf("resolve entitlements: %w", err)
	}

	limit := monthlyLimitFor(&resolved.Limits, kind)
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

func monthlyLimitFor(limits *Limits, kind string) *int {
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

// ParamUpgradeURLTemplate is the system parameter holding the billing
// service's upgrade URL, with `{org}` standing for the org slug.
const ParamUpgradeURLTemplate = "entitlements.upgrade_url_template"

// UpgradeURL returns the URL an email may carry to reach the upgrade flow: the
// platform's own `<baseURL>/d/orgs/<slug>/upgrade` page, never the billing
// service. That page forwards a logged-in admin to the billing URL with a
// signed token minted at click time, a token that must never sit in a mailbox.
// It returns "" when the deployment configures no upgrade template
// (self-hosted) or when baseURL is empty.
func (s *Service) UpgradeURL(ctx context.Context, baseURL, orgSlug string) string {
	if baseURL == "" {
		return ""
	}

	param, err := s.db.GetSystemParameter(ctx, ParamUpgradeURLTemplate)
	if err != nil || param == nil {
		return ""
	}

	if template, ok := param.Value["value"].(string); !ok || template == "" {
		return ""
	}

	return fmt.Sprintf("%s%s/orgs/%s/upgrade", baseURL, config.DashboardBasePath, orgSlug)
}
