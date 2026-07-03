package realtime

import (
	"errors"
	"log/slog"
	"sort"
	"sync"

	"github.com/fclairamb/solidping/server/internal/notifier"
	"github.com/fclairamb/solidping/server/internal/prommetrics"
)

// ErrTooManyConnections is returned by Subscribe when the per-instance
// connection guard is reached.
var ErrTooManyConnections = errors.New("too many realtime connections")

// ErrHubClosed is returned by Subscribe once the hub has shut down.
var ErrHubClosed = errors.New("realtime hub is closed")

// ErrTooManySubscriptions is returned by AddScope when a connection's
// subscription cap (SP_REALTIME_MAX_SUBSCRIPTIONS_PER_CONNECTION) is reached.
var ErrTooManySubscriptions = errors.New("too many realtime subscriptions")

// Entity identifies the kind of thing a v2 scope subscribes to: either the
// org-wide collection ("checks", "incidents", "events", "jobs") or a single
// check ("check", paired with its uid).
type Entity string

// Entities. "check" is the only per-uid scope in v2; the rest are
// org-collection scopes (the org is implied by the connection).
const (
	EntityCheck     Entity = "check"
	EntityChecks    Entity = "checks"
	EntityIncidents Entity = "incidents"
	EntityEvents    Entity = "events"
	EntityJobs      Entity = "jobs"
)

// Scope is one client subscription: an entity, plus a uid for the per-check
// entity (empty for collection scopes).
type Scope struct {
	Entity Entity
	UID    string
}

// ScopedUpdate is one drained dirty scope plus the kinds that changed within
// it, ready to become an `update` frame.
type ScopedUpdate struct {
	Scope Scope
	Kinds []Kind
}

// Hub is the per-process registry of realtime subscribers, keyed by org uid.
// It holds the process's single subscription to the org.events bus channel —
// client connections never touch PostgreSQL — and forwards incoming hints to
// matching subscribers based on their subscribed scopes. Per-subscriber
// delivery state is a dirty scope->kind-set map, not a queue: slow clients
// coalesce naturally and memory stays bounded.
type Hub struct {
	logger           *slog.Logger
	maxConns         int
	maxSubscriptions int

	mu     sync.Mutex
	subs   map[string]map[*Subscriber]struct{} // org uid -> subscribers
	total  int
	closed bool

	done      chan struct{}
	closeOnce sync.Once
	wg        sync.WaitGroup
}

// NewHub creates the hub and subscribes to the org.events channel exactly
// once on the process-wide notifier. If the notifier surfaces transport
// reconnects (PgEventNotifier), a reconnect broadcasts a resync to every
// local subscriber — anything NOTIFYed during the gap is lost by design and
// recovered by the client's full refetch. maxSubscriptions <= 0 means
// unlimited per-connection scopes.
func NewHub(bus notifier.EventNotifier, maxConns int, logger *slog.Logger) *Hub {
	return NewHubWithSubscriptionCap(bus, maxConns, 0, logger)
}

// NewHubWithSubscriptionCap is NewHub with an explicit per-connection
// subscription cap (SP_REALTIME_MAX_SUBSCRIPTIONS_PER_CONNECTION).
func NewHubWithSubscriptionCap(bus notifier.EventNotifier, maxConns, maxSubscriptions int, logger *slog.Logger) *Hub {
	if logger == nil {
		logger = slog.Default()
	}

	hub := &Hub{
		logger:           logger.With("component", "realtime_hub"),
		maxConns:         maxConns,
		maxSubscriptions: maxSubscriptions,
		subs:             make(map[string]map[*Subscriber]struct{}),
		done:             make(chan struct{}),
	}

	events := bus.Listen(ChannelOrgEvents)

	var reconnects <-chan struct{}
	if rn, ok := bus.(notifier.ReconnectNotifier); ok {
		reconnects = rn.ReconnectEvents()
	}

	hub.wg.Add(1)
	go hub.run(events, reconnects)

	return hub
}

