package models

import (
	"time"

	"github.com/uptrace/bun"
)

// TLSStorageEntry is one asset in the certmagic key-value store backing
// in-server ACME (spec 2026-07-26-01). Keys are certmagic's own path-like
// namespace (slash-separated, no leading/trailing slash), values are opaque
// bytes.
//
// SECURITY: values include ACME account keys and certificate PRIVATE KEYS.
// Nothing outside internal/tlsedge may read this table, and it must never be
// exposed through an API, export, or debug surface.
type TLSStorageEntry struct {
	bun.BaseModel `bun:"table:tls_storage"`

	Key        string    `bun:"key,pk"`
	Value      []byte    `bun:"value,notnull"`
	ModifiedAt time.Time `bun:"modified_at,notnull"`
}

// TLSStorageKeyInfo is the metadata certmagic's Stat/List need about a stored
// key. It deliberately carries no value bytes so listings never pull private
// keys into memory.
type TLSStorageKeyInfo struct {
	Key        string
	Size       int64
	ModifiedAt time.Time
}

// TLSStorageLock is an expiring, cluster-wide lock row backing certmagic's
// Locker interface. The holder refreshes ExpiresAt while it works; a stale row
// may be taken over by any node, which is what keeps a crashed issuance from
// wedging renewals forever.
type TLSStorageLock struct {
	bun.BaseModel `bun:"table:tls_storage_locks"`

	Key       string    `bun:"key,pk"`
	Owner     string    `bun:"owner,notnull"`
	ExpiresAt time.Time `bun:"expires_at,notnull"`
}
