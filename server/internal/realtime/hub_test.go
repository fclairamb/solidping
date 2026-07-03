package realtime

import (
	"context"
	"fmt"
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

// publishHint is a small test helper that encodes and sends a hint directly
// on the bus, bypassing the Publisher's coalescing. Every call site in this
// file targets "org-a" — org isolation itself is covered separately by
// TestHub_OtherOrgSubscriberNeverMatches, which publishes for "org-a" too and
// asserts an "org-b" subscriber never sees it.
func publishHint(t *testing.T, bus notifier.EventNotifier, kinds []Kind, checkUids ...string) {
	t.Helper()
	r := require.New(t)

	checkUIDSet := make(map[string]struct{}, len(checkUids))
	for _, uid := range checkUids {
		checkUIDSet[uid] = struct{}{}
	}

	payload, err := EncodeHint("org-a", kindSet(kinds), checkUIDSet)
	r.NoError(err)
	r.NoError(bus.Notify(context.Background(), ChannelOrgEvents, payload))
}

func TestHub_DefaultSilent_ZeroScopesReceiveNothing(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	bus := notifier.NewLocalEventNotifier()
	hub := NewHub(bus, 0, nil)
	defer hub.Close()

	sub, err := hub.Subscribe("org-a")
	r.NoError(err)

	publishHint(t, bus, []Kind{KindChecks, KindResults, KindIncidents}, "check-1")

	select {
	case <-sub.Signal():
		t.Fatal("a connection with zero subscriptions must never receive a dispatch")
	case <-time.After(100 * time.Millisecond):
	}
}

func TestHub_CollectionSubscriberReceivesMatchingKindOnly(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	bus := notifier.NewLocalEventNotifier()
	hub := NewHub(bus, 0, nil)
	defer hub.Close()

	sub, err := hub.Subscribe("org-a")
	r.NoError(err)
	r.NoError(sub.AddScope(Scope{Entity: EntityIncidents}))

	// A results-only hint (checks collection concern) must not reach the
	// incidents-only subscriber.
	publishHint(t, bus, []Kind{KindResults}, "check-1")
	select {
	case <-sub.Signal():
		t.Fatal("incidents subscriber must not receive a results-only hint")
	case <-time.After(100 * time.Millisecond):
	}

	publishHint(t, bus, []Kind{KindIncidents, KindEvents}, "check-1")
	waitSignal(t, sub)
	updates, resync, closed := sub.Take()
	r.False(resync)
	r.False(closed)
	r.Equal([]ScopedUpdate{{Scope: Scope{Entity: EntityIncidents}, Kinds: []Kind{KindIncidents}}}, updates)
}

func TestHub_PerCheckIsolation(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	bus := notifier.NewLocalEventNotifier()
	hub := NewHub(bus, 0, nil)
	defer hub.Close()

	sub, err := hub.Subscribe("org-a")
	r.NoError(err)
	r.NoError(sub.AddScope(Scope{Entity: EntityCheck, UID: "check-x"}))

	// Activity on a different check of the same org must produce no frame.
	publishHint(t, bus, []Kind{KindResults}, "check-y")
	select {
	case <-sub.Signal():
		t.Fatal("subscriber for check-x must not receive a hint about check-y")
	case <-time.After(100 * time.Millisecond):
	}

	// Activity on the subscribed check delivers.
	publishHint(t, bus, []Kind{KindResults}, "check-x")
	waitSignal(t, sub)
	updates, _, _ := sub.Take()
	r.Equal([]ScopedUpdate{{Scope: Scope{Entity: EntityCheck, UID: "check-x"}, Kinds: []Kind{KindResults}}}, updates)
}

func TestHub_OtherOrgSubscriberNeverMatches(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	bus := notifier.NewLocalEventNotifier()
	hub := NewHub(bus, 0, nil)
	defer hub.Close()

	subA, err := hub.Subscribe("org-a")
	r.NoError(err)
	r.NoError(subA.AddScope(Scope{Entity: EntityChecks}))
	subB, err := hub.Subscribe("org-b")
	r.NoError(err)
	r.NoError(subB.AddScope(Scope{Entity: EntityChecks}))

	publishHint(t, bus, []Kind{KindChecks}, "check-1")

	waitSignal(t, subA)
	updates, _, _ := subA.Take()
	r.Len(updates, 1)

	select {
	case <-subB.Signal():
		t.Fatal("subscriber for another org must not receive the hint")
	case <-time.After(50 * time.Millisecond):
	}
}

func TestHub_StormCollapseWildcardReachesCollectionAndPerCheckSubscribers(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	bus := notifier.NewLocalEventNotifier()
	hub := NewHub(bus, 0, nil)
	defer hub.Close()

	collectionSub, err := hub.Subscribe("org-a")
	r.NoError(err)
	r.NoError(collectionSub.AddScope(Scope{Entity: EntityChecks}))

	perCheckSub, err := hub.Subscribe("org-a")
	r.NoError(err)
	r.NoError(perCheckSub.AddScope(Scope{Entity: EntityCheck, UID: "check-arbitrary"}))

	payload, err := EncodeHint("org-a", kindSet([]Kind{KindResults}), map[string]struct{}{"*": {}})
	r.NoError(err)
	r.NoError(bus.Notify(context.Background(), ChannelOrgEvents, payload))

	waitSignal(t, collectionSub)
	updates, _, _ := collectionSub.Take()
	r.Equal([]ScopedUpdate{{Scope: Scope{Entity: EntityChecks}, Kinds: []Kind{KindResults}}}, updates)

	waitSignal(t, perCheckSub)
	updates, _, _ = perCheckSub.Take()
	r.Equal([]ScopedUpdate{
		{Scope: Scope{Entity: EntityCheck, UID: "check-arbitrary"}, Kinds: []Kind{KindResults}},
	}, updates)
}

func TestHub_SlowSubscriberCoalescesIntoDirtySet(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	bus := notifier.NewLocalEventNotifier()
	hub := NewHub(bus, 0, nil)
	defer hub.Close()

	sub, err := hub.Subscribe("org-a")
	r.NoError(err)
	r.NoError(sub.AddScope(Scope{Entity: EntityChecks}))
	r.NoError(sub.AddScope(Scope{Entity: EntityJobs}))
	r.NoError(sub.AddScope(Scope{Entity: EntityIncidents}))

	send := func(kinds ...Kind) {
		publishHint(t, bus, kinds)
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

	updates, _, _ := sub.Take()
	r.ElementsMatch([]ScopedUpdate{
		{Scope: Scope{Entity: EntityChecks}, Kinds: []Kind{KindResults}},
		{Scope: Scope{Entity: EntityJobs}, Kinds: []Kind{KindJobs}},
		{Scope: Scope{Entity: EntityIncidents}, Kinds: []Kind{KindIncidents}},
	}, updates)

	// Drained: a second Take returns nothing.
	updates, resync, closed := sub.Take()
	r.Empty(updates)
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
		updates, resync, closed := sub.Take()
		r.Empty(updates)
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

func TestHub_SubscriptionCapGuard(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	bus := notifier.NewLocalEventNotifier()
	hub := NewHubWithSubscriptionCap(bus, 0, 2, nil)
	defer hub.Close()

	sub, err := hub.Subscribe("org-a")
	r.NoError(err)

	r.NoError(sub.AddScope(Scope{Entity: EntityCheck, UID: "check-1"}))
	r.NoError(sub.AddScope(Scope{Entity: EntityCheck, UID: "check-2"}))

	err = sub.AddScope(Scope{Entity: EntityCheck, UID: "check-3"})
	r.ErrorIs(err, ErrTooManySubscriptions)

	// Re-adding an already-subscribed scope is idempotent, not blocked by the cap.
	r.NoError(sub.AddScope(Scope{Entity: EntityCheck, UID: "check-1"}))
	r.Equal(2, sub.ScopeCount())

	// Freeing a slot admits a new scope.
	sub.RemoveScope(Scope{Entity: EntityCheck, UID: "check-2"})
	r.NoError(sub.AddScope(Scope{Entity: EntityCheck, UID: "check-3"}))
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
	r.NoError(sub.AddScope(Scope{Entity: EntityIncidents}))
	r.NoError(sub.AddScope(Scope{Entity: EntityEvents}))

	pub.PublishImmediate(context.Background(), "org-e2e", "check-1", KindIncidents, KindEvents)

	waitSignal(t, sub)
	updates, _, _ := sub.Take()
	r.ElementsMatch([]ScopedUpdate{
		{Scope: Scope{Entity: EntityIncidents}, Kinds: []Kind{KindIncidents}},
		{Scope: Scope{Entity: EntityEvents}, Kinds: []Kind{KindEvents}},
	}, updates)
}

func TestHub_MalformedPayloadIsDropped(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	bus := notifier.NewLocalEventNotifier()
	hub := NewHub(bus, 0, nil)
	defer hub.Close()

	sub, err := hub.Subscribe("org-a")
	r.NoError(err)
	r.NoError(sub.AddScope(Scope{Entity: EntityChecks}))

	r.NoError(bus.Notify(context.Background(), ChannelOrgEvents, "{not json"))

	select {
	case <-sub.Signal():
		t.Fatal("malformed payload must be dropped, not delivered")
	case <-time.After(50 * time.Millisecond):
	}
}

func TestHub_ScopeCountAndHasScope(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	bus := notifier.NewLocalEventNotifier()
	hub := NewHub(bus, 0, nil)
	defer hub.Close()

	sub, err := hub.Subscribe("org-a")
	r.NoError(err)
	r.Equal(0, sub.ScopeCount())
	r.False(sub.HasScope(Scope{Entity: EntityChecks}))

	r.NoError(sub.AddScope(Scope{Entity: EntityChecks}))
	r.Equal(1, sub.ScopeCount())
	r.True(sub.HasScope(Scope{Entity: EntityChecks}))

	sub.RemoveScope(Scope{Entity: EntityChecks})
	r.Equal(0, sub.ScopeCount())
	r.False(sub.HasScope(Scope{Entity: EntityChecks}))
}

// TestHub_ManyDistinctCheckSubscribersAllReachedByWildcard exercises a
// storm-scale fan-out: many per-check subscribers across the org all receive
// the wildcard hint, proving the collapse doesn't just work for one uid.
func TestHub_ManyDistinctCheckSubscribersAllReachedByWildcard(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	bus := notifier.NewLocalEventNotifier()
	hub := NewHub(bus, 0, nil)
	defer hub.Close()

	const n = 80
	subs := make([]*Subscriber, n)
	for i := range n {
		sub, err := hub.Subscribe("org-storm")
		r.NoError(err)
		r.NoError(sub.AddScope(Scope{Entity: EntityCheck, UID: fmt.Sprintf("check-%d", i)}))
		subs[i] = sub
	}

	payload, err := EncodeHint("org-storm", kindSet([]Kind{KindResults}), map[string]struct{}{"*": {}})
	r.NoError(err)
	r.NoError(bus.Notify(context.Background(), ChannelOrgEvents, payload))

	for _, sub := range subs {
		waitSignal(t, sub)
		updates, _, _ := sub.Take()
		r.Len(updates, 1)
	}
}
