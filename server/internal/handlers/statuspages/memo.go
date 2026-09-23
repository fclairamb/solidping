package statuspages

// Per-process memo of the computed public status-page view (spec 2026-09-22-09).
//
// # Why the computed struct and not the encoded bytes
//
// The expensive part of a public page read is the fan-out that BUILDS the
// answer — sections, up to 200 per-resource lookups, availability buckets, the
// response-time seam, incidents, updates. Encoding the result as JSON is
// milliseconds on top of that. Caching the struct rather than the bytes also
// lets the summary endpoint and the SVG badge share one entry, and lets the
// default-page route share the slug route's entry, because what they have in
// common is the computation, not the wire format.
//
// # Gate first, always
//
// Nothing in this file decides who may read a page. The memo is consulted only
// AFTER publicAccessError has returned nil for this request, on every request,
// and the computation it stores is the body of a page the caller was already
// entitled to see. Two rules keep that true, and both are asserted by tests:
//
//  1. No lookup, gate or authorization step may live inside a memoized
//     closure. What goes in the closure is pure computation from an
//     already-resolved, already-authorized page row.
//  2. A memo hit returns a body, never a decision. A caller that skipped the
//     gate cannot reach one, because the gate is the caller's own first
//     statement, not something the memo could be asked to replay.
//
// A `password` page's body is identical for everybody who typed the password,
// so sharing it in process is safe. What would not be safe is answering someone
// who never typed it, and the ordering above — not any property of this cache —
// is what prevents that.
//
// # What lives in a shared entry
//
// A memoized value is handed to every caller within the TTL, so callers must
// treat it as read-only. The top-level struct is returned by value (a caller's
// own field write is its own copy), but the slices inside it — Sections,
// ActiveIncidents, RecentUpdates — are shared. Deep-copying 3 MB per read would
// spend exactly what this memo exists to save, so the rule is "do not mutate a
// view you did not compute", and the request-derived fields that DO vary per
// caller (publicPageURL) are assembled in the handler, outside the memo.

import (
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"

	"github.com/fclairamb/solidping/server/internal/prommetrics"
	"github.com/fclairamb/solidping/server/internal/statuspagecache"
)

// memoProduct names which of the two memoized products a key addresses. They
// share one map — and therefore one TTL, one bound and one invalidation — so a
// write path cannot evict the page and leave its summary behind.
type memoProduct string

const (
	// memoProductPage is the full ViewStatusPage payload.
	memoProductPage memoProduct = "page"
	// memoProductSummary is the viewStatusPageSummary rollup, shared by the
	// summary endpoint and the SVG badge.
	memoProductSummary memoProduct = "summary"
)

// memoMaxEntries bounds the map. A very large public page is a few megabytes of
// computed view; 256 of them is an installation with hundreds of huge public
// pages, which is not a shape that exists. It is a blast-radius limit, not a
// tuning knob — hence a constant rather than a parameter.
const memoMaxEntries = 256

// memoKey identifies one memoized view. Comparable, so it is both the map key
// and (via flightKey) the singleflight key — the two cannot drift apart.
//
// orgUID is carried for InvalidateOrg alone: a write path that changed
// something org-wide but cannot name the page has to be able to evict, and
// scanning keys is cheaper than a second index over a map bounded at 256.
type memoKey struct {
	product      memoProduct
	orgUID       string
	pageUID      string
	availability bool
	responseTime bool
}

// flightKey is the singleflight key: the same identity as the map key, flattened
// to the string that API requires.
func (k memoKey) flightKey() string {
	return string(k.product) + "\x00" + k.pageUID + "\x00" +
		strconv.FormatBool(k.availability) + strconv.FormatBool(k.responseTime)
}

// pageViewKey is the key for a full page view under the given include set.
func pageViewKey(orgUID, pageUID string, opts ViewOptions) memoKey {
	return memoKey{
		product:      memoProductPage,
		orgUID:       orgUID,
		pageUID:      pageUID,
		availability: opts.Availability,
		responseTime: opts.ResponseTime,
	}
}

// summaryKey is the key for a summary rollup. withAvailability is the whole of
// the shape here — the summary has no response-time series — which is what lets
// the badge (withAvailability=false) and the JSON endpoint
// (withAvailability=true) coexist without one paying for the other's work.
func summaryKey(orgUID, pageUID string, withAvailability bool) memoKey {
	return memoKey{
		product:      memoProductSummary,
		orgUID:       orgUID,
		pageUID:      pageUID,
		availability: withAvailability,
	}
}

// memoEntry is a computed view plus the instant it was computed.
type memoEntry struct {
	value      any
	computedAt time.Time
}

// pageMemo is the TTL-bound map plus the singleflight group that collapses
// concurrent misses.
//
// A nil *pageMemo is usable and simply computes through: the MCP wiring and the
// tests that build a Service literal must keep working without knowing this
// exists.
type pageMemo struct {
	mu      sync.Mutex
	entries map[memoKey]memoEntry
	flight  singleflight.Group
	// now is the clock. Injectable so the expiry test can cross the TTL
	// without sleeping; nil means time.Now.
	now func() time.Time
}

// newPageMemo builds an empty memo on the real clock.
func newPageMemo() *pageMemo {
	return &pageMemo{entries: make(map[memoKey]memoEntry)}
}

// clock reads the memo's time source.
func (m *pageMemo) clock() time.Time {
	if m.now != nil {
		return m.now()
	}

	return time.Now()
}

