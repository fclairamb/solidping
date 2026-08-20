package models

// Lowercase status wire names, the single source of truth for the string form
// of CheckStatus (and the matching subtest/table labels in tests). These are the
// values the dashboard keys status colors and labels on. Centralizing them keeps
// any single literal from repeating across the package (goconst).
const (
	// WireStatusCreated is the wire name for a just-created, not-yet-executed status.
	WireStatusCreated = "created"
	// WireStatusUp is the wire name for a healthy status.
	WireStatusUp = "up"
	// WireStatusDown is the wire name for a failing status.
	WireStatusDown = "down"
	// WireStatusValidating is the wire name for the transient pre-incident status.
	WireStatusValidating = "validating"
	// WireStatusDegraded is the wire name for the aggregated/summary degraded status.
	WireStatusDegraded = "degraded"
	// WireStatusWarning is the wire name for the live "up with something to report" status.
	WireStatusWarning = "warning"
	// WireStatusTimeout is the wire name for a timed-out status.
	WireStatusTimeout = "timeout"
	// WireStatusError is the wire name for an errored status.
	WireStatusError = "error"
	// WireStatusRunning is the wire name for an in-progress status.
	WireStatusRunning = "running"
	// WireStatusAbandoned is the wire name for a raw attempt the
	// abandoned-result reaper finalized: terminal, but excluded from every
	// availability calculation (spec 2026-08-18-10).
	WireStatusAbandoned = "abandoned"
	// WireStatusUnknown is the fallback wire name for an unrecognized status.
	WireStatusUnknown = "unknown"
)
