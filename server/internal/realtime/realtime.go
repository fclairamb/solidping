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

// CollapseCheckUids is the storm-collapse threshold: when a flush window
// accumulates more distinct check uids for an org than this, the publisher
// replaces the list with ["*"] rather than let the payload grow toward the
// 8KB NOTIFY limit. Subscribers treat ["*"] as "every check of this org".
const CollapseCheckUids = 64

// checkUidsWildcard is the sentinel list meaning "every check of the org".
var checkUidsWildcard = []string{"*"}

// Hint is the JSON payload published on the bus: org uid, the dirty kinds,
// and the check uids the check-attributable kinds (results, checks,
// incidents) apply to. events/jobs are org-level and never contribute to
// CheckUids. Far below the 8KB NOTIFY payload limit by construction — see
// CollapseCheckUids for the storm-collapse guarantee.
type Hint struct {
	Org       string   `json:"org"`
	Kinds     []string `json:"kinds"`
	CheckUids []string `json:"checkUids,omitempty"`
}

// EncodeHint serializes a hint for the notifier bus. Kinds and check uids are
// sorted so payloads are deterministic (stable tests, readable logs). A
// checkUids set larger than CollapseCheckUids collapses to the ["*"]
// wildcard.
func EncodeHint(orgUID string, kinds map[Kind]struct{}, checkUids map[string]struct{}) (string, error) {
	names := make([]string, 0, len(kinds))
	for k := range kinds {
		names = append(names, string(k))
	}
	sort.Strings(names)

	data, err := json.Marshal(Hint{Org: orgUID, Kinds: names, CheckUids: collapseCheckUids(checkUids)})
	if err != nil {
		return "", fmt.Errorf("encoding hint: %w", err)
	}

	return string(data), nil
}

// collapseCheckUids sorts a check-uid set into a deterministic slice, or
// collapses it to the ["*"] wildcard once it exceeds CollapseCheckUids. A nil
// or empty set returns nil (org-level-only hint, e.g. events/jobs).
func collapseCheckUids(checkUids map[string]struct{}) []string {
	if len(checkUids) == 0 {
		return nil
	}
	if len(checkUids) > CollapseCheckUids {
		return checkUidsWildcard
	}

	uids := make([]string, 0, len(checkUids))
	for uid := range checkUids {
		uids = append(uids, uid)
	}
	sort.Strings(uids)

	return uids
}

// IsWildcardCheckUids reports whether a hint's CheckUids is the ["*"] storm-
// collapse sentinel meaning "every check of this org".
func IsWildcardCheckUids(checkUids []string) bool {
	return len(checkUids) == 1 && checkUids[0] == "*"
}

// encodePendingHint serializes a publisher's pending per-org state, honoring
// an already-collapsed check-uid set (the wildcard, once tripped, must stay
// the wildcard even though the underlying set was cleared to bound memory).
func encodePendingHint(orgUID string, pending *pendingOrg) (string, error) {
	names := make([]string, 0, len(pending.kinds))
	for k := range pending.kinds {
		names = append(names, string(k))
	}
	sort.Strings(names)

	checkUids := checkUidsWildcard
	if !pending.collapsed {
		checkUids = collapseCheckUids(pending.checkUids)
	}

	data, err := json.Marshal(Hint{Org: orgUID, Kinds: names, CheckUids: checkUids})
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