// lookup returns a live entry's value. An expired entry is deleted on the way
// out — lazy eviction is enough because a key nobody reads again is reclaimed by
// the insert sweep instead.
func (m *pageMemo) lookup(key memoKey) (any, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()

	entry, found := m.entries[key]
	if !found {
		return nil, false
	}

	if m.clock().Sub(entry.computedAt) >= statuspagecache.PageMemoTTL {
		delete(m.entries, key)

		return nil, false
	}

	return entry.value, true
}

// store records a computed view, sweeping the oldest entries first when the map
// has outgrown its bound.
func (m *pageMemo) store(key memoKey, value any) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.entries == nil {
		m.entries = make(map[memoKey]memoEntry)
	}

	m.entries[key] = memoEntry{value: value, computedAt: m.clock()}

	if len(m.entries) > memoMaxEntries {
		m.sweepLocked()
	}
}

// sweepLocked drops expired entries first and then, if the map is still over
// its bound, the oldest ones until it fits. Caller holds mu.
func (m *pageMemo) sweepLocked() {
	now := m.clock()

	for key, entry := range m.entries {
		if now.Sub(entry.computedAt) >= statuspagecache.PageMemoTTL {
			delete(m.entries, key)
		}
	}

	if len(m.entries) <= memoMaxEntries {
		return
	}

	keys := make([]memoKey, 0, len(m.entries))
	for key := range m.entries {
		keys = append(keys, key)
	}

	sort.Slice(keys, func(i, j int) bool {
		return m.entries[keys[i]].computedAt.Before(m.entries[keys[j]].computedAt)
	})

	for _, key := range keys[:len(m.entries)-memoMaxEntries] {
		delete(m.entries, key)
	}
}

// invalidate drops every entry for one page — both products, every key variant.
// Evicting one shape and leaving another is how a page would keep serving an
// edited section to whoever asked with a different `include` set.
func (m *pageMemo) invalidate(pageUID string) {
	if m == nil || pageUID == "" {
		return
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	for key := range m.entries {
		if key.pageUID == pageUID {
			delete(m.entries, key)
		}
	}
}

// invalidateOrg drops every entry belonging to an organization. For the write
// path that knows something changed but cannot name the page it changed.
func (m *pageMemo) invalidateOrg(orgUID string) {
	if m == nil || orgUID == "" {
		return
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	for key := range m.entries {
		if key.orgUID == orgUID {
			delete(m.entries, key)
		}
	}
}

// size reports how many entries are held. Tests only.
func (m *pageMemo) size() int {
	if m == nil {
		return 0
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	return len(m.entries)
}

// memoDo returns the memoized view for key, computing it at most once across
// concurrent callers.
//
// Generic and free-standing rather than a method because Go methods cannot
// carry type parameters, and the alternative — one map and one code path per
// product — is how the page and its summary would end up with two different
// TTLs and two different invalidation lists.
//
// An erroring computation stores nothing. singleflight forgets its key once the
// call returns, so the error reaches everyone who waited on THAT call and the
// next caller retries from scratch; a failed page render is never cached, not
// even for an instant.
func memoDo[T any](memo *pageMemo, key memoKey, compute func() (T, error)) (T, error) {
	if memo == nil {
		return compute()
	}

	if cached, found := memo.lookup(key); found {
		if typed, ok := cached.(T); ok {
			prommetrics.RecordStatusPageMemoHit(string(key.product))

			return typed, nil
		}
	}

	prommetrics.RecordStatusPageMemoMiss(string(key.product))

	result, err, shared := memo.flight.Do(key.flightKey(), func() (any, error) {
		computed, computeErr := compute()
		if computeErr != nil {
			return nil, computeErr
		}

		memo.store(key, computed)

		return computed, nil
	})

	// shared is reported to every participant of a collapsed call, the leader
	// included, so this counts "callers that did not pay for their own
	// computation plus the one that paid for theirs" — which is the shape a
	// graph of an incident spike wants: it rises exactly when the collapsing is
	// doing work.
	if shared {
		prommetrics.RecordStatusPageMemoShared(string(key.product))
	}

	if err != nil {
		var zero T

		return zero, err
	}

	typed, ok := result.(T)
	if !ok {
		// Two products stored under one map cannot collide — the product is
		// part of the key — so this is unreachable rather than a fallback
		// worth logging. Recompute rather than return a zero value.
		return compute()
	}

	return typed, nil
}

// memoKeysForPage lists the live keys of one page. Tests only — it is how the
// key-variant test asserts two shapes are two entries and one shape reached
// through two routes is one.
func (m *pageMemo) memoKeysForPage(pageUID string) []string {
	if m == nil {
		return nil
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	keys := make([]string, 0, len(m.entries))

	for key := range m.entries {
		if key.pageUID == pageUID {
			keys = append(keys, key.flightKey())
		}
	}

	sort.Strings(keys)

	return keys
}

// memoDebugString renders the live keys, for a test failure message that says
// which shapes were held rather than only how many.
func (m *pageMemo) memoDebugString() string {
	if m == nil {
		return "<no memo>"
	}

	m.mu.Lock()
	keys := make([]string, 0, len(m.entries))

	for key := range m.entries {
		keys = append(keys, key.flightKey())
	}
	m.mu.Unlock()

	sort.Strings(keys)

	return strings.Join(keys, " | ")
}
