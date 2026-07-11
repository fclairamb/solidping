package notifier

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
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
	// lastGrowthWarn rate-limits the abnormal-listener-growth warning emitted by
	// Listen. Guarded by mu (written under the write lock in Listen).
	lastGrowthWarn time.Time
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

	// pingInFlight guards the keepalive ping so ticks cannot stack an unbounded
	// pile of goroutines when the listener pipeline stalls (see the ticker case).
	var pingInFlight atomic.Bool

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
			// Periodic ping to keep connection alive and detect issues early.
			// Non-stacking: pq.Listener.Ping serializes on the listener mutex, so
			// if the pipeline is wedged the ping blocks indefinitely. Only spawn a
			// new ping when the previous one has returned; otherwise skip the tick
			// and surface the stall as a rate-limited warning. This bounds the
			// ping goroutines to at most one, instead of the one-per-tick pile-up
			// (364 stuck goroutines in 9h) seen during the 2026-07-11 incident.
			if pingInFlight.CompareAndSwap(false, true) {
				go func() {
					defer pingInFlight.Store(false)
					if err := n.listener.Ping(); err != nil {
						n.logger.Warn("postgres listener ping failed", "error", err)
					}
				}()
			} else {
				n.logger.Warn("postgres listener ping still in flight; skipping tick (listener pipeline may be stalled)")
			}
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
	n.warnIfGrowingLocked(eventType)
	return ch
}

// warnIfGrowingLocked emits a rate-limited warning when a single event type's
// listener slice grows past listenerGrowthWarnThreshold — the signature of a
// consumer that Listens without ever Unlistening. Must be called with n.mu held
// for writing (it reads/writes lastGrowthWarn).
func (n *PgEventNotifier) warnIfGrowingLocked(eventType string) {
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
// double-close race. The underlying pq.Listener LISTEN is intentionally left in
// place — the set of event types is small and fixed, so re-subscribing is not
// worth the churn. Unknown channels are a no-op.
func (n *PgEventNotifier) Unlisten(eventType string, target <-chan string) {
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
