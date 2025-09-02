package notifier

import (
	"context"
	"testing"
	"time"
)

func TestLocalEventNotifier_Notify(t *testing.T) {
	t.Parallel()

	t.Run("successful notification", func(t *testing.T) {
		t.Parallel()

		notifier := NewLocalEventNotifier()
		defer func() { _ = notifier.Close() }()

		ctx := context.Background()

		// Listen for check.created events
		ch := notifier.Listen("check.created")

		// Send notification
		if err := notifier.Notify(ctx, "check.created", `{"check_uid":"chk_123"}`); err != nil {
			t.Fatalf("Notify() error = %v, want nil", err)
		}

		// Verify notification received with correct payload
		select {
		case payload := <-ch:
			if payload != `{"check_uid":"chk_123"}` {
				t.Fatalf("Expected payload %q, got %q", `{"check_uid":"chk_123"}`, payload)
			}
		case <-time.After(100 * time.Millisecond):
			t.Fatal("Expected notification but none received")
		}
	})

	t.Run("non-blocking when channel full", func(t *testing.T) {
		t.Parallel()

		notifier := NewLocalEventNotifier()
		defer func() { _ = notifier.Close() }()

		ctx := context.Background()

		// Listen for events
		_ = notifier.Listen("check.created")

		// Fill the buffer (size 1)
		if err := notifier.Notify(ctx, "check.created", "payload1"); err != nil {
			t.Fatalf("First Notify() error = %v, want nil", err)
		}

		// Second notification should not block (channel is full)
		done := make(chan struct{})
		go func() {
			_ = notifier.Notify(ctx, "check.created", "payload2")
			close(done)
		}()

		// Verify it completes quickly without blocking
		select {
		case <-done:
			// Success - did not block
		case <-time.After(100 * time.Millisecond):
			t.Fatal("Notify() blocked when channel full")
		}
	})

	t.Run("multiple event types", func(t *testing.T) {
		t.Parallel()

		notifier := NewLocalEventNotifier()
		defer func() { _ = notifier.Close() }()

		ctx := context.Background()

		// Listen for different event types
		createdCh := notifier.Listen("check.created")
		updatedCh := notifier.Listen("check.updated")

		// Send notifications
		_ = notifier.Notify(ctx, "check.created", "created_payload")
		_ = notifier.Notify(ctx, "check.updated", "updated_payload")

		// Verify each channel receives correct payload
		select {
		case payload := <-createdCh:
			if payload != "created_payload" {
				t.Fatalf("Expected created_payload, got %s", payload)
			}
		case <-time.After(100 * time.Millisecond):
			t.Fatal("Expected check.created notification")
		}

		select {
		case payload := <-updatedCh:
			if payload != "updated_payload" {
				t.Fatalf("Expected updated_payload, got %s", payload)
			}
		case <-time.After(100 * time.Millisecond):
			t.Fatal("Expected check.updated notification")
		}
	})

	t.Run("no listeners for event type", func(t *testing.T) {
		t.Parallel()

		notifier := NewLocalEventNotifier()
		defer func() { _ = notifier.Close() }()

		ctx := context.Background()

		// Send notification without any listeners
		if err := notifier.Notify(ctx, "check.created", "payload"); err != nil {
			t.Fatalf("Notify() without listeners error = %v, want nil", err)
		}
	})
}

func TestLocalEventNotifier_Listen(t *testing.T) {
	t.Parallel()

	t.Run("returns channel", func(t *testing.T) {
		t.Parallel()

		notifier := NewLocalEventNotifier()
		defer func() { _ = notifier.Close() }()

		ch := notifier.Listen("check.created")
		if ch == nil {
			t.Fatal("Listen() returned nil")
		}

		// Verify we can receive from the channel
		ctx := context.Background()
		_ = notifier.Notify(ctx, "check.created", "test_payload")

		select {
		case payload := <-ch:
			if payload != "test_payload" {
				t.Fatalf("Expected test_payload, got %s", payload)
			}
		case <-time.After(100 * time.Millisecond):
			t.Fatal("Expected to receive from Listen()")
		}
	})

	t.Run("multiple listeners for same event", func(t *testing.T) {
		t.Parallel()

		notifier := NewLocalEventNotifier()
		defer func() { _ = notifier.Close() }()

		ctx := context.Background()

		// Create multiple listeners for the same event type
		ch1 := notifier.Listen("check.created")
		ch2 := notifier.Listen("check.created")
		ch3 := notifier.Listen("check.created")

		// Send one notification
		_ = notifier.Notify(ctx, "check.created", "shared_payload")

		// All listeners should receive the notification
		for i, ch := range []<-chan string{ch1, ch2, ch3} {
			select {
			case payload := <-ch:
				if payload != "shared_payload" {
					t.Fatalf("Listener %d: Expected shared_payload, got %s", i+1, payload)
				}
			case <-time.After(100 * time.Millisecond):
				t.Fatalf("Listener %d: Expected notification", i+1)
			}
		}
	})
}

func TestLocalEventNotifier_Close(t *testing.T) {
	t.Parallel()

	t.Run("closes all channels", func(t *testing.T) {
		t.Parallel()

		notifier := NewLocalEventNotifier()

		// Create listeners for multiple event types
		ch1 := notifier.Listen("check.created")
		ch2 := notifier.Listen("check.updated")

		// Close the notifier
		if err := notifier.Close(); err != nil {
			t.Fatalf("Close() error = %v, want nil", err)
		}

		// Verify all channels are closed
		for i, ch := range []<-chan string{ch1, ch2} {
			select {
			case _, ok := <-ch:
				if ok {
					t.Fatalf("Channel %d: Expected channel to be closed but received value", i+1)
				}
				// Success - channel is closed
			case <-time.After(100 * time.Millisecond):
				t.Fatalf("Channel %d: Should be closed immediately", i+1)
			}
		}
	})

	t.Run("multiple close calls are safe", func(t *testing.T) {
		t.Parallel()

		notifier := NewLocalEventNotifier()

		// Close multiple times should not panic
		if err := notifier.Close(); err != nil {
			t.Fatalf("First Close() error = %v, want nil", err)
		}

		if err := notifier.Close(); err != nil {
			t.Fatalf("Second Close() error = %v, want nil", err)
		}
	})

	t.Run("notifications after close are ignored", func(t *testing.T) {
		t.Parallel()

		notifier := NewLocalEventNotifier()
		ctx := context.Background()

		_ = notifier.Listen("check.created")

		// Close the notifier
		_ = notifier.Close()

		// Notify after close should not panic
		if err := notifier.Notify(ctx, "check.created", "payload"); err != nil {
			t.Fatalf("Notify() after Close() error = %v, want nil", err)
		}
	})
}

func TestLocalEventNotifier_ConcurrentNotifications(t *testing.T) {
	t.Parallel()

	notifier := NewLocalEventNotifier()
	defer func() { _ = notifier.Close() }()

	ctx := context.Background()

	// Create listener
	ch := notifier.Listen("check.created")

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
	case <-time.After(100 * time.Millisecond):
		t.Fatal("Expected notification but none received")
	}
}
