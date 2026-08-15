package checkerdef

// Expiry severity labels emitted in check output. They let the dashboard render
// a consistent badge regardless of which checker produced the result.
const (
	// SeverityNone means the target is not near expiry (StatusUp).
	SeverityNone = ""
	// SeverityWarning means expiry is approaching the warning window
	// (StatusWarning — amber, counts as up, no incident).
	SeverityWarning = "warning"
	// SeverityCritical means expiry is at/inside the critical window
	// (StatusDown — pages, as a hard failure).
	SeverityCritical = "critical"
)

// GradedExpiryStatus maps a days-remaining value onto a two-tier expiry policy,
// returning the Status to report and a severity label for output. It is the
// single source of truth for the tiering rule shared by expiry-aware checkers
// (checkssl and checkdomain) so the comparison literal is not duplicated
// across checkers.
//
// Tiering (callers pass the MINIMUM days-remaining across whatever they
// monitor, e.g. the whole certificate chain):
//   - daysRemaining <= criticalDays                  → StatusDown,    SeverityCritical
//   - criticalDays  <  daysRemaining <= warningDays  → StatusWarning, SeverityWarning
//   - otherwise                                      → StatusUp,      SeverityNone
//
// Callers are expected to validate warningDays >= criticalDays >= 0; when that
// holds the warning band is non-empty only when warningDays > criticalDays.
func GradedExpiryStatus(daysRemaining, warningDays, criticalDays int) (Status, string) {
	switch {
	case daysRemaining <= criticalDays:
		return StatusDown, SeverityCritical
	case daysRemaining <= warningDays:
		return StatusWarning, SeverityWarning
	default:
		return StatusUp, SeverityNone
	}
}
