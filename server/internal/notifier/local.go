// Package notifier provides check notification implementations for different database backends.
package notifier

import (
	"context"
	"log/slog"
	"sync"
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

	ch := make(chan string, 1) // Buffered to avoid blocking
	n.listeners[eventType] = append(n.listeners[eventType], ch)
	return ch
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
