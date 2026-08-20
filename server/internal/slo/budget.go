package slo

import (
	"math"
	"time"

	"github.com/fclairamb/solidping/server/internal/uptimebar"
)

// Objective states, as surfaced to the dashboard.
const (
	// StateUnknown means the window carries no countable probe at all. It is
	// deliberately NOT "healthy": no data is not 100%, the same rule the
	// availability API already follows (availabilityPct is nullable there).
	StateUnknown = "unknown"
	// StateHealthy means budget remains and it is being spent no faster than
	// the objective allows.
	StateHealthy = "healthy"
	// StateAtRisk means budget still remains but is being spent faster than
	// sustainable — the whole point of tracking a burn rate.
	StateAtRisk = "at_risk"
	// StateBreached means the window's error budget is spent.
	StateBreached = "breached"
)

// Input is everything Compute needs. Everything is explicit — no clocks, no
// database, no globals — so the math is exhaustively table-testable.
type Input struct {
	// TargetPct is the objective, e.g. 99.9.
	TargetPct float64
	// Stats is the merged availability bucket for the scope over Window.
	Stats uptimebar.BucketStats
	// Window is the calendar period being evaluated.
	Window Window
	// Now is the evaluation instant. For a closed past window this may sit
	// after Window.End, in which case the window is simply complete.
	Now time.Time
	// CoverageStart is the earliest instant the scope could have been measured
	// (typically the check's creation). Zero means "no clamp". It is what makes
	// a check created on the 20th report a partial month instead of 19 days of
	// phantom downtime.
	CoverageStart time.Time
	// ExcludeMaintenance subtracts probes recorded under an active maintenance
	// window from both the numerator and the denominator.
	ExcludeMaintenance bool
}

// Status is the computed objective state for one window.
type Status struct {
	TargetPct float64
	// AttainmentPct is nil when the window carries no countable probe.
	// Rendering nil as 100% would be the single most dangerous bug in this
	// feature: it turns "we were not watching" into "everything was fine".
	AttainmentPct    *float64
	HasData          bool
	TotalChecks      int
	SuccessfulChecks int

	// MonitoredSeconds is the whole window's monitorable duration (clamped by
	// CoverageStart, and by observed maintenance when excluded). It is the
	// basis of the budget: "99.9% monthly" allows 0.1% of the MONTH, not 0.1%
	// of however much of the month has elapsed so far.
	MonitoredSeconds int64
	// ElapsedSeconds is the part of MonitoredSeconds that has already happened.
	// It is the basis of consumption.
	ElapsedSeconds int64

	BudgetTotalSeconds     int64
	BudgetConsumedSeconds  int64
	BudgetRemainingSeconds int64
	// ExcludedMaintenanceSeconds is how much elapsed time was recorded under an
	// active maintenance window. Reported even when ExcludeMaintenance is off,
	// because a partially-covered month (tagging only accrues from deploy day)
	// has to be legible rather than merely correct.
	ExcludedMaintenanceSeconds int64

	// BurnRate is observed error rate ÷ allowed error rate. 1.0 spends the
	// budget exactly over the window; 2.0 spends it in half the window. Nil
	// when there is no data, or when the target is 100% (no allowance to
	// divide by).
	BurnRate *float64
	// ProjectedExhaustionAt is when the remaining budget runs out at the
	// current burn rate. Nil when the budget is not being consumed, is already
	// spent, or would survive past the end of the window.
	ProjectedExhaustionAt *time.Time

	State string
	// Partial is true when the window is still running or the scope did not
	// exist for all of it.
	Partial bool
}