// Subscribe registers a new connection for the org. It starts with zero
// scopes — the default-silent guarantee: a connection receives nothing until
// it adds a scope via AddScope.
func (h *Hub) Subscribe(orgUID string) (*Subscriber, error) {
	h.mu.Lock()
	defer h.mu.Unlock()

	if h.closed {
		return nil, ErrHubClosed
	}
	if h.maxConns > 0 && h.total >= h.maxConns {
		return nil, ErrTooManyConnections
	}

	sub := &Subscriber{
		org:           orgUID,
		maxScopes:     h.maxSubscriptions,
		subscriptions: make(map[Scope]struct{}),
		dirty:         make(map[Scope]map[Kind]struct{}),
		signal:        make(chan struct{}, 1),
	}

	orgSubs, ok := h.subs[orgUID]
	if !ok {
		orgSubs = make(map[*Subscriber]struct{})
		h.subs[orgUID] = orgSubs
	}
	orgSubs[sub] = struct{}{}
	h.total++
	prommetrics.RealtimeConnections.Set(float64(h.total))

	return sub, nil
}

// Unsubscribe removes the subscriber and wakes its consumer so a blocked
// connection handler observes the closed state and returns.
func (h *Hub) Unsubscribe(sub *Subscriber) {
	if sub == nil {
		return
	}

	h.mu.Lock()
	if orgSubs, ok := h.subs[sub.org]; ok {
		if _, present := orgSubs[sub]; present {
			delete(orgSubs, sub)
			if len(orgSubs) == 0 {
				delete(h.subs, sub.org)
			}
			h.total--
			prommetrics.RealtimeConnections.Set(float64(h.total))
		}
	}
	h.mu.Unlock()

	sub.markClosed()
}

// ConnectionCount returns the number of currently registered subscribers.
func (h *Hub) ConnectionCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()

	return h.total
}

// Close terminates the dispatch loop and closes every subscriber so open
// connections drain promptly (wired into server shutdown ahead of http
// shutdown).
func (h *Hub) Close() {
	h.closeOnce.Do(func() {
		close(h.done)
		h.wg.Wait()

		h.mu.Lock()
		h.closed = true
		all := make([]*Subscriber, 0, h.total)
		for _, orgSubs := range h.subs {
			for sub := range orgSubs {
				all = append(all, sub)
			}
		}
		h.subs = make(map[string]map[*Subscriber]struct{})
		h.total = 0
		prommetrics.RealtimeConnections.Set(0)
		h.mu.Unlock()

		for _, sub := range all {
			sub.markClosed()
		}
	})
}

// run forwards bus events and transport reconnects to local subscribers.
// A nil reconnects channel (LocalEventNotifier) simply never fires.
func (h *Hub) run(events <-chan string, reconnects <-chan struct{}) {
	defer h.wg.Done()

	for {
		select {
		case payload, ok := <-events:
			if !ok {
				return
			}
			h.dispatch(payload)
		case _, ok := <-reconnects:
			if !ok {
				reconnects = nil

				continue
			}
			h.broadcastResync()
		case <-h.done:
			return
		}
	}
}

// collectionEntityForKind maps a hint kind to the collection entity that
// watches it. Not every kind has a collection consumer (there is none today
// that doesn't), but the mapping is explicit so a future kind must be a
// deliberate addition here.
//
//nolint:gochecknoglobals // constant lookup map, not mutated after init
var collectionEntityForKind = map[Kind]Entity{
	KindResults:   EntityChecks, // results feed the "checks" collection (dashboard rollups)
	KindChecks:    EntityChecks,
	KindIncidents: EntityIncidents,
	KindEvents:    EntityEvents,
	KindJobs:      EntityJobs,
}

