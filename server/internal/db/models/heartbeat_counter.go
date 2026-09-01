package models

import (
	"time"

	"github.com/uptrace/bun"
)

// HeartbeatCounter is the last accepted SP2 replay counter for one heartbeat
// check (spec 2026-09-01-06).
//
// The embedded push transport's signed message form carries a
// strictly-increasing counter; the server accepts a beat only when its counter
// is strictly greater than the value stored here, then advances it. That is
// what makes an SP2 beat unreplayable — an old datagram fails, and even the
// most recent one cannot be sent twice.
//
// A row exists only for checks that have actually accepted a signed beat.
type HeartbeatCounter struct {
	bun.BaseModel `bun:"table:heartbeat_counters"`

	CheckUID    string    `bun:"check_uid,pk"`
	LastCounter int64     `bun:"last_counter,notnull"`
	UpdatedAt   time.Time `bun:"updated_at,notnull"`
}
