package notifier

import (
	"fmt"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/postgres"
)

//nolint:paralleltest // Test uses shared database connection
func TestPgEventNotifier(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping PostgreSQL notifier test in short mode")
	}

	// Create embedded PostgreSQL instance
	tempDir, err := os.MkdirTemp("", "pg-notifier-test-*")
	require.NoError(t, err)

	t.Cleanup(func() {
		_ = os.RemoveAll(tempDir)
	})

	ctx := t.Context()
	port := uint32(15436)
	svc, err := postgres.NewEmbedded(ctx, tempDir, port, false, "", false)
	require.NoError(t, err, "Failed to create PostgreSQL service")

	t.Cleanup(func() {
		_ = svc.Close()
	})

	// Initialize database
	err = svc.Initialize(ctx)
	require.NoError(t, err)

	db := svc.DB()

	// Build connection string for listener
	connString := fmt.Sprintf("postgres://postgres:postgres@localhost:%d/solidping_test?sslmode=disable", port)

	t.Run("NewPgEventNotifier", func(t *testing.T) { //nolint:paralleltest // Test uses shared database connection
		logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelDebug}))
		notifier, err := NewPgEventNotifier(db, connString, logger)
		require.NoError(t, err, "NewPgEventNotifier should not fail")
		require.NotNil(t, notifier)

		defer func() {
			_ = notifier.Close()
		}()

		ch := notifier.Listen("check.created")
		assert.NotNil(t, ch)
	})

	t.Run("Notify_and_receive", func(t *testing.T) { //nolint:paralleltest // Test uses shared database connection
		logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelDebug}))
		notifier, err := NewPgEventNotifier(db, connString, logger)
		require.NoError(t, err)

		defer func() {
			_ = notifier.Close()
		}()

		// Listen for check.created events
		ch := notifier.Listen("check.created")

		// Give listener time to establish connection
		time.Sleep(100 * time.Millisecond)

		// Send notification with payload
		err = notifier.Notify(ctx, "check.created", `{"check_uid":"chk_123"}`)
		require.NoError(t, err)

		// Verify notification received with correct payload
		select {
		case payload := <-ch:
			assert.JSONEq(t, `{"check_uid":"chk_123"}`, payload)
		case <-time.After(2 * time.Second):
			t.Fatal("Expected notification but none received within timeout")
		}
	})

	t.Run("multiple_event_types", func(t *testing.T) { //nolint:paralleltest // Test uses shared database connection
		logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelDebug}))
		notifier, err := NewPgEventNotifier(db, connString, logger)
		require.NoError(t, err)

		defer func() {
			_ = notifier.Close()
		}()

		// Listen for different event types
		createdCh := notifier.Listen("check.created")
		updatedCh := notifier.Listen("check.updated")

		// Give listener time to establish connection
		time.Sleep(100 * time.Millisecond)

		// Send different notifications
		err = notifier.Notify(ctx, "check.created", "created_payload")
		require.NoError(t, err)

		err = notifier.Notify(ctx, "check.updated", "updated_payload")
		require.NoError(t, err)

		// Verify each channel receives correct payload
		select {
		case payload := <-createdCh:
			assert.Equal(t, "created_payload", payload)
		case <-time.After(2 * time.Second):
			t.Fatal("Expected check.created notification")
		}

		select {
		case payload := <-updatedCh:
			assert.Equal(t, "updated_payload", payload)
		case <-time.After(2 * time.Second):
			t.Fatal("Expected check.updated notification")
		}
	})

	//nolint:paralleltest // Test uses shared database connection
	t.Run("multiple_notifications_coalesce", func(t *testing.T) {
		logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelDebug}))
		notifier, err := NewPgEventNotifier(db, connString, logger)
		require.NoError(t, err)

		defer func() {
			_ = notifier.Close()
		}()

		// Listen for events
		ch := notifier.Listen("check.created")

		// Give listener time to establish connection
		time.Sleep(100 * time.Millisecond)

		// Send multiple notifications rapidly
		for i := 0; i < 5; i++ {
			_ = notifier.Notify(ctx, "check.created", "payload")
		}

		// Should receive at least one notification
		select {
		case <-ch:
			// Success - received notification
		case <-time.After(2 * time.Second):
			t.Fatal("Expected notification but none received")
		}

		// Notifications may coalesce in the channel buffer
	})

	t.Run("Close_closes_listener_and_channel", func(t *testing.T) {
		logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelDebug}))
		notifier, err := NewPgEventNotifier(db, connString, logger)
		require.NoError(t, err)

		// Create a listener
		ch := notifier.Listen("check.created")

		// Close the notifier
		err = notifier.Close()
		require.NoError(t, err)

		// Verify channel is closed
		select {
		case _, ok := <-ch:
			if ok {
				t.Fatal("Expected channel to be closed but received value")
			}
			// Success - channel is closed
		case <-time.After(100 * time.Millisecond):
			t.Fatal("Channel should be closed immediately")
		}
	})

	t.Run("multiple_close_calls_are_safe", func(t *testing.T) {
		logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelDebug}))
		notifier, err := NewPgEventNotifier(db, connString, logger)
		require.NoError(t, err)

		// Close multiple times should not panic
		err = notifier.Close()
		require.NoError(t, err)

		err = notifier.Close()
		require.NoError(t, err)
	})

	t.Run("concurrent_notifications", func(t *testing.T) {
		logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelDebug}))
		notifier, err := NewPgEventNotifier(db, connString, logger)
		require.NoError(t, err)

		defer func() {
			_ = notifier.Close()
		}()

		// Listen for events
		ch := notifier.Listen("check.created")

		// Give listener time to establish connection
		time.Sleep(100 * time.Millisecond)

		// Send notifications from multiple goroutines concurrently
		const numGoroutines = 10
		done := make(chan struct{}, numGoroutines)

		for i := 0; i < numGoroutines; i++ {
			go func() {
				_ = notifier.Notify(ctx, "check.created", "payload")
				done <- struct{}{}
			}()
		}

		// Wait for all goroutines to complete
		for i := 0; i < numGoroutines; i++ {
			select {
			case <-done:
				// Goroutine completed
			case <-time.After(time.Second):
				t.Fatal("Goroutine did not complete in time")
			}
		}

		// Should receive at least one notification
		select {
		case <-ch:
			// Success
		case <-time.After(2 * time.Second):
			t.Fatal("Expected notification but none received")
		}
	})

	t.Run("multiple_listeners_same_event", func(t *testing.T) {
		logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelDebug}))
		notifier, err := NewPgEventNotifier(db, connString, logger)
		require.NoError(t, err)

		defer func() {
			_ = notifier.Close()
		}()

		// Create multiple listeners for the same event type
		ch1 := notifier.Listen("check.created")
		ch2 := notifier.Listen("check.created")
		ch3 := notifier.Listen("check.created")

		// Give listener time to establish connection
		time.Sleep(100 * time.Millisecond)

		// Send one notification
		_ = notifier.Notify(ctx, "check.created", "shared_payload")

		// All listeners should receive the notification
		for i, ch := range []<-chan string{ch1, ch2, ch3} {
			select {
			case payload := <-ch:
				assert.Equal(t, "shared_payload", payload, "Listener %d", i+1)
			case <-time.After(2 * time.Second):
				t.Fatalf("Listener %d: Expected notification", i+1)
			}
		}
	})
}

