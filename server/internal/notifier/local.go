// Package notifier provides check notification implementations for different database backends.
package notifier

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

// LocalEventNotifier uses in-memory channels for notifications.
// This implementation works for any database (SQLite, PostgreSQL, etc.)
// and is particularly useful for single-process deployments.
type LocalEventNotifier struct {
	listeners map[string][]chan string
	mu        sync.RWMutex
	closed    bool
	closeOnce sync.Once
	logger    *slog.Logger
	// lastGrowthWarn rate-limits the abnormal-listener-growth warning emitted by
	// Listen. Guarded by mu (written under the write lock in Listen).
	lastGrowthWarn time.Time
}

// NewLocalEventNotifier creates a new channel-based notifier.
func NewLocalEventNotifier() *LocalEventNotifier {
	logger := slog.Default().With("component", "event_notifier")
	return &LocalEventNotifier{
		listeners: make(map[string][]chan string),
		logger:    logger,
	}
}

// Notify sends a notification to all listeners of the specified event type.
// Uses non-blocking send to prevent API slowdown if channels are full.
func (n *LocalEventNotifier) Notify(ctx context.Context, eventType string, payload string) error {
	n.mu.RLock()
	defer n.mu.RUnlock()

	n.logger.DebugContext(
		ctx,
		"Sending notification",
		slog.String("event_type", eventType),
		slog.String("payload", payload),
	)

	if n.closed {
		return nil
	}

	listeners, ok := n.listeners[eventType]
	if !ok {
		return nil
	}

	for _, ch := range listeners {
		select {
		case ch <- payload:
			// Notification sent successfully
		default:
			// Channel full, skip
			// Consumer will pick up on next cycle
		}
	}
	return nil
}

// Listen returns a channel that receives payloads for events of the specified type.
func (n *LocalEventNotifier) Listen(eventType string) <-chan string {
	n.mu.Lock()
	defer n.mu.Unlock()

	ch := make(chan string, ListenerBuffer) // buffered: see ListenerBuffer
	n.listeners[eventType] = append(n.listeners[eventType], ch)
	n.warnIfGrowingLocked(eventType)
	return ch
}

// warnIfGrowingLocked emits a rate-limited warning when a single event type's
// listener slice grows past listenerGrowthWarnThreshold — the signature of a
// consumer that Listens without ever Unlistening. Must be called with n.mu held
// for writing (it reads/writes lastGrowthWarn).
func (n *LocalEventNotifier) warnIfGrowingLocked(eventType string) {
	count := len(n.listeners[eventType])
	if count < listenerGrowthWarnThreshold {
		return
	}
	if now := time.Now(); now.Sub(n.lastGrowthWarn) >= listenerGrowthWarnInterval {
		n.lastGrowthWarn = now
		n.logger.Warn("event-notifier listener slice growing abnormally; possible listener leak",
			slog.String("event_type", eventType),
			slog.Int("count", count))
	}
}

// Unlisten deregisters a channel previously returned by Listen. See the
// EventNotifier interface for the contract. Deregister-only: the channel is not
// closed here (Close closes every remaining channel), which avoids a
// double-close race. Unknown channels are a no-op.
func (n *LocalEventNotifier) Unlisten(eventType string, target <-chan string) {
	n.mu.Lock()
	defer n.mu.Unlock()

	channels := n.listeners[eventType]
	for i, c := range channels {
		if c == target {
			last := len(channels) - 1
			channels[i] = channels[last]
			channels[last] = nil // release the reference for GC
			n.listeners[eventType] = channels[:last]
			return
		}
	}
}

// ListenerCount returns the total number of registered listener channels across
// all event types. Each Listen call appends a channel; a listener that is never
// drained/deregistered leaks both the channel and its goroutine, so this count
// is the memory-analysis signal for the event-listener hypothesis.
func (n *LocalEventNotifier) ListenerCount() int {
	n.mu.RLock()
	defer n.mu.RUnlock()
	total := 0
	for _, channels := range n.listeners {
		total += len(channels)
	}
	return total
}

// Close closes all notification channels.
func (n *LocalEventNotifier) Close() error {
	n.closeOnce.Do(func() {
		n.mu.Lock()
		defer n.mu.Unlock()
		n.closed = true
		for _, channels := range n.listeners {
			for _, ch := range channels {
				close(ch)
			}
		}
	})
	return nil
}
