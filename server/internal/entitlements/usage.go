package entitlements

import (
	"context"
	"fmt"
	"time"
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
}

// Usage computes the org's current resource consumption. Non-internal
// checks only: system-created checks do not consume the user's quota.
func (s *Service) Usage(ctx context.Context, orgUID string) (Usage, error) {
	rates, err := s.db.ListOrgCheckRates(ctx, orgUID)
	if err != nil {
		return Usage{}, fmt.Errorf("list check rates: %w", err)
	}

	var perMin float64
	for _, r := range rates {
		if r.Enabled && time.Duration(r.Period) > 0 {
			// Each selected region runs the check every period, so the
			// per-minute cost is the single-region rate times the region count
			// (min 1 — a no-region check still runs once). Mirrors the actual
			// worker dispatch rate the ReserveCheckExecution token bucket sees.
			regions := len(r.Regions)
			if regions < 1 {
				regions = 1
			}
			perMin += float64(time.Minute) / float64(time.Duration(r.Period)) * float64(regions)
		}
	}

	members, err := s.db.CountMembersForOrg(ctx, orgUID)
	if err != nil {
		return Usage{}, fmt.Errorf("count members: %w", err)
	}

	return Usage{Checks: len(rates), ChecksPerMinute: perMin, SSOUsers: members}, nil
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
