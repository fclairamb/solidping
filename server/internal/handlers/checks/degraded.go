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
	errDegradedMExceedsN = errors.New("cannot exceed its window (M of N requires M <= N)")
)

// maxDegradedWindow caps a window at 1000 probes. Past that the query stops
// being answerable from the 24 h raw-retention band for any realistic period,
// and the rule would silently evaluate a window it never actually filled.
const maxDegradedWindow = 1000

var errDegradedWindowTooLarge = fmt.Errorf("must be <= %d probes", maxDegradedWindow)

// applyDegradedCreate copies the degraded configuration from a create request
// onto the new check. Absent fields keep models.NewCheck's fleet-calibrated
// defaults (5/60, 3/6, threshold 0, enabled).
func applyDegradedCreate(check *models.Check, req *CreateCheckRequest) error {
	values := degradedValues{
		Failures:       req.DegradedFailures,
		FailuresWindow: req.DegradedFailuresWindow,
		Slow:           req.DegradedSlow,
		SlowWindow:     req.DegradedSlowWindow,
		SlowThresholdMs: req.SlowThresholdMs,
	}
	if err := validateDegradedFields(values); err != nil {
		return err
	}

	if req.DegradedFailures != nil {
		check.DegradedFailures = *req.DegradedFailures
	}

	if req.DegradedFailuresWindow != nil {
		check.DegradedFailuresWindow = *req.DegradedFailuresWindow
	}

	if req.DegradedSlow != nil {
		check.DegradedSlow = *req.DegradedSlow
	}

	if req.DegradedSlowWindow != nil {
		check.DegradedSlowWindow = *req.DegradedSlowWindow
	}

	if req.SlowThresholdMs != nil {
		check.SlowThresholdMs = *req.SlowThresholdMs
	}

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
// Turning the feature ON also retires the dry-run stamp, so the check page's
// "would have fired — enable?" banner cannot keep asking for something the
// operator has just done.
func applyDegradedUpdate(update *models.CheckUpdate, req *UpdateCheckRequest) error {
	if err := validateDegradedFields(degradedValues{
		Failures:        req.DegradedFailures,
		FailuresWindow:  req.DegradedFailuresWindow,
		Slow:            req.DegradedSlow,
		SlowWindow:      req.DegradedSlowWindow,
		SlowThresholdMs: req.SlowThresholdMs,
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

// validateDegradedFields rejects negative values, oversized windows, and an M
// larger than its own N.
func validateDegradedFields(values degradedValues) error {
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

	if values.Failures != nil && values.FailuresWindow != nil &&
		*values.Failures > *values.FailuresWindow {
		return fmt.Errorf("degradedFailures: %w", errDegradedMExceedsN)
	}

	if values.Slow != nil && values.SlowWindow != nil &&
		*values.Slow > *values.SlowWindow {
		return fmt.Errorf("degradedSlow: %w", errDegradedMExceedsN)
	}

	return nil
}
