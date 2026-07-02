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
	// reconnectChans receive a signal whenever the pq.Listener re-establishes
	// its session after a drop (see ReconnectEvents / notifier.ReconnectNotifier).
	reconnectChans []chan struct{}
	mu             sync.RWMutex
	done           chan struct{}
	closeOnce      sync.Once
	wg             sync.WaitGroup
	logger         *slog.Logger
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
				// pq.Listener re-issues LISTEN for every registered channel on
				// its own; consumers only need to resync state they may have
				// missed while the session was down.
				n.signalReconnect()
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

	ch := make(chan string, ListenerBuffer) // buffered: see ListenerBuffer
	n.listeners[eventType] = append(n.listeners[eventType], ch)
	return ch
}

// ReconnectEvents registers and returns a channel signaled each time the
// underlying pq.Listener reconnects after a connection drop. Buffered (size 1)
// with non-blocking sends so reconnect bursts coalesce into a single pending
// signal. Closed by Close. Implements notifier.ReconnectNotifier.
func (n *PgEventNotifier) ReconnectEvents() <-chan struct{} {
	n.mu.Lock()
	defer n.mu.Unlock()

	ch := make(chan struct{}, 1)
	n.reconnectChans = append(n.reconnectChans, ch)

	return ch
}

// signalReconnect fans a reconnect signal out to every registered reconnect
// channel without blocking (a full channel already has a pending signal).
func (n *PgEventNotifier) signalReconnect() {
	n.mu.RLock()
	defer n.mu.RUnlock()

	for _, ch := range n.reconnectChans {
		select {
		case ch <- struct{}{}:
		default:
			// Signal already pending — reconnects coalesce.
		}
	}
}

// ListenerCount returns the total number of registered listener channels across
// all event types. Mirrors LocalEventNotifier.ListenerCount so the memory
// surface can report listener cardinality regardless of the DB backend.
func (n *PgEventNotifier) ListenerCount() int {
	n.mu.RLock()
	defer n.mu.RUnlock()
	total := 0
	for _, channels := range n.listeners {
		total += len(channels)
	}
	return total
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
		for _, ch := range n.reconnectChans {
			close(ch)
		}
		n.reconnectChans = nil
	})
	return err
}
