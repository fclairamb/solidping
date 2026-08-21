// Package capturecache is the deported agent's short-lived, bounded holding
// area for binary probe captures (today: the PNG a failing browser check took).
//
// It exists because of a hard protocol constraint: the agent WebSocket is a
// JSON control channel, so a megabyte PNG cannot ride the result frame — see
// checkerdef.Screenshot. The agent therefore keeps the bytes HERE and sends
// only a marker naming this cache's slot; if (and only if) that result opens or
// reopens an incident, the server asks for the upload and the agent POSTs the
// bytes out-of-band.
//
// Everything about it is deliberately small and lossy. The bytes are worth
// keeping for the second or two between "I submitted a failing result" and "the
// server decided that result was an incident onset", and worth nothing
// afterwards. So the cache is bounded by BOTH entry count and total bytes
// (either bound alone lets an adversarial or merely unlucky size distribution
// blow the budget), entries expire on a TTL, and a read REMOVES the entry: a
// capture is served at most once, which is what makes "an upload that failed is
// never retried forever" true by construction rather than by policy.
package capturecache

import (
	"crypto/rand"
	"encoding/hex"
	"sync"
	"time"
)

// Defaults sized for the real traffic: a browser check captures at most
// MaxScreenshotBytes (4 MiB) per failing run, and the window a capture has to
// survive is one result round-trip.
const (
	// DefaultMaxEntries is how many captures may be held at once.
	DefaultMaxEntries = 4
	// DefaultMaxBytes is the total byte budget across all held captures.
	DefaultMaxBytes = 8 * 1024 * 1024
	// DefaultTTL is how long an unclaimed capture survives. Generous relative
	// to the sub-second round-trip it has to cover, short enough that a
	// forgotten capture cannot outlive the incident it belonged to.
	DefaultTTL = 5 * time.Minute
)

// captureIDBytes is the entropy in a capture id. The id is not a secret (it
// names a slot in this process's own RAM and is echoed back by the server
// verbatim), but it must not collide between concurrent captures.
const captureIDBytes = 12

// entry is one held capture.
type entry struct {
	id       string
	png      []byte
	storedAt time.Time
}

// Cache is a bounded, TTL'd, take-once store of capture bytes. The zero value
// is not usable — build one with New.
type Cache struct {
	mu         sync.Mutex
	maxEntries int
	maxBytes   int
	ttl        time.Duration
	now        func() time.Time

	// order holds the live entries OLDEST FIRST; eviction always drops
	// order[0]. A slice is the right shape here because the cache holds a
	// handful of entries and never reorders on read (a read removes).
	order []*entry
	byID  map[string]*entry
	bytes int
}

// Option customizes a Cache at construction.
type Option func(*Cache)

// WithClock overrides the time source (tests drive the TTL with it).
func WithClock(now func() time.Time) Option {
	return func(c *Cache) {
		if now != nil {
			c.now = now
		}
	}
}

// New builds a cache. Non-positive bounds fall back to the defaults.
func New(maxEntries, maxBytes int, ttl time.Duration, opts ...Option) *Cache {
	cache := &Cache{
		maxEntries: maxEntries,
		maxBytes:   maxBytes,
		ttl:        ttl,
		now:        time.Now,
		byID:       map[string]*entry{},
	}

	if cache.maxEntries <= 0 {
		cache.maxEntries = DefaultMaxEntries
	}

	if cache.maxBytes <= 0 {
		cache.maxBytes = DefaultMaxBytes
	}

	if cache.ttl <= 0 {
		cache.ttl = DefaultTTL
	}

	for _, opt := range opts {
		opt(cache)
	}

	return cache
}

// NewDefault builds a cache with the default bounds.
func NewDefault() *Cache {
	return New(DefaultMaxEntries, DefaultMaxBytes, DefaultTTL)
}

// Put stores a capture and returns the id naming it, or "" when the capture
// was not stored at all.
//
// A blob larger than the WHOLE byte budget is refused rather than admitted:
// admitting it would evict every other capture and still leave the cache over
// budget. The caller's contract is "empty id means advertise nothing", so a
// refusal degrades to "this run produced no capture" — which is exactly what
// the marker fields already mean.
//
// The caller must not mutate png afterwards; ownership moves here.
func (c *Cache) Put(png []byte) string {
	if len(png) == 0 || len(png) > c.maxBytes {
		return ""
	}

	id, err := newCaptureID()
	if err != nil {
		return ""
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	now := c.now()
	c.expireLocked(now)

	item := &entry{id: id, png: png, storedAt: now}
	c.byID[id] = item
	c.order = append(c.order, item)
	c.bytes += len(png)

	c.evictLocked()

	return id
}

// Take returns a capture and REMOVES it. The second call for the same id
// always misses — a capture is uploadable exactly once.
func (c *Cache) Take(id string) ([]byte, bool) {
	if id == "" {
		return nil, false
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	c.expireLocked(c.now())

	item, ok := c.byID[id]
	if !ok {
		return nil, false
	}

	c.removeLocked(item)

	return item.png, true
}

// Len reports how many captures are held (expired ones excluded).
func (c *Cache) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.expireLocked(c.now())

	return len(c.order)
}

// Bytes reports the total bytes held (expired ones excluded).
func (c *Cache) Bytes() int {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.expireLocked(c.now())

	return c.bytes
}

// expireLocked drops every entry past its TTL. Caller holds the lock.
func (c *Cache) expireLocked(now time.Time) {
	for len(c.order) > 0 && now.Sub(c.order[0].storedAt) >= c.ttl {
		c.removeLocked(c.order[0])
	}
}

// evictLocked drops the OLDEST entries until both bounds hold again. Caller
// holds the lock.
func (c *Cache) evictLocked() {
	for len(c.order) > 0 && (len(c.order) > c.maxEntries || c.bytes > c.maxBytes) {
		c.removeLocked(c.order[0])
	}
}

// removeLocked unlinks one entry. Caller holds the lock.
func (c *Cache) removeLocked(item *entry) {
	delete(c.byID, item.id)

	for i, held := range c.order {
		if held == item {
			c.order = append(c.order[:i], c.order[i+1:]...)

			break
		}
	}

	c.bytes -= len(item.png)
}

// newCaptureID mints a random, collision-free capture id.
func newCaptureID() (string, error) {
	buf := make([]byte, captureIDBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", err //nolint:wrapcheck // the sole caller degrades to "no capture"
	}

	return hex.EncodeToString(buf), nil
}
