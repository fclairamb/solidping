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

// Hub is the per-process registry of realtime stream subscribers, keyed by
// org uid. It holds the process's single subscription to the org.events bus
// channel — client streams never touch PostgreSQL — and forwards incoming
// hints to matching subscribers. Per-subscriber delivery state is a dirty
// kind-set, not a queue: slow clients coalesce naturally and memory stays
// bounded.
type Hub struct {
	logger   *slog.Logger
	maxConns int

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
// recovered by the client's full refetch.
func NewHub(bus notifier.EventNotifier, maxConns int, logger *slog.Logger) *Hub {
	if logger == nil {
		logger = slog.Default()
	}

	hub := &Hub{
		logger:   logger.With("component", "realtime_hub"),
		maxConns: maxConns,
		subs:     make(map[string]map[*Subscriber]struct{}),
		done:     make(chan struct{}),
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

// Subscribe registers a new stream subscriber for the org.
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
		org:    orgUID,
		dirty:  make(map[Kind]struct{}),
		signal: make(chan struct{}, 1),
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
// stream handler observes the closed state and returns.
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
// streams drain promptly (wired into server shutdown ahead of http shutdown).
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

// dispatch decodes a bus payload and merges its kinds into every matching
// subscriber's dirty set.
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

	h.mu.Lock()
	targets := make([]*Subscriber, 0, len(h.subs[hint.Org]))
	for sub := range h.subs[hint.Org] {
		targets = append(targets, sub)
	}
	h.mu.Unlock()

	for _, sub := range targets {
		sub.offer(kinds)
	}
	if len(targets) > 0 {
		prommetrics.RealtimeHintsDelivered.Add(float64(len(targets)))
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

// Subscriber is one stream connection's delivery state: a dirty kind-set plus
// resync/closed flags, with a 1-buffered wake-up channel. No queue, no
// backpressure — a slow consumer coalesces into a single pending state.
type Subscriber struct {
	org string

	mu     sync.Mutex
	dirty  map[Kind]struct{}
	resync bool
	closed bool

	signal chan struct{} // buffered (1): wake-up, never data
}

// Org returns the org uid this subscriber is registered for.
func (s *Subscriber) Org() string { return s.org }

// Signal returns the wake-up channel. After a receive, call Take to drain the
// pending state.
func (s *Subscriber) Signal() <-chan struct{} { return s.signal }

// Take atomically drains and returns the pending delivery state: the dirty
// kinds (sorted), whether a resync is required, and whether the subscriber
// has been closed.
func (s *Subscriber) Take() ([]Kind, bool, bool) {
	s.mu.Lock()
	kinds := make([]Kind, 0, len(s.dirty))
	for k := range s.dirty {
		kinds = append(kinds, k)
	}
	s.dirty = make(map[Kind]struct{})
	resync := s.resync
	s.resync = false
	closed := s.closed
	s.mu.Unlock()

	sort.Slice(kinds, func(i, j int) bool { return kinds[i] < kinds[j] })

	return kinds, resync, closed
}

// offer merges kinds into the dirty set and wakes the consumer.
func (s *Subscriber) offer(kinds []Kind) {
	s.mu.Lock()
	for _, k := range kinds {
		s.dirty[k] = struct{}{}
	}
	s.mu.Unlock()

	s.wake()
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