// checkAttributableKinds are the kinds that also apply to a specific check
// and therefore route to `check`-scoped subscribers via Hint.CheckUids.
//
//nolint:gochecknoglobals // constant lookup set, not mutated after init
var checkAttributableKinds = map[Kind]struct{}{
	KindResults:   {},
	KindChecks:    {},
	KindIncidents: {},
}

// dispatch decodes a bus payload and merges its kinds into every matching
// subscriber's dirty state: collection subscribers whose entity matches a
// hint kind, and `check` subscribers whose uid is in the hint's CheckUids (or
// every `check` subscriber of the org when CheckUids is the ["*"] wildcard).
// A subscriber with zero scopes never matches — the default-silent guarantee.
func (h *Hub) dispatch(payload string) {
	hint, err := DecodeHint(payload)
	if err != nil {
		h.logger.Warn("dropping malformed realtime hint", "error", err)

		return
	}

	kinds := make([]Kind, 0, len(hint.Kinds))
	for _, name := range hint.Kinds {
		kinds = append(kinds, Kind(name))
	}

	wildcard := IsWildcardCheckUids(hint.CheckUids)
	checkUIDSet := make(map[string]struct{}, len(hint.CheckUids))
	if !wildcard {
		for _, uid := range hint.CheckUids {
			checkUIDSet[uid] = struct{}{}
		}
	}

	h.mu.Lock()
	targets := make([]*Subscriber, 0, len(h.subs[hint.Org]))
	for sub := range h.subs[hint.Org] {
		targets = append(targets, sub)
	}
	h.mu.Unlock()

	delivered := 0
	for _, sub := range targets {
		if sub.offerHint(kinds, wildcard, checkUIDSet) {
			delivered++
		}
	}
	if delivered > 0 {
		prommetrics.RealtimeHintsDelivered.Add(float64(delivered))
	}
}

// broadcastResync marks every local subscriber for resync after a bus
// transport gap.
func (h *Hub) broadcastResync() {
	h.mu.Lock()
	targets := make([]*Subscriber, 0, h.total)
	for _, orgSubs := range h.subs {
		for sub := range orgSubs {
			targets = append(targets, sub)
		}
	}
	h.mu.Unlock()

	h.logger.Info("bus reconnected — broadcasting resync to local realtime subscribers",
		"subscribers", len(targets))

	for _, sub := range targets {
		sub.markResync()
	}
}

// Subscriber is one connection's delivery state: the set of scopes it has
// subscribed to, a per-scope dirty kind-set, plus resync/closed flags, with a
// 1-buffered wake-up channel. No queue, no backpressure — a slow consumer
// coalesces into pending state per scope.
type Subscriber struct {
	org       string
	maxScopes int // <=0 means unlimited

	mu            sync.Mutex
	subscriptions map[Scope]struct{}
	dirty         map[Scope]map[Kind]struct{}
	resync        bool
	closed        bool

	signal chan struct{} // buffered (1): wake-up, never data
}

// Org returns the org uid this subscriber is registered for.
func (s *Subscriber) Org() string { return s.org }

// Signal returns the wake-up channel. After a receive, call Take to drain the
// pending state.
func (s *Subscriber) Signal() <-chan struct{} { return s.signal }

// AddScope subscribes the connection to a scope, enforcing the per-connection
// subscription cap. Re-adding an already-subscribed scope is idempotent (not
// an error) — replay-on-reconnect relies on this.
func (s *Subscriber) AddScope(scope Scope) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, already := s.subscriptions[scope]; already {
		return nil
	}
	if s.maxScopes > 0 && len(s.subscriptions) >= s.maxScopes {
		return ErrTooManySubscriptions
	}
	s.subscriptions[scope] = struct{}{}

	return nil
}

// RemoveScope unsubscribes the connection from a scope. Removing a scope that
// was never subscribed is a no-op.
func (s *Subscriber) RemoveScope(scope Scope) {
	s.mu.Lock()
	delete(s.subscriptions, scope)
	delete(s.dirty, scope)
	s.mu.Unlock()
}

