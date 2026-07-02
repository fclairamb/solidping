package notifier

import (
	"context"
	"log/slog"

	"github.com/uptrace/bun"
)

// EventNotifier provides a generic event notification system.
// It supports multiple event types with payload data, allowing checkrunners
// and other components to wake up immediately instead of polling.
type EventNotifier interface {
	// Notify sends an event of the specified type with a payload.
	// This is a best-effort notification - errors may be logged but not propagated.
	Notify(ctx context.Context, eventType string, payload string) error

	// Listen returns a channel that receives payloads for events of the specified type.
	// Consumers can select on this channel to wake up when events occur.
	Listen(eventType string) <-chan string

	// Close releases resources used by the notifier.
	Close() error
}

// ListenerCounter is implemented by notifiers that can report how many listener
// channels are currently registered. Both LocalEventNotifier and
// PgEventNotifier satisfy it; the memory surface uses a type assertion so the
// EventNotifier interface itself stays minimal.
type ListenerCounter interface {
	ListenerCount() int
}

// ReconnectNotifier is implemented by notifiers whose underlying transport can
// drop and re-establish its subscription (PgEventNotifier's pq.Listener).
// Consumers subscribe to reconnect events to trigger a resync after a
// potential notification gap: anything NOTIFYed while the session was down is
// lost, so hint consumers must assume they missed events. LocalEventNotifier
// intentionally does not implement it — in-process channels cannot gap.
type ReconnectNotifier interface {
	// ReconnectEvents returns a channel that receives a signal each time the
	// transport reconnects after a drop. The channel is buffered (size 1) and
	// signals are delivered non-blocking, so a burst of reconnects coalesces
	// into one pending signal. The channel is closed when the notifier closes.
	ReconnectEvents() <-chan struct{}
}

// ListenerCount returns n's registered listener-channel count, or 0 if n does
// not implement ListenerCounter.
func ListenerCount(n EventNotifier) int {
	if lc, ok := n.(ListenerCounter); ok {
		return lc.ListenerCount()
	}
	return 0
}

// New creates an EventNotifier appropriate for the database type.
// For PostgreSQL, it uses NOTIFY/LISTEN for optimal performance.
// For SQLite and other databases, it uses in-memory channels.
//
// Parameters:
//   - db: The database connection
//   - dbType: Database type ("postgres", "sqlite", etc.)
//   - connString: Connection string (required for PostgreSQL LISTEN)
//   - logger: Optional logger (uses slog.Default() if nil)
func New(db *bun.DB, dbType string, connString string, logger *slog.Logger) (EventNotifier, error) {
	if logger == nil {
		logger = slog.Default()
	}

	switch dbType {
	case "postgres", "postgres-embedded":
		return NewPgEventNotifier(db, connString, logger)
	default: // "sqlite", "sqlite-memory", or any other
		return NewLocalEventNotifier(), nil
	}
}
