package agents

import (
	"context"
	"errors"
	"sync"
	"time"
)

// ErrReplayedNonce is returned when an (agent, nonce) pair is presented twice
// inside the retention window — i.e. a replayed reconnect signature.
var ErrReplayedNonce = errors.New("agent request nonce already used within the window")

// NonceGuard remembers consumed reconnect nonces so a signature cannot be
// replayed within the clock-skew window. It is the seam between the WS handler
// and where that memory actually lives.
//
// Production uses a DATABASE-backed guard: agent reconnects hit whichever API
// replica the load balancer picks, and a fly.io platform region is a fleet of
// machines reconnecting continuously, so a per-process cache would happily
// accept against replica B a signature already spent on replica A (spec
// 2026-07-27-01, P7). NonceCache below is the in-memory implementation kept for
// tests and for a no-database context; it is only sound with a single replica.
//
// Implementations MUST fail closed: any error other than ErrReplayedNonce still
// rejects the reconnect, which the agent simply retries.
type NonceGuard interface {
	// CheckAndStore records (agentUID, nonce) as consumed at now, keeping it
	// for retain. It returns ErrReplayedNonce if the pair was already consumed
	// within that window.
	CheckAndStore(ctx context.Context, agentUID, nonce string, now time.Time, retain time.Duration) error
}

// NonceCache is the in-memory NonceGuard: entries older than the retention
// window are pruned lazily on insert. Process-local — it defends a single
// instance, which is why the server uses the shared store instead. Kept as the
// no-database implementation (tests, and any embedding without a nonce table).
type NonceCache struct {
	mu       sync.Mutex
	seen     map[string]time.Time
	gcEvery  int
	inserts  int
	maxEntry int
}

// NewNonceCache creates an in-memory nonce guard.
func NewNonceCache() *NonceCache {
	return &NonceCache{
		seen:     make(map[string]time.Time),
		gcEvery:  256,
		maxEntry: 100000,
	}
}

// CheckAndStore records (agentUID, nonce). It returns ErrReplayedNonce if the
// pair was already seen within the retention window, otherwise nil.
func (c *NonceCache) CheckAndStore(
	_ context.Context, agentUID, nonce string, now time.Time, retain time.Duration,
) error {
	key := agentUID + "\x00" + nonce

	c.mu.Lock()
	defer c.mu.Unlock()

	c.maybeGC(now, retain)

	if ts, ok := c.seen[key]; ok && now.Sub(ts) <= retain {
		return ErrReplayedNonce
	}

	c.seen[key] = now
	c.inserts++

	return nil
}

// maybeGC prunes expired entries periodically (every gcEvery inserts) to bound
// memory. Also prunes when the map exceeds the hard cap.
func (c *NonceCache) maybeGC(now time.Time, retain time.Duration) {
	if c.inserts%c.gcEvery != 0 && len(c.seen) < c.maxEntry {
		return
	}

	for k, ts := range c.seen {
		if now.Sub(ts) > retain {
			delete(c.seen, k)
		}
	}
}

// Len reports the current number of cached nonces (for tests/metrics).
func (c *NonceCache) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()

	return len(c.seen)
}

// NonceStore is the persistence half of the database-backed guard. It is
// declared structurally (rather than importing internal/db) to keep this
// low-level protocol package free of database dependencies; db.Service
// satisfies it.
type NonceStore interface {
	CheckAndStoreAgentNonce(
		ctx context.Context, agentUID, nonce string, now time.Time, retain time.Duration,
	) error
}

// StoredNonceGuard is the shared, cluster-wide NonceGuard: it delegates to the
// `agent_nonces` table, so every API replica sees the same consumed nonces.
type StoredNonceGuard struct {
	store NonceStore
	// replayed is the store's replay sentinel, normalized to ErrReplayedNonce
	// so callers only ever match one error regardless of the guard in use.
	replayed error
}

// NewStoredNonceGuard wraps a nonce store (db.Service) as a NonceGuard,
// translating its replay sentinel into ErrReplayedNonce.
func NewStoredNonceGuard(store NonceStore, replayed error) *StoredNonceGuard {
	return &StoredNonceGuard{store: store, replayed: replayed}
}

// CheckAndStore delegates to the shared store.
func (g *StoredNonceGuard) CheckAndStore(
	ctx context.Context, agentUID, nonce string, now time.Time, retain time.Duration,
) error {
	err := g.store.CheckAndStoreAgentNonce(ctx, agentUID, nonce, now, retain)
	if err != nil && g.replayed != nil && errors.Is(err, g.replayed) {
		return ErrReplayedNonce
	}

	return err
}
