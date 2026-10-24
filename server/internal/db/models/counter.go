package models

// CounterValueKey is the key of a global attempt counter's state-entry value:
// {"count": N}. Used by db.Service.BumpGlobalCounter and its callers.
const CounterValueKey = "count"

// CounterValue extracts the integer count from a state entry holding a
// {"count": N} payload. JSON decoding turns numbers into float64, so every
// numeric shape is accepted; anything else (nil entry, nil value, missing or
// non-numeric count) reads as zero.
func CounterValue(entry *StateEntry) int {
	if entry == nil || entry.Value == nil {
		return 0
	}

	switch raw := (*entry.Value)[CounterValueKey].(type) {
	case float64:
		return int(raw)
	case int:
		return raw
	case int64:
		return int(raw)
	default:
		return 0
	}
}

// NewCounterValue builds the {"count": n} payload of a global attempt counter.
func NewCounterValue(n int) *JSONMap {
	return &JSONMap{CounterValueKey: n}
}