//nolint:paralleltest // Test uses different database types
func TestPgEventNotifier_factory(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping PostgreSQL notifier test in short mode")
	}

	// Create embedded PostgreSQL instance
	tempDir, err := os.MkdirTemp("", "pg-notifier-factory-test-*")
	require.NoError(t, err)

	t.Cleanup(func() {
		_ = os.RemoveAll(tempDir)
	})

	ctx := t.Context()
	port := uint32(15437)
	svc, err := postgres.NewEmbedded(ctx, tempDir, port, false, "", false)
	require.NoError(t, err)

	t.Cleanup(func() {
		_ = svc.Close()
	})

	err = svc.Initialize(ctx)
	require.NoError(t, err)

	db := svc.DB()
	connString := fmt.Sprintf("postgres://postgres:postgres@localhost:%d/solidping_test?sslmode=disable", port)
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelDebug}))

	//nolint:lll // Test name clarity over line length
	t.Run("creates_PgEventNotifier_for_postgres", func(t *testing.T) { //nolint:paralleltest // Test uses different database types
		notifier, err := New(db, "postgres", connString, logger)
		require.NoError(t, err)
		require.NotNil(t, notifier)

		defer func() {
			_ = notifier.Close()
		}()

		// Should be PgEventNotifier
		_, ok := notifier.(*PgEventNotifier)
		assert.True(t, ok, "Expected PgEventNotifier for postgres type")
	})

	//nolint:lll // Test name clarity over line length
	t.Run("creates_PgEventNotifier_for_postgres-embedded", func(t *testing.T) { //nolint:paralleltest // Test uses different database types
		notifier, err := New(db, "postgres-embedded", connString, logger)
		require.NoError(t, err)
		require.NotNil(t, notifier)

		defer func() {
			_ = notifier.Close()
		}()

		// Should be PgEventNotifier
		_, ok := notifier.(*PgEventNotifier)
		assert.True(t, ok, "Expected PgEventNotifier for postgres-embedded type")
	})
}

//nolint:tparallel // Test subtests are properly parallelized
func TestLocalEventNotifier_factory(t *testing.T) {
	t.Parallel()

	//nolint:lll // Test name clarity over line length
	t.Run("creates_LocalEventNotifier_for_sqlite", func(t *testing.T) { //nolint:paralleltest // Test uses different database types
		notifier, err := New(nil, "sqlite", "", nil)
		require.NoError(t, err)
		require.NotNil(t, notifier)

		defer func() {
			_ = notifier.Close()
		}()

		// Should be LocalEventNotifier
		_, ok := notifier.(*LocalEventNotifier)
		assert.True(t, ok, "Expected LocalEventNotifier for sqlite type")
	})

	//nolint:lll // Test name clarity over line length
	t.Run("creates_LocalEventNotifier_for_sqlite-memory", func(t *testing.T) { //nolint:paralleltest // Test uses different database types
		notifier, err := New(nil, "sqlite-memory", "", nil)
		require.NoError(t, err)
		require.NotNil(t, notifier)

		defer func() {
			_ = notifier.Close()
		}()

		// Should be LocalEventNotifier
		_, ok := notifier.(*LocalEventNotifier)
		assert.True(t, ok, "Expected LocalEventNotifier for sqlite-memory type")
	})

	//nolint:lll // Test name clarity over line length
	t.Run("creates_LocalEventNotifier_for_unknown_type", func(t *testing.T) { //nolint:paralleltest // Test uses different database types
		notifier, err := New(nil, "unknown-db", "", nil)
		require.NoError(t, err)
		require.NotNil(t, notifier)

		defer func() {
			_ = notifier.Close()
		}()

		// Should fall back to LocalEventNotifier
		_, ok := notifier.(*LocalEventNotifier)
		assert.True(t, ok, "Expected LocalEventNotifier for unknown type")
	})
}
