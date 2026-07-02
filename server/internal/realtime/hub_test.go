package realtime

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/notifier"
)

// fakeReconnectNotifier wraps a LocalEventNotifier with a controllable
// reconnect channel so hub resync behavior is testable without PostgreSQL.
type fakeReconnectNotifier struct {
	*notifier.LocalEventNotifier
	reconnects chan struct{}
}

func (f *fakeReconnectNotifier) ReconnectEvents() <-chan struct{} {
	return f.reconnects
}

func waitSignal(t *testing.T, sub *Subscriber) {
	t.Helper()

	select {
	case <-sub.Signal():
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for subscriber signal")
	}
}

func TestHub_DispatchesHintsToMatchingOrgOnly(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	bus := notifier.NewLocalEventNotifier()
	hub := NewHub(bus, 0, nil)
	defer hub.Close()

	subA, err := hub.Subscribe("org-a")
	r.NoError(err)
	subB, err := hub.Subscribe("org-b")
	r.NoError(err)

	payload, err := EncodeHint("org-a", kindSet([]Kind{KindResults, KindChecks}))
	r.NoError(err)
	r.NoError(bus.Notify(context.Background(), ChannelOrgEvents, payload))

	waitSignal(t, subA)
	kinds, resync, closed := subA.Take()
	r.Equal([]Kind{KindChecks, KindResults}, kinds)
	r.False(resync)
	r.False(closed)

	// org-b must not have been signaled.
	select {
	case <-subB.Signal():
		t.Fatal("subscriber for another org must not receive the hint")
	case <-time.After(50 * time.Millisecond):
	}
}

func TestHub_SlowSubscriberCoalescesIntoDirtySet(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	bus := notifier.NewLocalEventNotifier()
	hub := NewHub(bus, 0, nil)
	defer hub.Close()

	sub, err := hub.Subscribe("org-a")
	r.NoError(err)

	send := func(kinds ...Kind) {
		payload, encErr := EncodeHint("org-a", kindSet(kinds))
		r.NoError(encErr)
		r.NoError(bus.Notify(context.Background(), ChannelOrgEvents, payload))
	}

	send(KindResults)
	waitSignal(t, sub)
	// Do not Take yet — more hints arrive while the consumer is "slow".
	send(KindJobs)
	send(KindIncidents)

	r.Eventually(func() bool {
		sub.mu.Lock()
		defer sub.mu.Unlock()

		return len(sub.dirty) == 3
	}, 2*time.Second, time.Millisecond)

	kinds, _, _ := sub.Take()
	r.Equal([]Kind{KindIncidents, KindJobs, KindResults}, kinds)

	// Drained: a second Take returns nothing.
	kinds, resync, closed := sub.Take()
	r.Empty(kinds)
	r.False(resync)
	r.False(closed)
}

func TestHub_ReconnectBroadcastsResync(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	bus := &fakeReconnectNotifier{
		LocalEventNotifier: notifier.NewLocalEventNotifier(),
		reconnects:         make(chan struct{}, 1),
	}
	hub := NewHub(bus, 0, nil)
	defer hub.Close()

	subA, err := hub.Subscribe("org-a")
	r.NoError(err)
	subB, err := hub.Subscribe("org-b")
	r.NoError(err)

	bus.reconnects <- struct{}{}

	for _, sub := range []*Subscriber{subA, subB} {
		waitSignal(t, sub)
		kinds, resync, closed := sub.Take()
		r.Empty(kinds)
		r.True(resync, "every local subscriber must be marked for resync after a bus reconnect")
		r.False(closed)
	}
}

func TestHub_UnsubscribeClosesAndUnregisters(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	bus := notifier.NewLocalEventNotifier()
	hub := NewHub(bus, 0, nil)
	defer hub.Close()

	sub, err := hub.Subscribe("org-a")
	r.NoError(err)
	r.Equal(1, hub.ConnectionCount())

	hub.Unsubscribe(sub)
	r.Equal(0, hub.ConnectionCount())

	waitSignal(t, sub)
	_, _, closed := sub.Take()
	r.True(closed)

	// Idempotent.
	hub.Unsubscribe(sub)
	r.Equal(0, hub.ConnectionCount())
}

func TestHub_MaxConnectionsGuard(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	bus := notifier.NewLocalEventNotifier()
	hub := NewHub(bus, 2, nil)
	defer hub.Close()

	_, err := hub.Subscribe("org-a")
	r.NoError(err)
	second, err := hub.Subscribe("org-a")
	r.NoError(err)

	_, err = hub.Subscribe("org-b")
	r.ErrorIs(err, ErrTooManyConnections)

	// Freeing a slot admits a new subscriber.
	hub.Unsubscribe(second)
	_, err = hub.Subscribe("org-b")
	r.NoError(err)
}

func TestHub_CloseTerminatesSubscribers(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	bus := notifier.NewLocalEventNotifier()
	hub := NewHub(bus, 0, nil)

	sub, err := hub.Subscribe("org-a")
	r.NoError(err)

	hub.Close()

	waitSignal(t, sub)
	_, _, closed := sub.Take()
	r.True(closed)

	_, err = hub.Subscribe("org-a")
	r.ErrorIs(err, ErrHubClosed)
	r.Equal(0, hub.ConnectionCount())
}

func TestHub_EndToEndThroughPublisher(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	// Publisher and hub sharing one local bus — the exact SQLite single-node
	// wiring: hints published on one side surface on the other.
	bus := notifier.NewLocalEventNotifier()
	hub := NewHub(bus, 0, nil)
	defer hub.Close()
	pub := NewPublisher(t.Context(), bus, time.Second, nil)
	defer pub.Close()

	sub, err := hub.Subscribe("org-e2e")
	r.NoError(err)

	pub.PublishImmediate(context.Background(), "org-e2e", KindIncidents, KindEvents)

	waitSignal(t, sub)
	kinds, _, _ := sub.Take()
	r.Equal([]Kind{KindEvents, KindIncidents}, kinds)
}

func TestHub_MalformedPayloadIsDropped(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	bus := notifier.NewLocalEventNotifier()
	hub := NewHub(bus, 0, nil)
	defer hub.Close()

	sub, err := hub.Subscribe("org-a")
	r.NoError(err)

	r.NoError(bus.Notify(context.Background(), ChannelOrgEvents, "{not json"))

	select {
	case <-sub.Signal():
		t.Fatal("malformed payload must be dropped, not delivered")
	case <-time.After(50 * time.Millisecond):
	}
}
