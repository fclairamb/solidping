// Package maintenance resolves whether a check is inside an active maintenance
// window, with a small in-process TTL cache.
//
// This used to live as an unexported method on the incidents service, where it
// answered exactly one question: "should this result be allowed to open an
// incident?". Since spec 2026-08-20-01 the same answer is also needed one step
// EARLIER — at ingest, to tag `results.maintenance`, because rollup buckets
// cannot be sliced after the fact and an SLO that cannot subtract planned work
// is not trustworthy for any team that does planned work.
//
// Two copies of that logic drifting apart would be a silent correctness bug:
// an incident suppressed as maintenance but its results tagged as production
// (or the reverse) would make the SLO page contradict the incident list. So it
// lives here once, and both callers share one instance and therefore one cache.
package maintenance

import (
	"context"
	"sync"
	"time"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/utils/clock"
)

// CacheTTL bounds how long a check's maintenance-window definitions are cached
// in-process before re-fetching. Windows are mutated infrequently and emit no
// event today, so a 60 s TTL trades a small staleness window (≤60 s after a
// create/edit/delete) for eliminating a DB round-trip on the per-result hot
// path. Active/inactive transitions stay exact because models.IsActiveAt is
// re-evaluated on every call against the cached definitions.
const CacheTTL = 60 * time.Second

// WindowLister is the slice of db.Service this resolver needs.
type WindowLister interface {
	ListMaintenanceWindowsForCheck(ctx context.Context, checkUID string) ([]*models.MaintenanceWindow, error)
}

// cacheEntry is one check's cached window definitions plus the clock time they
// were fetched at.
type cacheEntry struct {
	windows   []*models.MaintenanceWindow
	fetchedAt time.Time
}

// Resolver answers "is this check in maintenance?" with a per-check TTL cache.
// The zero value is not usable; build one with NewResolver.
type Resolver struct {
	db    WindowLister
	clock clock.Clock

	mu    sync.RWMutex
	cache map[string]cacheEntry
}

// NewResolver builds a resolver. clk must not be nil.
func NewResolver(lister WindowLister, clk clock.Clock) *Resolver {
	return &Resolver{
		db:    lister,
		clock: clk,
		cache: make(map[string]cacheEntry),
	}
}

// IsActive reports whether the check is inside an active maintenance window
// right now (as told by the injected clock).
func (r *Resolver) IsActive(ctx context.Context, checkUID string) (bool, error) {
	return r.IsActiveAt(ctx, checkUID, r.clock.Now())
}

// IsActiveAt reports whether the check is inside an active maintenance window
// at the given instant. The cache is keyed only by check UID — window
// definitions do not depend on `at`, only the IsActiveAt evaluation does, so a
// backdated query (result backfill, test-data generation) reuses the same
// cached definitions without polluting them.
func (r *Resolver) IsActiveAt(ctx context.Context, checkUID string, at time.Time) (bool, error) {
	windows, err := r.windowsFor(ctx, checkUID)
	if err != nil {
		return false, err
	}

	for _, window := range windows {
		if models.IsActiveAt(window, at) {
			return true, nil
		}
	}

	return false, nil
}

func (r *Resolver) windowsFor(ctx context.Context, checkUID string) ([]*models.MaintenanceWindow, error) {
	now := r.clock.Now()

	r.mu.RLock()
	entry, ok := r.cache[checkUID]
	r.mu.RUnlock()

	if ok && now.Sub(entry.fetchedAt) < CacheTTL {
		return entry.windows, nil
	}

	windows, err := r.db.ListMaintenanceWindowsForCheck(ctx, checkUID)
	if err != nil {
		return nil, err
	}

	r.mu.Lock()
	r.cache[checkUID] = cacheEntry{windows: windows, fetchedAt: now}
	r.mu.Unlock()

	return windows, nil
}
