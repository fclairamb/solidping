package models

import (
	"slices"
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
	Capabilities []string   `bun:"capabilities,type:text[],array,nullzero"`
	CreatedAt    time.Time  `bun:"created_at,notnull,default:current_timestamp"`
	UpdatedAt    time.Time  `bun:"updated_at,notnull,default:current_timestamp"`
	DeletedAt    *time.Time `bun:"deleted_at"`
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
