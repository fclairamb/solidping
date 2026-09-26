package models

import "time"

// FixedWindow is one fixed-window counter an admission must pass: at most
// Limit events per Window, counted in the org-scoped state entry Key. Used by
// db.Service.AdmitFixedWindows ("Capture now", spec 2026-09-25-34).
type FixedWindow struct {
	Key    string
	Limit  int
	Window time.Duration
}

// Keys of a fixed-window counter's state-entry value.
const (
	fixedWindowKeyCount = "count"
	fixedWindowKeyStart = "windowStart"
)

// FixedWindowState reads a counter's (count, window start) from its state
// entry as it stands at `now`: an absent, soft-deleted, unreadable or elapsed
// entry is a fresh window starting now with a count of zero.
func (w FixedWindow) FixedWindowState(entry *StateEntry, now time.Time) (int, time.Time) {
	if entry == nil || entry.DeletedAt != nil || entry.Value == nil {
		return 0, now
	}

	value := *entry.Value

	startRaw, ok := value[fixedWindowKeyStart].(string)
	if !ok {
		return 0, now
	}

	start, err := time.Parse(time.RFC3339Nano, startRaw)
	if err != nil || !now.Before(start.Add(w.Window)) {
		return 0, now
	}

	var count int

	switch raw := value[fixedWindowKeyCount].(type) {
	case float64:
		count = int(raw)
	case int:
		count = raw
	case int64:
		count = int(raw)
	}

	return count, start
}

// FixedWindowValue is the state-entry value for (count, start).
func FixedWindowValue(count int, start time.Time) JSONMap {
	return JSONMap{
		fixedWindowKeyCount: count,
		fixedWindowKeyStart: start.UTC().Format(time.RFC3339Nano),
	}
}
