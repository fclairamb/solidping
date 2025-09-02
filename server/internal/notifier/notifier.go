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
