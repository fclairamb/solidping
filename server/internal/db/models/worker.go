package models

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Worker represents a distributed worker that executes checks.
type Worker struct {
	UID          string     `bun:"uid,pk,type:varchar(36)"`
	Slug         string     `bun:"slug,notnull"`
	Name         string     `bun:"name,notnull"`
	Region       *string    `bun:"region"`
	LastActiveAt *time.Time `bun:"last_active_at"`
	// Capabilities is the worker's self-reported capability set — the names of
	// the things it CAN do — refreshed alongside last_active_at (specs
	// 2026-08-15-11, 2026-08-16-02). One generic set rather than a column per
	// capability, so the next capability is a pure string addition.
	//
	// THREE STATES, NOT TWO, AND NIL IS THE ONLY UNKNOWN:
	//
	//	nil               unknown — nothing was ever reported (a worker that
	//	                  predates the feature, or has not checked in yet)
	//	[]string{}        reported, and this worker has none of them
	//	[]string{"ipv6"}  reported this exact set
	//
	// A NON-NIL SET IS AUTHORITATIVE AND CLOSED: absence from it means "no",
	// never "unknown". Conflating the two is precisely the lie this feature
	// exists to stop telling — it would paint every worker predating the
	// capability report as IPv6-incapable. Read it through Capability() rather
	// than by hand so the unknown case cannot be forgotten.
	//
	// The bun tag mirrors Check.Regions: pgdialect encodes it as the `text[]`
	// the Postgres schema declares, sqlitedialect ignores `array` and stores
	// the JSON array the SQLite schema expects. `nullzero` is what maps a nil
	// slice to SQL NULL; a non-nil empty slice is NOT zero for bun (its zero
	// checker for slices is "is nil"), so it still writes `{}` / `[]`.
	Capabilities []string `bun:"capabilities,type:text[],array,nullzero"`
	// Version is the worker's self-reported build version (specs
	// 2026-08-19-07), refreshed alongside Capabilities. UNLIKE Capabilities,
	// this is a TWO-state field, not three: a real build version is never
	// the empty string, so there is no meaningful "reported, and has none"
	// answer for a scalar the way there is for a set.
	//
	//	nil       unknown — nothing was ever reported (a worker that predates
	//	          the feature, or has not sent a claim frame yet)
	//	&"x.y.z"  the version this worker last reported
	//
	// nil is the only unknown, and it must never be rendered as "drifted" —
	// an old agent that predates version reporting must not look broken. The
	// match/drifted/unknown comparison against the server's own version is
	// computed at read time (handlers/agents), not stored here.
	Version   *string    `bun:"version"`
	CreatedAt time.Time  `bun:"created_at,notnull,default:current_timestamp"`
	UpdatedAt time.Time  `bun:"updated_at,notnull,default:current_timestamp"`
	DeletedAt *time.Time `bun:"deleted_at"`
}

// Capability names, as stored verbatim in workers.capabilities and re-exported
// by internal/regions. They live here because they are the values the column
// holds, and because a producer (the worker self-probe, the agent claim path)
// must be able to name one without importing the region service.
//
// Names are lowercase `[a-z0-9-]+` slugs; the database CHECK constraint and the
// SQLite triggers reject anything else.
const (
	// CapabilityIPv4 means the worker can originate IPv4 traffic.
	CapabilityIPv4 = "ipv4"
	// CapabilityIPv6 means the worker can originate IPv6 traffic.
	CapabilityIPv6 = "ipv6"
	// CapabilityBrowser means the worker can actually run a `browser` check:
	// a reachable remote Chrome (CDP) endpoint, or a local Chrome/Chromium
	// binary. Self-probed like the egress families, and advisory in the same
	// way — it drives a creation-time warning, never scheduling.
	CapabilityBrowser = "browser"
)

// CapabilityState is the THREE-state answer to "does this worker have X?".
// A distinct type rather than a bool, so a caller physically cannot collapse
// "unknown" into "no".
type CapabilityState uint8

const (
	// CapabilityStateUnknown means the worker has never reported its set.
	CapabilityStateUnknown CapabilityState = iota
	// CapabilityStateAbsent means the worker reported, and this capability was
	// not in the set. That is a real "no", not an absence of information.
	CapabilityStateAbsent
	// CapabilityStatePresent means the worker reported this capability.
	CapabilityStatePresent
)

// Capability answers, as a tri-state, whether this worker reports capability
// name. A nil set is unknown; a non-nil set is closed.
func (w *Worker) Capability(name string) CapabilityState {
	if w.Capabilities == nil {
		return CapabilityStateUnknown
	}

	if slices.Contains(w.Capabilities, name) {
		return CapabilityStatePresent
	}

	return CapabilityStateAbsent
}

// ValidateCapabilitySet reports whether a reported set is storable: every name
// a lowercase `[a-z0-9-]+` slug, and no duplicates. It is the Go mirror of the
// database CHECK constraint (Postgres) and triggers (SQLite).
//
// IT EXISTS TO PROTECT LIVENESS, NOT TO REPLACE THE DATABASE. A capability set
// arrives from a remote agent, so it is untrusted input on a code path that
// also refreshes last_active_at. Letting a malformed array fail the whole
// UPDATE would take the region's liveness down with it — the agent would still
// be running checks while its region quietly went stale. Callers reject the set
// (falling back to "not reported", which changes nothing) and still write the
// heartbeat. The database remains the authority that garbage is never stored.
//
// A nil set is valid: it means "not reported".
func ValidateCapabilitySet(capabilities []string) error {
	seen := make(map[string]struct{}, len(capabilities))

	for _, name := range capabilities {
		if name == "" {
			return fmt.Errorf("%w: empty capability name", ErrInvalidCapabilitySet)
		}

		for _, char := range name {
			if (char < 'a' || char > 'z') && (char < '0' || char > '9') && char != '-' {
				return fmt.Errorf("%w: %q is not a lowercase [a-z0-9-] slug",
					ErrInvalidCapabilitySet, name)
			}
		}

		// `null` is RESERVED, not merely discouraged. Postgres renders any
		// element equal to "NULL" case-insensitively in quotes (`{"null"}`),
		// because a BARE `NULL` in an array's text form IS the NULL element —
		// so the `text[]` CHECK constraint refuses the name outright. Without
		// this branch a set containing it would pass here, store fine on
		// SQLite, and hard-fail the Postgres write, taking last_active_at down
		// with it: precisely the liveness failure this validator prevents.
		if strings.EqualFold(name, reservedCapabilityNull) {
			return fmt.Errorf("%w: %q is reserved", ErrInvalidCapabilitySet, name)
		}

		if _, dup := seen[name]; dup {
			return fmt.Errorf("%w: %q reported twice", ErrInvalidCapabilitySet, name)
		}

		seen[name] = struct{}{}
	}

	return nil
}

// ErrInvalidCapabilitySet is returned by ValidateCapabilitySet.
var ErrInvalidCapabilitySet = errors.New("invalid capability set")

// reservedCapabilityNull is the one name no dialect may store — see
// ValidateCapabilitySet for why Postgres cannot represent it unambiguously.
const reservedCapabilityNull = "null"

// NewWorker creates a new worker with generated UID.
func NewWorker(slug, name string) *Worker {
	now := time.Now()

	return &Worker{
		UID:       uuid.New().String(),
		Slug:      slug,
		Name:      name,
		CreatedAt: now,
		UpdatedAt: now,
	}
}

// WorkerUpdate represents fields that can be updated.
type WorkerUpdate struct {
	Slug         *string
	Name         *string
	Region       *string
	LastActiveAt *time.Time
}