// Compute evaluates an objective over one window.
//
//nolint:cyclop,funlen // one linear formula; splitting it hides the arithmetic.
func Compute(input *Input) Status {
	allowedFraction := 1 - input.TargetPct/100
	if allowedFraction < 0 {
		allowedFraction = 0
	}

	out := Status{TargetPct: input.TargetPct, State: StateUnknown}

	start := input.Window.Start
	if !input.CoverageStart.IsZero() && input.CoverageStart.After(start) {
		start = input.CoverageStart
	}

	end := input.Window.End
	elapsedEnd := end

	if input.Now.Before(end) {
		elapsedEnd = input.Now
	}

	monitored := math.Max(0, end.Sub(start).Seconds())
	elapsed := math.Max(0, elapsedEnd.Sub(start).Seconds())

	raw := input.Stats
	stats := raw

	// Maintenance share of the elapsed time, inferred from the probe ratio.
	// Probes are evenly spaced by construction (one check, one period), so the
	// share of probes tagged maintenance is the share of time under
	// maintenance — the same ratio-to-duration step the availability API
	// already makes for downtime.
	var excluded float64

	if raw.Total > 0 && raw.MaintTotal > 0 {
		excluded = float64(raw.MaintTotal) / float64(raw.Total) * elapsed
	}

	if input.ExcludeMaintenance {
		stats = raw.ExcludingMaintenance()
		elapsed = math.Max(0, elapsed-excluded)
		monitored = math.Max(0, monitored-excluded)
	}

	out.ExcludedMaintenanceSeconds = int64(math.Round(excluded))
	out.MonitoredSeconds = int64(math.Round(monitored))
	out.ElapsedSeconds = int64(math.Round(elapsed))
	out.TotalChecks = stats.Total
	out.SuccessfulChecks = stats.Up
	out.Partial = elapsedEnd.Before(end) || start.After(input.Window.Start)

	budgetTotal := allowedFraction * monitored
	out.BudgetTotalSeconds = int64(math.Round(budgetTotal))

	pct, ok := stats.AvailabilityPct()
	if !ok {
		// No countable probe. Attainment stays nil, the budget stays untouched,
		// and the state stays "unknown" — never a silent 100%.
		out.BudgetRemainingSeconds = out.BudgetTotalSeconds

		return out
	}

	out.HasData = true
	out.AttainmentPct = &pct

	failedFraction := 1 - pct/100
	if failedFraction < 0 {
		failedFraction = 0
	}

	consumed := failedFraction * elapsed
	out.BudgetConsumedSeconds = int64(math.Round(consumed))
	out.BudgetRemainingSeconds = out.BudgetTotalSeconds - out.BudgetConsumedSeconds

	if allowedFraction > 0 {
		burn := failedFraction / allowedFraction
		out.BurnRate = &burn
	}

	switch {
	case out.BudgetConsumedSeconds > out.BudgetTotalSeconds:
		out.State = StateBreached
	case out.BurnRate != nil && *out.BurnRate > 1:
		out.State = StateAtRisk
	default:
		out.State = StateHealthy
	}

	out.ProjectedExhaustionAt = projectExhaustion(input, &out, consumed, elapsed)

	return out
}

// projectExhaustion extrapolates the current consumption rate to the moment the
// remaining budget hits zero. It deliberately refuses to answer beyond the end
// of the window: an SLO window resets, so "you run out in March" is not a
// statement this month's budget can make.
func projectExhaustion(input *Input, out *Status, consumed, elapsed float64) *time.Time {
	if !out.HasData || elapsed <= 0 || consumed <= 0 {
		return nil
	}

	remaining := float64(out.BudgetRemainingSeconds)
	if remaining <= 0 {
		return nil
	}

	ratePerSecond := consumed / elapsed
	if ratePerSecond <= 0 {
		return nil
	}

	at := input.Now.Add(time.Duration(remaining/ratePerSecond) * time.Second)
	if !at.Before(input.Window.End) {
		return nil
	}

	return &at
}

// MergeStats folds several checks' buckets into the one bucket a group SLO is
// evaluated on.
//
// Summing Up/Total rather than averaging percentages is exactly the weighted
// average a single check computes, so a one-member group and that member's own
// SLO can never disagree — the same reasoning as statuspages.mergeBuckets. A
// member with no rows contributes nothing, which is not the same as
// contributing a zero.
func MergeStats(stats []uptimebar.BucketStats) uptimebar.BucketStats {
	var merged uptimebar.BucketStats

	for i := range stats {
		merged.Add(stats[i])
	}

	return merged
}
