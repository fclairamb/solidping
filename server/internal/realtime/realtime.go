// Package realtime fans org-scoped, data-free "hint" events out to dashboard
// stream subscribers. Hints carry only {org, kinds} — the client invalidates
// the matching query caches and refetches over the normal REST API, so
// delivery is best-effort by design: a missed hint is corrected by the next
// hint, the client's slow fallback poll, or a resync after reconnect.
//
// The fan-out rides the existing notifier.EventNotifier bus: PostgreSQL
// LISTEN/NOTIFY across API replicas, in-process channels on SQLite. PostgreSQL
// acts as a wake-up signal, never as a message relay — there is no durable
// event log and no schema.
package realtime

import (
	"encoding/json"
	"fmt"
	"sort"
)

// ChannelOrgEvents is the single notifier bus channel every org hint event is
// published on. One LISTEN per process (the Hub's), regardless of how many
// dashboard streams are open.
const ChannelOrgEvents = "org.events"

// Kind identifies a family of org-scoped resources a hint invalidates.
type Kind string

// Hint kinds. The client maps each kind to the TanStack Query caches to
// invalidate.
const (
	// KindResults signals new check results (coalesced — high volume).
	KindResults Kind = "results"
	// KindChecks signals a check status transition or check config change.
	KindChecks Kind = "checks"
	// KindIncidents signals incident open/close/escalation/acknowledge.
	KindIncidents Kind = "incidents"
	// KindEvents signals a new audit/timeline event row.
	KindEvents Kind = "events"
	// KindJobs signals background-job lifecycle changes (coalesced).
	KindJobs Kind = "jobs"
)

// Hint is the JSON payload published on the bus: org uid plus the dirty kinds.
// Far below the 8KB NOTIFY payload limit by construction.
type Hint struct {
	Org   string   `json:"org"`
	Kinds []string `json:"kinds"`
}

// EncodeHint serializes a hint for the notifier bus. Kinds are sorted so
// payloads are deterministic (stable tests, readable logs).
func EncodeHint(orgUID string, kinds map[Kind]struct{}) (string, error) {
	names := make([]string, 0, len(kinds))
	for k := range kinds {
		names = append(names, string(k))
	}
	sort.Strings(names)

	data, err := json.Marshal(Hint{Org: orgUID, Kinds: names})
	if err != nil {
		return "", fmt.Errorf("encoding hint: %w", err)
	}

	return string(data), nil
}

// DecodeHint parses a bus payload back into a hint.
func DecodeHint(payload string) (Hint, error) {
	var h Hint
	if err := json.Unmarshal([]byte(payload), &h); err != nil {
		return Hint{}, fmt.Errorf("decoding hint: %w", err)
	}

	return h, nil
}

// kindSet builds a set from a kind list.
func kindSet(kinds []Kind) map[Kind]struct{} {
	set := make(map[Kind]struct{}, len(kinds))
	for _, k := range kinds {
		set[k] = struct{}{}
	}

	return set
}
