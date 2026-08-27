package entitlements

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/utils/timeutils"
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

// CheckRateProposal is a not-yet-saved check shape, as the check form has it
// while the user is still typing. It is what ProjectChecksPerMinute
// substitutes into the org's live demand so the form can warn BEFORE the save
// rather than after the first skipped execution.
type CheckRateProposal struct {
	// ExcludeCheckUID is the check being edited. Its stored row is dropped
	// from the sum before the proposal is added, so an edit projects a
	// replacement rather than a duplicate. Empty for a create.
	ExcludeCheckUID string
	// Type decides whether the proposal costs anything at all: passive types
	// return before the token gate and so never draw execution budget.
	Type string
	// Period is the proposed execution interval. Zero or negative means "no
	// usable period proposed" and the proposal contributes nothing.
	Period time.Duration
	// Regions is the proposed region set; each region runs the check every
	// period, so the cost scales with max(1, len(Regions)).
	Regions []string
	// Enabled is the proposed enabled state. A disabled check is scheduled
	// nowhere and costs nothing.
	Enabled bool
}

// ProjectedRate is the answer to "what would my demand be if I saved this?".
// It deliberately carries no skip counter: a projection is about the future,
// and today's skips belong to ChecksPerMinuteStatus.
type ProjectedRate struct {
	// Demand is the org's per-minute execution rate WITH the proposal applied.
	Demand float64
	// Current is the org's demand as it stands today, proposal excluded. Kept
	// so a caller can tell an edit that already was over the cap from one that
	// pushes it over.
	Current float64
	// Limit is the resolved MaxChecksPerMinute cap. nil = unlimited.
	Limit *int
}

// Over reports whether the PROJECTED demand exceeds the cap. Unlimited is
// never over.
func (p ProjectedRate) Over() bool {
	return p.Limit != nil && p.Demand > float64(*p.Limit)
}

// ProjectChecksPerMinute recomputes the org's checks-per-minute demand with
// one check replaced (or added) by the caller's proposal.
//
// Same formula, same exclusions as ChecksPerMinuteStatus — it literally calls
// the same summing function — so the number the form warns about and the
// number the dispatch gate meters can never drift apart.
//
// A nil receiver (entitlements disabled) yields a zero projection and no
// error: there is no cap to be over.
func (s *Service) ProjectChecksPerMinute(
	ctx context.Context, orgUID string, proposal CheckRateProposal,
) (ProjectedRate, error) {
	if s == nil {
		return ProjectedRate{}, nil
	}

	resolved, err := s.Resolve(ctx, orgUID)
	if err != nil {
		return ProjectedRate{}, fmt.Errorf("resolve entitlements: %w", err)
	}

	rates, err := s.db.ListOrgCheckRates(ctx, orgUID)
	if err != nil {
		return ProjectedRate{}, fmt.Errorf("list check rates: %w", err)
	}

	projected := make([]models.CheckRate, 0, len(rates)+1)

	for i := range rates {
		if proposal.ExcludeCheckUID != "" && rates[i].UID == proposal.ExcludeCheckUID {
			continue
		}

		projected = append(projected, rates[i])
	}

	if proposal.Period > 0 {
		projected = append(projected, models.CheckRate{
			Enabled: proposal.Enabled,
			Period:  timeutils.Duration(proposal.Period),
			Regions: proposal.Regions,
			Type:    proposal.Type,
		})
	}

	return ProjectedRate{
		Demand:  checksPerMinuteRate(projected, true),
		Current: checksPerMinuteRate(rates, true),
		Limit:   resolved.Limits.MaxChecksPerMinute,
	}, nil
}
