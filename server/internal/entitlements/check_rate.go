package entitlements

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/db/models"
)

// ChecksPerMinute answers the two questions an org has when its executions go
// missing: "is what I have scheduled over my cap?" (Demand vs Limit, the
// predictive half) and "did I actually lose executions?" (SkippedToday, the
// factual half). Both are needed — an org that just dropped back under its cap
// still lost executions earlier today, and an org that just went over has not
// lost any yet.
type ChecksPerMinute struct {
	// Demand is the org's scheduled execution rate: the sum over enabled,
	// non-deleted, non-internal, ACTIVE checks of
	// max(1, len(regions)) × 60s/period.
	Demand float64 `json:"demand"`
	// Limit is the resolved MaxChecksPerMinute cap. nil = unlimited, in which
	// case Demand can never be "over".
	Limit *int `json:"limit"`
	// SkippedToday is how many executions the per-org rate gate deferred today
	// (UTC), counted across both claim paths. Zero when nothing was skipped.
	SkippedToday int `json:"skippedToday"`
}

// Over reports whether the org's scheduled demand exceeds its cap. An unlimited
// cap is never over.
func (c ChecksPerMinute) Over() bool {
	return c.Limit != nil && c.Demand > float64(*c.Limit)
}

// ChecksPerMinuteStatus assembles the demand/limit/skips triple for an org.
//
// The skip counter is deliberately non-fatal: it is the factual half of a
// warning banner, and losing it must not take the whole entitlements payload
// (limits, upgrade URL) down with it.
func (s *Service) ChecksPerMinuteStatus(ctx context.Context, orgUID string) (ChecksPerMinute, error) {
	resolved, err := s.Resolve(ctx, orgUID)
	if err != nil {
		return ChecksPerMinute{}, fmt.Errorf("resolve entitlements: %w", err)
	}

	rates, err := s.db.ListOrgCheckRates(ctx, orgUID)
	if err != nil {
		return ChecksPerMinute{}, fmt.Errorf("list check rates: %w", err)
	}

	skipped, err := s.db.GetMonthlyUsage(
		ctx, orgUID, models.UsageCounterKindCheckRateLimited, dayStart(s.now()),
	)
	if err != nil {
		slog.WarnContext(ctx, "rate-limited skip counter read failed; reporting 0 skips",
			"orgUID", orgUID, "error", err)

		skipped = 0
	}

	return ChecksPerMinute{
		Demand:       checksPerMinuteRate(rates, true),
		Limit:        resolved.Limits.MaxChecksPerMinute,
		SkippedToday: skipped,
	}, nil
}

// RecordRateLimitedSkip tallies one execution deferred by the per-org
// MaxChecksPerMinute gate, in the org's daily bucket.
//
// Best-effort by construction: it is called from inside the two claim gates,
// after the decision to defer is already made, so a counter write failure must
// only be logged — never propagated into a path that would then run a check the
// cap says it should not. Write volume is bounded by the cap itself: an org
// cannot skip faster than it schedules.
//
// A nil receiver (entitlements disabled) is a no-op.
func (s *Service) RecordRateLimitedSkip(ctx context.Context, orgUID string) {
	if s == nil {
		return
	}

	if err := s.db.IncrementUsageCounter(
		ctx, orgUID, models.UsageCounterKindCheckRateLimited, dayStart(s.now()),
	); err != nil {
		slog.WarnContext(ctx, "failed to record a rate-limited check skip; the org banner will under-report",
			"orgUID", orgUID, "error", err)
	}
}

// checksPerMinuteRate sums the per-minute execution rate of a check set.
//
// excludePassive selects between the two legitimate readings of "checks per
// minute": the Usage page's configured-rate figure counts every check, while
// the demand measured against MaxChecksPerMinute must skip passive types
// (heartbeat, email) because they return before the token gate and so consume
// no execution budget.
func checksPerMinuteRate(rates []models.CheckRate, excludePassive bool) float64 {
	var perMin float64

	for i := range rates {
		rate := &rates[i]

		if !rate.Enabled || time.Duration(rate.Period) <= 0 {
			continue
		}

		if excludePassive && checkerdef.CheckType(rate.Type).IsPassive() {
			continue
		}

		// Each selected region runs the check every period, so the per-minute
		// cost is the single-region rate times the region count (min 1 — a
		// no-region check still runs once).
		regions := len(rate.Regions)
		if regions < 1 {
			regions = 1
		}

		perMin += float64(time.Minute) / float64(time.Duration(rate.Period)) * float64(regions)
	}

	return perMin
}

// dayStart returns the UTC day of t as an ISO date string. The rate-limited
// skip counter buckets by day, not by month, so the banner clears itself once
// an org spends a full day back under its cap.
func dayStart(t time.Time) string {
	return t.UTC().Format("2006-01-02")
}
