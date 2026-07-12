package notifier

import (
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
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

// TestLocalEventNotifier_BurstWithinBufferIsLossless pins the ListenerBuffer
// guarantee: a burst of up to ListenerBuffer notifications sent while the
// consumer is NOT draining must all be delivered once it starts. This is the
// regression test for the CI-only realtime failures where two back-to-back
// org hints outran the draining goroutine and the second was dropped by the
// old 1-slot buffer.
func TestLocalEventNotifier_BurstWithinBufferIsLossless(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	n := NewLocalEventNotifier()
	defer func() { _ = n.Close() }()

	ch := n.Listen("burst")

	// Publish a full buffer's worth before the consumer reads anything.
	for i := 0; i < ListenerBuffer; i++ {
		r.NoError(n.Notify(context.Background(), "burst", strconv.Itoa(i)))
	}

	// Every payload must be buffered, in order, with nothing dropped.
	for i := 0; i < ListenerBuffer; i++ {
		select {
		case got := <-ch:
			r.Equal(strconv.Itoa(i), got)
		case <-time.After(time.Second):
			r.FailNowf("dropped payload", "payload %d never arrived", i)
		}
	}
}

// TestLocalEventNotifier_UnlistenBoundsListenerCount is the regression test for
// the 2026-07-11 listener leak: a per-call subscriber (GetJobWait) that Listens
// then Unlistens must not grow the listener slices without bound. After each
// Listen/Unlisten cycle ListenerCount returns to its baseline.
func TestLocalEventNotifier_UnlistenBoundsListenerCount(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		cycles int
	}{
		{name: "few cycles", cycles: 10},
		{name: "many cycles", cycles: 5000},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)

			n := NewLocalEventNotifier()
			defer func() { _ = n.Close() }()

			r.Equal(0, n.ListenerCount(), "baseline should be zero")

			for i := 0; i < tc.cycles; i++ {
				ch := n.Listen("job.created")
				r.Equal(1, n.ListenerCount(), "exactly one listener while subscribed")
				n.Unlisten("job.created", ch)
				r.Equal(0, n.ListenerCount(), "Unlisten must reclaim the slot")
			}

			r.Equal(0, n.ListenerCount(), "no listeners must leak across cycles")
		})
	}
}

// TestLocalEventNotifier_UnlistenSelective verifies Unlisten removes only the
// targeted channel and that unknown channels are a safe no-op.
func TestLocalEventNotifier_UnlistenSelective(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	n := NewLocalEventNotifier()
	defer func() { _ = n.Close() }()

	ch1 := n.Listen("check.created")
	ch2 := n.Listen("check.created")
	ch3 := n.Listen("check.updated")
	r.Equal(3, n.ListenerCount())

	// Unlisten of a channel that was never registered is a no-op.
	stranger := make(chan string)
	n.Unlisten("check.created", stranger)
	r.Equal(3, n.ListenerCount(), "unknown channel must not remove anything")

	// Wrong event type is also a no-op.
	n.Unlisten("check.updated", ch1)
	r.Equal(3, n.ListenerCount(), "channel must only be removed under its own event type")

	// Remove ch1; ch2 must still receive notifications.
	n.Unlisten("check.created", ch1)
	r.Equal(2, n.ListenerCount())

	r.NoError(n.Notify(context.Background(), "check.created", "hello"))
	select {
	case got := <-ch2:
		r.Equal("hello", got)
	case <-time.After(time.Second):
		r.FailNow("surviving listener ch2 did not receive notification")
	}

	// Double Unlisten of ch1 is a no-op.
	n.Unlisten("check.created", ch1)
	r.Equal(2, n.ListenerCount())

	n.Unlisten("check.created", ch2)
	n.Unlisten("check.updated", ch3)
	r.Equal(0, n.ListenerCount())
}
