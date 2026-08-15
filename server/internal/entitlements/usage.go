package entitlements

import (
	"context"
	"fmt"
	"time"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

// Usage is the org's current resource consumption, computed on demand
// (only when the API caller asks for it via ?with=usage).
//
// ChecksPerMinute is sum((60s/period) × max(1, len(regions))) over enabled,
// non-internal checks — a multi-region check executes once per region per
// period, so its per-minute cost scales with the region count (spec
// 2026-07-20-05). It excludes internal checks (discovery hosts, heartbeats)
// and therefore may be lower than the effective worker-dispatch rate enforced
// by ReserveCheckExecution, whose token bucket counts every execution
// including internal ones. Checks counts all non-internal, non-deleted
// checks regardless of enabled state.
type Usage struct {
	Checks          int     `json:"checks"`
	ChecksPerMinute float64 `json:"checksPerMinute"`
	// SSOUsers is the org's total member count (all members, regardless
	// of how they joined). The wire key stays `ssoUsers` for backward
	// compatibility; it is enforced against the MaxUsers cap.
	SSOUsers int `json:"ssoUsers"`
	// Agents is the org's count of active (non-revoked, non-deleted) deported
	// agents across all private regions. Enforced against MaxDeportedAgents.
	Agents int `json:"agents"`
	// CustomDomains is the org's count of live status pages with a custom
	// domain set. Enforced against MaxCustomDomains.
	CustomDomains int `json:"customDomains"`
	// WhatsappThisMonth is the org's outbound WhatsApp template messages in
	// the current UTC calendar month. Enforced against MaxWhatsappPerMonth.
	// Unlike the other figures this is a persistent counter, not a live count
	// — sent messages cannot be un-sent.
	WhatsappThisMonth int `json:"whatsappThisMonth"`
	// SMSGuard reports the instance-spend guards (instance-wide hourly cap,
	// destination-country allow-list) and how many of this org's sends they
	// have refused since process start. Nil when the deployment configures no
	// instance-spend guard at all. A breach must never fail silently, so it
	// surfaces here as well as in the logs.
	SMSGuard *SMSGuardStatus `json:"smsGuard,omitempty"`
}

// Usage computes the org's current resource consumption. Non-internal
// checks only: system-created checks do not consume the user's quota.
func (s *Service) Usage(ctx context.Context, orgUID string) (Usage, error) {
	rates, err := s.db.ListOrgCheckRates(ctx, orgUID)
	if err != nil {
		return Usage{}, fmt.Errorf("list check rates: %w", err)
	}

	var perMin float64
	for i := range rates {
		rate := &rates[i]
		if rate.Enabled && time.Duration(rate.Period) > 0 {
			// Each selected region runs the check every period, so the
			// per-minute cost is the single-region rate times the region count
			// (min 1 — a no-region check still runs once). Mirrors the actual
			// worker dispatch rate the ReserveCheckExecution token bucket sees.
			regions := len(rate.Regions)
			if regions < 1 {
				regions = 1
			}
			perMin += float64(time.Minute) / float64(time.Duration(rate.Period)) * float64(regions)
		}
	}

	members, err := s.db.CountMembersForOrg(ctx, orgUID)
	if err != nil {
		return Usage{}, fmt.Errorf("count members: %w", err)
	}

	agentCount, err := s.countActiveAgents(ctx, orgUID)
	if err != nil {
		return Usage{}, fmt.Errorf("count agents: %w", err)
	}

	customDomains, err := s.db.CountStatusPagesWithCustomDomain(ctx, orgUID)
	if err != nil {
		return Usage{}, fmt.Errorf("count custom domains: %w", err)
	}

	// A counter read must never fail the whole usage page: the monthly
	// counter is informational here, and the reservation path is the one that
	// actually enforces the cap.
	whatsapp, err := s.db.GetMonthlyUsage(
		ctx, orgUID, models.UsageCounterKindWhatsApp, monthStart(s.now()),
	)
	if err != nil {
		whatsapp = 0
	}

	return Usage{
		Checks: len(rates), ChecksPerMinute: perMin, SSOUsers: members,
		Agents: agentCount, CustomDomains: customDomains,
		WhatsappThisMonth: whatsapp,
		SMSGuard:          s.SMSGuardStatusFor(orgUID),
	}, nil
}

// countActiveAgents counts the org's active (non-revoked, non-deleted)
// deported agents across all private regions. Shared by Usage and
// AgentCreateAllowed so both agree on what counts against the cap.
func (s *Service) countActiveAgents(ctx context.Context, orgUID string) (int, error) {
	agents, err := s.db.ListAgents(ctx, orgUID)
	if err != nil {
		return 0, fmt.Errorf("list agents: %w", err)
	}

	count := 0

	for _, agent := range agents {
		if agent.Status == models.AgentStatusActive {
			count++
		}
	}

	return count, nil
}

// AgentCreateAllowed returns ErrEntitlementExceeded (wrapped in QuotaError)
// when enrolling another agent would breach the org's MaxDeportedAgents cap.
// nil cap = unlimited. Counts active (non-revoked, non-deleted) agents across
// all private regions — mirrors CheckCreateAllowed.
//
// Called from two places: MintEnrollmentToken (early UX, before an operator
// ever starts a container) and the agentws enrollment path (the correctness
// point — a token minted under the cap could still over-enroll if the cap
// drops or another token is consumed first).
//
// Race window: the count and the caller's insert are not atomic, mirroring
// CheckCreateAllowed and CheckMembership. A tight race may slip one extra
// agent past the cap; acceptable for a soft quota guard.
func (s *Service) AgentCreateAllowed(ctx context.Context, orgUID string) error {
	resolved, err := s.Resolve(ctx, orgUID)
	if err != nil {
		return fmt.Errorf("resolve entitlements: %w", err)
	}

	if resolved.Limits.MaxDeportedAgents == nil {
		return nil
	}

	limit := *resolved.Limits.MaxDeportedAgents

	count, err := s.countActiveAgents(ctx, orgUID)
	if err != nil {
		return fmt.Errorf("count agents: %w", err)
	}

	if count >= limit {
		return &QuotaError{
			LimitName:    "MaxDeportedAgents",
			Limit:        limit,
			CurrentUsage: count,
		}
	}

	return nil
}

// CustomDomainAllowed returns ErrEntitlementExceeded (wrapped in QuotaError)
// when setting another custom domain would breach the org's MaxCustomDomains
// cap. nil cap = unlimited. Counts live status pages in the org that already
// have a custom domain set. Called only when setting a NEW non-null domain on a
// page that did not previously have one.
//
// Race window: the count and the caller's write are not atomic, mirroring
// CheckCreateAllowed. A tight race may slip one extra domain past the cap;
// acceptable for a soft quota guard (the global unique index still arbitrates
// ownership).
func (s *Service) CustomDomainAllowed(ctx context.Context, orgUID string) error {
	resolved, err := s.Resolve(ctx, orgUID)
	if err != nil {
		return fmt.Errorf("resolve entitlements: %w", err)
	}

	if resolved.Limits.MaxCustomDomains == nil {
		return nil
	}

	limit := *resolved.Limits.MaxCustomDomains

	count, err := s.db.CountStatusPagesWithCustomDomain(ctx, orgUID)
	if err != nil {
		return fmt.Errorf("count custom domains: %w", err)
	}

	if count >= limit {
		return &QuotaError{
			LimitName:    "MaxCustomDomains",
			Limit:        limit,
			CurrentUsage: count,
		}
	}

	return nil
}

// CheckCreateAllowed returns ErrEntitlementExceeded (wrapped in QuotaError)
// when creating another check would breach the org's MaxChecks cap. nil cap
// = unlimited. Counts non-internal, non-deleted checks only, so internal /
// system-created checks neither consume nor are gated by the quota — callers
// must skip this guard for internal checks anyway.
//
// Race window: the count and the caller's insert are not atomic, mirroring
// CheckMembership. A tight race may slip one extra check past the cap;
// acceptable for a soft quota guard.
func (s *Service) CheckCreateAllowed(ctx context.Context, orgUID string) error {
	resolved, err := s.Resolve(ctx, orgUID)
	if err != nil {
		return fmt.Errorf("resolve entitlements: %w", err)
	}

	if resolved.Limits.MaxChecks == nil {
		return nil
	}

	limit := *resolved.Limits.MaxChecks

	rates, err := s.db.ListOrgCheckRates(ctx, orgUID)
	if err != nil {
		return fmt.Errorf("count checks: %w", err)
	}

	if len(rates) >= limit {
		return &QuotaError{
			LimitName:    "MaxChecks",
			Limit:        limit,
			CurrentUsage: len(rates),
		}
	}

	return nil
}
