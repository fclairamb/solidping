package checks

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

// fieldReminderIntervalSeconds is the wire name of the per-check reminder
// interval (spec 2026-10-12-16).
const fieldReminderIntervalSeconds = "reminderIntervalSeconds"

// CodeInvalidReminderInterval is a reminderIntervalSeconds outside the allowed
// values.
const CodeInvalidReminderInterval = "INVALID_REMINDER_INTERVAL"

// errReminderIntervalOutOfRange is returned when reminderIntervalSeconds is
// neither 0 (off) nor between 15 minutes and 24 hours.
var errReminderIntervalOutOfRange = fmt.Errorf(
	"must be 0 (off) or between %d and %d seconds (15 minutes to 24 hours)",
	models.ReminderIntervalMinSeconds, models.ReminderIntervalMaxSeconds,
)

// validateReminderInterval range-checks a reminder interval. Wrapped with the
// field name so the 400 says which field it is about.
func validateReminderInterval(seconds int) error {
	if !models.ValidReminderIntervalSeconds(seconds) {
		return fmt.Errorf("%s: %w", fieldReminderIntervalSeconds, errReminderIntervalOutOfRange)
	}

	return nil
}

// OptionalInt is a nullable integer a PATCH can set, clear or leave alone:
// an absent field leaves the stored value unchanged, an explicit JSON null
// clears it, a number sets it. A plain *int cannot tell the first two apart.
type OptionalInt struct {
	// Set is true when the field was present in the request, null included.
	Set bool
	// Value is the number, nil for an explicit null.
	Value *int
}

// errOptionalIntNotANumber is returned for a value that is neither null nor an
// integer.
var errOptionalIntNotANumber = errors.New("must be an integer or null")

// UnmarshalJSON records that the field was present.
func (o *OptionalInt) UnmarshalJSON(data []byte) error {
	o.Set = true

	if string(data) == "null" {
		o.Value = nil

		return nil
	}

	var value int
	if err := json.Unmarshal(data, &value); err != nil {
		return fmt.Errorf("%w: %w", errOptionalIntNotANumber, err)
	}

	o.Value = &value

	return nil
}

// MarshalJSON renders the value, or null.
func (o OptionalInt) MarshalJSON() ([]byte, error) {
	if o.Value == nil {
		return []byte("null"), nil
	}

	return json.Marshal(*o.Value)
}

// IsZero reports an absent field, so `omitzero` drops it on marshal.
func (o OptionalInt) IsZero() bool {
	return !o.Set
}

// optionalIntOf builds a present OptionalInt from a pointer (nil is null).
func optionalIntOf(value *int) OptionalInt {
	return OptionalInt{Set: true, Value: value}
}

// applyReminderIntervalUpdate maps a PATCH's reminderIntervalSeconds onto a
// CheckUpdate: absent leaves it, null puts the check back on the org default,
// a value is validated and set.
func applyReminderIntervalUpdate(update *models.CheckUpdate, value OptionalInt) error {
	if !value.Set {
		return nil
	}

	if value.Value == nil {
		update.ClearReminderInterval = true

		return nil
	}

	if err := validateReminderInterval(*value.Value); err != nil {
		return err
	}

	update.ReminderIntervalSeconds = value.Value

	return nil
}

// appendReminderFinding checks reminderIntervalSeconds (spec 2026-10-12-16).
func appendReminderFinding(findings []requestFieldFinding, values *requestFieldValues) []requestFieldFinding {
	if values.ReminderIntervalSeconds == nil {
		return findings
	}

	if err := validateReminderInterval(*values.ReminderIntervalSeconds); err != nil {
		findings = append(findings, requestFieldFinding{
			Name: fieldReminderIntervalSeconds, Code: CodeInvalidReminderInterval,
			Message: errReminderIntervalOutOfRange.Error(), Err: err,
		})
	}

	return findings
}

// copyIntPtr returns an independent copy of a nullable int.
func copyIntPtr(value *int) *int {
	if value == nil {
		return nil
	}

	out := *value

	return &out
}
