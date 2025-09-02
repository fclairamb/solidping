package notifier

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/lib/pq"
	"github.com/uptrace/bun"
)

// PgEventNotifier uses PostgreSQL LISTEN/NOTIFY for notifications.
// This provides optimal performance for PostgreSQL databases by using
// native database pub/sub instead of polling.
type PgEventNotifier struct {
	db        *bun.DB
	listener  *pq.Listener
	listeners map[string][]chan string // eventType -> channels
	mu        sync.RWMutex
	done      chan struct{}
	closeOnce sync.Once
	wg        sync.WaitGroup
	logger    *slog.Logger
}

// NewPgEventNotifier creates a new PostgreSQL LISTEN/NOTIFY based notifier.
// connString should be a PostgreSQL connection string (DSN).
func NewPgEventNotifier(db *bun.DB, connString string, logger *slog.Logger) (*PgEventNotifier, error) {
	if logger == nil {
		logger = slog.Default()
	}

	n := &PgEventNotifier{
		db:        db,
		listeners: make(map[string][]chan string),
		done:      make(chan struct{}),
		logger:    logger,
	}

	// Set up PostgreSQL listener with automatic reconnection
	n.listener = pq.NewListener(
		connString,
		10*time.Second, // minReconnectInterval
		time.Minute,    // maxReconnectInterval
		func(ev pq.ListenerEventType, err error) {
			switch ev {
			case pq.ListenerEventConnected:
				logger.Debug("postgres listener connected")
			case pq.ListenerEventDisconnected:
				logger.Warn("postgres listener disconnected", "error", err)
			case pq.ListenerEventReconnected:
				logger.Info("postgres listener reconnected")
			case pq.ListenerEventConnectionAttemptFailed:
				logger.Error("postgres listener connection attempt failed", "error", err)
			}
		},
	)

	// Start goroutine to forward postgres notifications to our channels
	n.wg.Add(1)
	go n.listenLoop()

	return n, nil
}

// channelName converts an event type to a PostgreSQL channel name.
// Dots are replaced with underscores since PostgreSQL identifiers can't contain dots.
func (n *PgEventNotifier) channelName(eventType string) string {
	return strings.ReplaceAll(eventType, ".", "_")
}

// eventTypeFromChannel converts a PostgreSQL channel name back to an event type.
func (n *PgEventNotifier) eventTypeFromChannel(channelName string) string {
	return strings.ReplaceAll(channelName, "_", ".")
}

// listenLoop forwards notifications from PostgreSQL to the internal channels.
func (n *PgEventNotifier) listenLoop() {
	defer n.wg.Done()

	ticker := time.NewTicker(90 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case notification := <-n.listener.Notify:
			if notification != nil {
				n.logger.Debug("received postgres notification",
					"channel", notification.Channel,
					"payload", notification.Extra)

				eventType := n.eventTypeFromChannel(notification.Channel)
				payload := notification.Extra

				// Forward to our notification channels (non-blocking)
				n.mu.RLock()
				listeners, ok := n.listeners[eventType]
				n.mu.RUnlock()

				if ok {
					for _, ch := range listeners {
						select {
						case ch <- payload:
						default:
							// Channel full, skip (consumer will catch up)
						}
					}
				}
			}
		case <-ticker.C:
			// Periodic ping to keep connection alive and detect issues early
			go func() {
				if err := n.listener.Ping(); err != nil {
					n.logger.Warn("postgres listener ping failed", "error", err)
				}
			}()
		case <-n.done:
			// Shutdown signal received, exit the loop
			return
		}
	}
}

// Notify sends a NOTIFY signal to PostgreSQL with the payload.
// This is a best-effort operation - errors are logged but not propagated.
func (n *PgEventNotifier) Notify(ctx context.Context, eventType string, payload string) error {
	channelName := n.channelName(eventType)
	query := fmt.Sprintf("NOTIFY %s, %s", channelName, pq.QuoteLiteral(payload))
	_, err := n.db.ExecContext(ctx, query)
	if err != nil {
		// Log but don't return error - notifications are best-effort
		n.logger.DebugContext(ctx, "failed to send NOTIFY",
			"error", err,
			"channel", channelName,
			"eventType", eventType)
	}
	return nil
}

// Listen returns a channel that receives payloads for events of the specified type.
// If this is the first listener for this event type, it subscribes to the PostgreSQL channel.
func (n *PgEventNotifier) Listen(eventType string) <-chan string {
	n.mu.Lock()
	defer n.mu.Unlock()

	channelName := n.channelName(eventType)

	// Subscribe to PostgreSQL channel if first listener for this event type
	if _, exists := n.listeners[eventType]; !exists {
		if err := n.listener.Listen(channelName); err != nil {
			n.logger.Error("failed to listen on channel",
				"channel", channelName,
				"eventType", eventType,
				"error", err)
		}
		n.listeners[eventType] = []chan string{}
	}

	ch := make(chan string, 1) // Buffered to avoid blocking
	n.listeners[eventType] = append(n.listeners[eventType], ch)
	return ch
}

// Close releases resources used by the PostgreSQL listener.
func (n *PgEventNotifier) Close() error {
	var err error
	n.closeOnce.Do(func() {
		// Signal the listenLoop to stop
		close(n.done)

		// Wait for listenLoop to finish
		n.wg.Wait()

		// Now safe to close listener and notification channels
		if n.listener != nil {
			err = n.listener.Close()
		}

		n.mu.Lock()
		defer n.mu.Unlock()
		for _, channels := range n.listeners {
			for _, ch := range channels {
				close(ch)
			}
		}
	})
	return err
}
