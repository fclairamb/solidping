package checks

import (
	"errors"
	"fmt"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

// Degraded-detection validation errors (spec 2026-09-22-03).
var (
	errDegradedNegative = errors.New("must be >= 0 (0 = the rule is off)")
	// errDegradedMExceedsN rejects "7 of 6", which can never fire and is
	// therefore a silently-off rule the operator believes is on — the exact
	// failure mode this whole feature exists to eliminate.
	//
	// Both this and its mirror below are sentinels wrapped with the concrete
	// number they were compared against, because the other side of the
	// comparison is usually NOT in the request: it is the EFFECTIVE value —
	// the stored column, or the code default when that column is NULL. Saying
	// "its window" or "already configured" would be wrong on a create, where
	// nothing is configured yet and the default is what makes the rule
	// unreachable. The word "effective" is the one that covers both.
	errDegradedMExceedsN = errors.New("exceeds the effective window")
	// errDegradedNExceededByM is the same refusal seen from the other side: a
	// request that shrinks only the window below the effective M leaves exactly
	// the same dead rule behind.
	errDegradedNExceededByM = errors.New("is below the effective M")
)

// degradedRuleHint spells out the invariant both refusals violate, appended to
// each so the message stands on its own in an API error body.
const degradedRuleHint = "M of N requires M <= N, or set M to 0 to turn the rule off"

// maxDegradedWindow caps a window at 1000 probes. Past that the query stops
// being answerable from the 24 h raw-retention band for any realistic period,
// and the rule would silently evaluate a window it never actually filled.
const maxDegradedWindow = 1000

var errDegradedWindowTooLarge = fmt.Errorf("must be <= %d probes", maxDegradedWindow)

// applyDegradedCreate copies the degraded configuration from a create request
// onto the new check. An absent field stays nil — NULL in the database, which
// reads back as the fleet-calibrated default (5/60, 3/6, threshold 0) through
// models.Check's EffectiveDegraded* accessors. Nothing is written to store a
// default.
func applyDegradedCreate(check *models.Check, req *CreateCheckRequest) error {
	values := degradedValues{
		Failures:        req.DegradedFailures,
		FailuresWindow:  req.DegradedFailuresWindow,
		Slow:            req.DegradedSlow,
		SlowWindow:      req.DegradedSlowWindow,
		SlowThresholdMs: req.SlowThresholdMs,
	}
	// A fresh check has no stored configuration, so the code defaults are what
	// an unmentioned field will resolve to.
	if err := validateDegradedFields(values, degradedEffective{
		Failures:       models.DefaultDegradedFailures,
		FailuresWindow: models.DefaultDegradedFailuresWindow,
		Slow:           models.DefaultDegradedSlow,
		SlowWindow:     models.DefaultDegradedSlowWindow,
	}); err != nil {
		return err
	}

	check.DegradedFailures = req.DegradedFailures
	check.DegradedFailuresWindow = req.DegradedFailuresWindow
	check.DegradedSlow = req.DegradedSlow
	check.DegradedSlowWindow = req.DegradedSlowWindow
	check.SlowThresholdMs = req.SlowThresholdMs

	if req.DegradedEnabled != nil {
		check.DegradedEnabled = *req.DegradedEnabled
	}

	return nil
}

// degradedValues is the shape both the create and the update path validate, so
// "7 of 6" is refused on creation as well as on edit.
type degradedValues struct {
	Failures        *int
	FailuresWindow  *int
	Slow            *int
	SlowWindow      *int
	SlowThresholdMs *int
}

// applyDegradedUpdate validates and copies the degraded configuration from a
// PATCH onto the update.
//
// `check` is the stored row, needed so the M <= N rule is checked against the
// values the check will actually run under: a PATCH that raises M alone must be
// compared to the N already on the check (or the default N when that column is
// NULL), not waved through because the request happens not to mention N.
//
// Turning the feature ON also retires the dry-run stamp, so the check page's
// "would have fired — enable?" banner cannot keep asking for something the
// operator has just done.
func applyDegradedUpdate(
	update *models.CheckUpdate, req *UpdateCheckRequest, check *models.Check,
) error {
	if err := validateDegradedFields(degradedValues{
		Failures:        req.DegradedFailures,
		FailuresWindow:  req.DegradedFailuresWindow,
		Slow:            req.DegradedSlow,
		SlowWindow:      req.DegradedSlowWindow,
		SlowThresholdMs: req.SlowThresholdMs,
	}, degradedEffective{
		Failures:       check.EffectiveDegradedFailures(),
		FailuresWindow: check.EffectiveDegradedFailuresWindow(),
		Slow:           check.EffectiveDegradedSlow(),
		SlowWindow:     check.EffectiveDegradedSlowWindow(),
	}); err != nil {
		return err
	}

	update.DegradedFailures = req.DegradedFailures
	update.DegradedFailuresWindow = req.DegradedFailuresWindow
	update.DegradedSlow = req.DegradedSlow
	update.DegradedSlowWindow = req.DegradedSlowWindow
	update.SlowThresholdMs = req.SlowThresholdMs
	update.DegradedEnabled = req.DegradedEnabled

	if req.DegradedEnabled != nil && *req.DegradedEnabled {
		update.ClearDegradedWouldFireAt = true
	}

	return nil
}

// degradedEffective carries the N values a request that omits them will end up
// running under — the stored column, or the code default when it is NULL.
type degradedEffective struct {
	Failures       int
	FailuresWindow int
	Slow           int
	SlowWindow     int
}

// validateDegradedFields rejects negative values, oversized windows, and an M
// larger than its own N.
//
// The M <= N comparison uses the EFFECTIVE N (request value if present,
// `effective` otherwise). Comparing only when both arrive in the same request
// would let "degradedFailures: 70" through against a window of 60 and store a
// rule that can never fire — a silently-off rule the operator believes is on,
// which is what errDegradedMExceedsN exists to prevent.
func validateDegradedFields(values degradedValues, effective degradedEffective) error {
	fields := []struct {
		name  string
		value *int
	}{
		{"degradedFailures", values.Failures},
		{"degradedFailuresWindow", values.FailuresWindow},
		{"degradedSlow", values.Slow},
		{"degradedSlowWindow", values.SlowWindow},
		{"slowThresholdMs", values.SlowThresholdMs},
	}

	for _, field := range fields {
		if field.value != nil && *field.value < 0 {
			return fmt.Errorf("%s: %w", field.name, errDegradedNegative)
		}
	}

	for _, field := range []struct {
		name  string
		value *int
	}{
		{"degradedFailuresWindow", values.FailuresWindow},
		{"degradedSlowWindow", values.SlowWindow},
	} {
		if field.value != nil && *field.value > maxDegradedWindow {
			return fmt.Errorf("%s: %w", field.name, errDegradedWindowTooLarge)
		}
	}

	if err := validateDegradedRule(
		"degradedFailures", "degradedFailuresWindow",
		values.Failures, values.FailuresWindow, effective.Failures, effective.FailuresWindow,
	); err != nil {
		return err
	}

	return validateDegradedRule(
		"degradedSlow", "degradedSlowWindow",
		values.Slow, values.SlowWindow, effective.Slow, effective.SlowWindow,
	)
}

// validateDegradedRule checks M <= N for ONE rule, blaming whichever side the
// request actually moved: raising M is reported against M, shrinking N against
// N. A request that moves neither is always legal, because the stored pair was
// validated when it was written.
func validateDegradedRule(
	matchName, windowName string, matches, window *int, effectiveMatches, effectiveWindow int,
) error {
	if resolvedWindow := intOr(window, effectiveWindow); matches != nil && *matches > resolvedWindow {
		return fmt.Errorf("%s: %d %w of %d (%s)",
			matchName, *matches, errDegradedMExceedsN, resolvedWindow, degradedRuleHint)
	}

	// The mirror case: a request that only SHRINKS the window must not strand an
	// M already above it.
	if matches == nil && window != nil && effectiveMatches > *window {
		return fmt.Errorf("%s: %d %w of %d (%s)",
			windowName, *window, errDegradedNExceededByM, effectiveMatches, degradedRuleHint)
	}

	return nil
}

// intOr returns *value when set, fallback otherwise.
func intOr(value *int, fallback int) int {
	if value == nil {
		return fallback
	}

	return *value
}
