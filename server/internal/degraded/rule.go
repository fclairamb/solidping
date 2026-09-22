// Package degraded holds the degraded-detection rule primitive (spec
// 2026-09-22-03): "fires when M of the last N countable probes match", applied
// to two populations on the same check — failures, and successful-but-slow
// probes.
//
// It is deliberately PURE: no database, no clock of its own, no logging. The
// whole point of the split is that the window edge, the max-age rule, the
// maintenance-is-not-a-slot rule and the resolution streak are table-driven
// testable without a probe stream, a job runner or an incident row.
//
// Why probes and not seconds: a fixed denominator needs no `min_samples` floor,
// the meaning survives a period change, and alert latency scales with the
// sampling the operator already chose. The trade is that a 5-minute-period check
// gets a 5 h failure window; that is accepted, not overlooked.
package degraded

import (
	"time"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

// maxAgeWindowMultiplier bounds how far back "the last N probes" may reach:
// 2 x N x period. "The last 6 probes" of a check that was paused for two days
// must not include Tuesday.
const maxAgeWindowMultiplier = 2

// Probe is one raw result, reduced to what the rules read.
type Probe struct {
	At         time.Time
	Status     models.ResultStatus
	DurationMs float64
	// Maintenance is the ingest-time tag: a probe recorded while an active
	// maintenance window covered the check. Such a probe is SKIPPED, not counted
	// as a slot — planned work must neither trip a rule nor push a real failure
	// out of the window.
	Maintenance bool
}

// Countable reports whether this probe occupies a slot. Status in
// (up, down, timeout, error, warning); a lifecycle marker (created/running), an
// abandoned row (our infrastructure failing, not the target's) and a maintenance
// probe are all non-slots.
func (p Probe) Countable() bool {
	if p.Maintenance {
		return false
	}

	switch p.Status {
	case models.ResultStatusUp, models.ResultStatusDown, models.ResultStatusTimeout,
		models.ResultStatusError, models.ResultStatusWarning:
		return true
	case models.ResultStatusCreated, models.ResultStatusRunning,
		models.ResultStatusDegraded, models.ResultStatusAbandoned:
		return false
	default:
		return false
	}
}

// Failed reports membership of the failure population: not up and not warning.
func (p Probe) Failed() bool {
	return !p.Status.CountsAsUp()
}

// Slow reports membership of the slow population for a given threshold: a
// SUCCESSFUL probe (up or warning) that took longer than the threshold. A failed
// probe is never also slow — it is already counted by the other rule, and a
// timeout would otherwise trip both.
func (p Probe) Slow(thresholdMs int) bool {
	if thresholdMs <= 0 || !p.Status.CountsAsUp() {
		return false
	}

	return p.DurationMs > float64(thresholdMs)
}

// Params is one check's rule configuration plus the period the max-age rule
// needs.
type Params struct {
	Failures       int
	FailuresWindow int
	Slow           int
	SlowWindow     int
	SlowThreshold  int
	Period         time.Duration
}

// FailureRuleActive reports whether the failure rule is configured at all.
// M = 0 disables it, and a zero window is meaningless.
func (p Params) FailureRuleActive() bool {
	return p.Failures > 0 && p.FailuresWindow > 0
}

// SlowRuleActive reports whether the slow rule is configured. It needs a
// threshold as well as an M and an N: with slow_threshold_ms = 0 there is no
// duration a probe could exceed, which is the documented "slow rule off" state.
func (p Params) SlowRuleActive() bool {
	return p.Slow > 0 && p.SlowWindow > 0 && p.SlowThreshold > 0
}

// RuleOutcome is what one population's evaluation concluded.
type RuleOutcome struct {
	// Active is false when the rule is not configured; Matches/Slots are then 0
	// and Fired is false.
	Active bool
	// Matches is how many of the examined probes belong to the population.
	Matches int
	// Slots is how many countable probes were actually available inside the
	// window and the max age — at most N, less on a young or long-paused check.
	Slots int
	// Threshold is the M this rule fired (or failed to fire) against.
	Threshold int
	// Window is the configured N.
	Window int
	// Fired reports Matches >= M.
	Fired bool
	// FirstMatchAt is when the OLDEST matching probe inside the window ran — the
	// honest "degraded since", and what the chart band and the notification's
	// deep link start from. Zero when nothing matched.
	FirstMatchAt time.Time
}

// Outcome is the whole evaluation of one check at one instant.
type Outcome struct {
	Failure RuleOutcome
	Slow    RuleOutcome
	// CleanStreak is how many consecutive countable probes, counting back from
	// the newest, belong to NEITHER population. Resolution reads this.
	CleanStreak int
	// ResolveWindow is the N that governs resolution: the larger window among
	// the rules that fired, or (when nothing fired) among the rules that are
	// configured. "When both populations fired, the larger N governs."
	ResolveWindow int
	// WindowStart / WindowEnd bound the probes actually examined, for the
	// notification's deep link into the episode.
	WindowStart time.Time
	WindowEnd   time.Time
}

// Firing reports whether either rule fired.
func (o Outcome) Firing() bool {
	return o.Failure.Fired || o.Slow.Fired
}

// StartedAt is when the degraded condition began: the oldest matching probe of
// whichever fired rule reaches furthest back. Zero when nothing fired.
func (o Outcome) StartedAt() time.Time {
	var out time.Time

	for _, rule := range []RuleOutcome{o.Failure, o.Slow} {
		if !rule.Fired || rule.FirstMatchAt.IsZero() {
			continue
		}

		if out.IsZero() || rule.FirstMatchAt.Before(out) {
			out = rule.FirstMatchAt
		}
	}

	return out
}

// Evaluate applies both rules to `probes`, which MUST be ordered newest first
// (the order the results query returns).
//
// Non-countable rows are skipped without consuming a slot, and a probe older
// than 2 x N x period never counts — both per rule, since the two rules have
// different Ns and therefore different reaches.
func Evaluate(probes []Probe, params Params, now time.Time) Outcome {
	out := Outcome{WindowEnd: now}

	if params.FailureRuleActive() {
		out.Failure = evaluateRule(probes, params, now, params.Failures, params.FailuresWindow, Probe.Failed)
	}

	if params.SlowRuleActive() {
		slow := func(p Probe) bool { return p.Slow(params.SlowThreshold) }
		out.Slow = evaluateRule(probes, params, now, params.Slow, params.SlowWindow, slow)
	}

	out.ResolveWindow = resolveWindow(params, out)
	out.CleanStreak = cleanStreak(probes, params, now, out.ResolveWindow)
	out.WindowStart = windowStart(probes, params, now, out.ResolveWindow)

	return out
}

// evaluateRule counts one population over the newest `window` countable probes.
func evaluateRule(
	probes []Probe, params Params, now time.Time,
	threshold, window int, matches func(Probe) bool,
) RuleOutcome {
	out := RuleOutcome{Active: true, Threshold: threshold, Window: window}
	maxAge := maxAge(params.Period, window)

	for _, probe := range probes {
		if out.Slots >= window {
			break
		}

		if !probe.Countable() || tooOld(probe, now, maxAge) {
			continue
		}

		out.Slots++

		if matches(probe) {
			out.Matches++
			// probes run newest first, so every later assignment is older.
			out.FirstMatchAt = probe.At
		}
	}

	out.Fired = out.Matches >= threshold

	return out
}

// cleanStreak counts consecutive countable probes, newest first, that belong to
// neither population. It stops at the governing window: a streak longer than the
// window it has to beat is not more resolved than one exactly that long, and
// counting further would walk the whole retention band.
func cleanStreak(probes []Probe, params Params, now time.Time, window int) int {
	if window <= 0 {
		return 0
	}

	maxAge := maxAge(params.Period, window)
	streak := 0

	for _, probe := range probes {
		if streak >= window {
			break
		}

		if !probe.Countable() || tooOld(probe, now, maxAge) {
			continue
		}

		if probe.Failed() || probe.Slow(params.SlowThreshold) {
			break
		}

		streak++
	}

	return streak
}

// windowStart is when the oldest examined probe ran, so a notification can link
// into the span the numbers describe.
func windowStart(probes []Probe, params Params, now time.Time, window int) time.Time {
	if window <= 0 {
		return time.Time{}
	}

	maxAge := maxAge(params.Period, window)
	slots := 0

	var out time.Time

	for _, probe := range probes {
		if slots >= window {
			break
		}

		if !probe.Countable() || tooOld(probe, now, maxAge) {
			continue
		}

		slots++
		out = probe.At
	}

	return out
}

// resolveWindow picks the N that governs resolution.
func resolveWindow(params Params, out Outcome) int {
	window := 0

	consider := func(rule RuleOutcome, onlyFired bool) {
		if !rule.Active || (onlyFired && !rule.Fired) {
			return
		}

		if rule.Window > window {
			window = rule.Window
		}
	}

	if out.Firing() {
		consider(out.Failure, true)
		consider(out.Slow, true)

		return window
	}

	consider(out.Failure, false)
	consider(out.Slow, false)

	return window
}

// maxAge is the 2 x N x period reach of a window.
func maxAge(period time.Duration, window int) time.Duration {
	if period <= 0 || window <= 0 {
		return 0
	}

	return maxAgeWindowMultiplier * time.Duration(window) * period
}

// tooOld reports whether a probe falls outside the window's reach. A zero
// maxAge (unknown period) disables the rule rather than dropping everything —
// refusing to evaluate a check whose period we cannot read would be a silent
// hole, and the window count still bounds the query.
func tooOld(probe Probe, now time.Time, maxAge time.Duration) bool {
	if maxAge <= 0 {
		return false
	}

	return now.Sub(probe.At) > maxAge
}
