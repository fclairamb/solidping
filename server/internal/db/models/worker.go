package models

import (
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
	// EgressIPv4 / EgressIPv6 are the worker's self-reported egress families,
	// refreshed alongside last_active_at (spec 2026-08-15-11).
	//
	// NULLABLE, AND NULL MEANS "UNKNOWN". A worker that predates the field, or
	// one that has not reported yet, is NOT the same thing as a worker with no
	// IPv6 route, and must never be rendered as one — that conflation is
	// precisely the lie this feature exists to stop telling. Every reader has
	// to carry the three-state value through; do not default these to false.
	EgressIPv4 *bool      `bun:"egress_ipv4"`
	EgressIPv6 *bool      `bun:"egress_ipv6"`
	CreatedAt  time.Time  `bun:"created_at,notnull,default:current_timestamp"`
	UpdatedAt  time.Time  `bun:"updated_at,notnull,default:current_timestamp"`
	DeletedAt  *time.Time `bun:"deleted_at"`
}

// WorkerEgress carries a worker's self-reported egress families across the
// registration/heartbeat boundary. A nil field means "not reported" and leaves
// whatever the row already holds untouched — an agent that cannot answer never
// overwrites a known value with a fabricated one.
type WorkerEgress struct {
	IPv4 *bool
	IPv6 *bool
}

// Empty reports whether nothing at all was reported, in which case the
// heartbeat writes no capability columns.
func (e WorkerEgress) Empty() bool {
	return e.IPv4 == nil && e.IPv6 == nil
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