// ScopeCount returns the number of scopes currently subscribed.
func (s *Subscriber) ScopeCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()

	return len(s.subscriptions)
}

// HasScope reports whether the connection is currently subscribed to scope.
func (s *Subscriber) HasScope(scope Scope) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	_, ok := s.subscriptions[scope]

	return ok
}

// Take atomically drains and returns the pending delivery state: the dirty
// scopes (each with its sorted kinds, sorted by entity then uid), whether a
// resync is required, and whether the subscriber has been closed.
func (s *Subscriber) Take() ([]ScopedUpdate, bool, bool) {
	s.mu.Lock()
	updates := make([]ScopedUpdate, 0, len(s.dirty))
	for scope, kindSet := range s.dirty {
		kinds := make([]Kind, 0, len(kindSet))
		for k := range kindSet {
			kinds = append(kinds, k)
		}
		sort.Slice(kinds, func(i, j int) bool { return kinds[i] < kinds[j] })
		updates = append(updates, ScopedUpdate{Scope: scope, Kinds: kinds})
	}
	s.dirty = make(map[Scope]map[Kind]struct{})
	resync := s.resync
	s.resync = false
	closed := s.closed
	s.mu.Unlock()

	sort.Slice(updates, func(i, j int) bool {
		if updates[i].Scope.Entity != updates[j].Scope.Entity {
			return updates[i].Scope.Entity < updates[j].Scope.Entity
		}

		return updates[i].Scope.UID < updates[j].Scope.UID
	})

	return updates, resync, closed
}

// offerHint routes a decoded hint's kinds to this subscriber's matching
// scopes: each kind's collection entity (if subscribed) and, for
// check-attributable kinds, the `check` scope of any uid in checkUIDs (or
// every subscribed `check` scope when wildcard is set). Returns true if
// anything was offered (used for the delivered-count metric).
func (s *Subscriber) offerHint(kinds []Kind, wildcard bool, checkUIDs map[string]struct{}) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	if len(s.subscriptions) == 0 {
		return false // default-silent: no scopes, no delivery.
	}

	offered := false
	for _, kind := range kinds {
		if entity, ok := collectionEntityForKind[kind]; ok {
			scope := Scope{Entity: entity}
			if _, subscribed := s.subscriptions[scope]; subscribed {
				s.addDirty(scope, kind)
				offered = true
			}
		}

		if _, attributable := checkAttributableKinds[kind]; !attributable {
			continue
		}

		for scope := range s.subscriptions {
			if scope.Entity != EntityCheck {
				continue
			}
			if wildcard {
				s.addDirty(scope, kind)
				offered = true

				continue
			}
			if _, matches := checkUIDs[scope.UID]; matches {
				s.addDirty(scope, kind)
				offered = true
			}
		}
	}

	if offered {
		s.wakeLocked()
	}

	return offered
}

// addDirty merges kind into scope's pending dirty set. Caller holds s.mu.
func (s *Subscriber) addDirty(scope Scope, kind Kind) {
	set, ok := s.dirty[scope]
	if !ok {
		set = make(map[Kind]struct{})
		s.dirty[scope] = set
	}
	set[kind] = struct{}{}
}

// markResync flags the subscriber for a full resync and wakes the consumer.
func (s *Subscriber) markResync() {
	s.mu.Lock()
	s.resync = true
	s.mu.Unlock()

	s.wake()
}

// markClosed flags the subscriber as terminated and wakes the consumer.
func (s *Subscriber) markClosed() {
	s.mu.Lock()
	s.closed = true
	s.mu.Unlock()

	s.wake()
}

// wake signals the consumer without blocking; a pending signal coalesces.
func (s *Subscriber) wake() {
	select {
	case s.signal <- struct{}{}:
	default:
	}
}

// wakeLocked is wake for callers already holding s.mu.
func (s *Subscriber) wakeLocked() {
	select {
	case s.signal <- struct{}{}:
	default:
	}
}
