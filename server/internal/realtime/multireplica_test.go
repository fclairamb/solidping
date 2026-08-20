package realtime_test

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/postgres"
	"github.com/fclairamb/solidping/server/internal/notifier"
	"github.com/fclairamb/solidping/server/internal/realtime"
)

// drainHint waits for the subscriber's wake-up and returns its drained
// scoped updates.
func drainHint(t *testing.T, sub *realtime.Subscriber) []realtime.ScopedUpdate {
	t.Helper()

	select {
	case <-sub.Signal():
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for a cross-replica hint")
	}

	updates, _, closed := sub.Take()
	require.False(t, closed)

	return updates
}

// TestMultiReplica_PostgresHintCrossesInstances proves the distributed
// contract: a hint published through one PgEventNotifier (replica B's write
// path) reaches a hub subscriber on a different PgEventNotifier (replica A's
// API process) via PostgreSQL NOTIFY — no shared process state involved.
func TestMultiReplica_PostgresHintCrossesInstances(t *testing.T) { //nolint:paralleltest // embedded PG instance
	if testing.Short() {
		t.Skip("Skipping embedded PostgreSQL multi-replica test in short mode")
	}

	r := require.New(t)
	ctx := t.Context()

	const port = uint32(15438)
	svc, err := postgres.NewEmbedded(ctx, "realtime-multireplica", port, false, "", false, 0)
	r.NoError(err, "failed to start embedded postgres")
	t.Cleanup(func() { _ = svc.Close() })
	r.NoError(svc.Initialize(ctx))

	connString := fmt.Sprintf("postgres://postgres:postgres@localhost:%d/solidping_test?sslmode=disable", port)
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))

	// Replica A: holds the hub (API process with dashboard connections).
	notifierA, err := notifier.NewPgEventNotifier(svc.DB(), connString, logger)
	r.NoError(err)
	t.Cleanup(func() { _ = notifierA.Close() })

	// Replica B: ingests results and publishes hints.
	notifierB, err := notifier.NewPgEventNotifier(svc.DB(), connString, logger)
	r.NoError(err)
	t.Cleanup(func() { _ = notifierB.Close() })

	hub := realtime.NewHub(notifierA, 0, logger)
	t.Cleanup(hub.Close)

	sub, err := hub.Subscribe("org-x")
	r.NoError(err)
	r.NoError(sub.AddScope(realtime.Scope{Entity: realtime.EntityCheck, UID: "check-x1"}))
	other, err := hub.Subscribe("org-other")
	r.NoError(err)
	r.NoError(other.AddScope(realtime.Scope{Entity: realtime.EntityCheck, UID: "check-x1"}))

	// Give the LISTEN a moment to be established before the first NOTIFY.
	time.Sleep(500 * time.Millisecond)

	pub := realtime.NewPublisher(ctx, notifierB, time.Second, logger)
	t.Cleanup(pub.Close)
	pub.PublishImmediate(context.Background(), "org-x", "check-x1", realtime.KindChecks, realtime.KindIncidents)

	updates := drainHint(t, sub)
	r.Equal([]realtime.ScopedUpdate{
		{
			Scope: realtime.Scope{Entity: realtime.EntityCheck, UID: "check-x1"},
			Kinds: []realtime.Kind{realtime.KindChecks, realtime.KindIncidents},
		},
	}, updates, "hint published on replica B must reach the subscriber held by replica A")

	// Org isolation across the bus: the other org saw nothing, even though it
	// is watching the same check uid string (uids are org-scoped by hub
	// registration, not by value).
	select {
	case <-other.Signal():
		t.Fatal("hint for org-x must not reach an org-other subscriber")
	case <-time.After(200 * time.Millisecond):
	}
}

// TestMultiReplica_LocalNotifierSameScenario runs the exact same scenario on
// the SQLite single-process path: one LocalEventNotifier shared by publisher
// and hub. Behavior must be identical.
func TestMultiReplica_LocalNotifierSameScenario(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	bus := notifier.NewLocalEventNotifier()
	t.Cleanup(func() { _ = bus.Close() })

	hub := realtime.NewHub(bus, 0, nil)
	t.Cleanup(hub.Close)

	sub, err := hub.Subscribe("org-x")
	r.NoError(err)
	r.NoError(sub.AddScope(realtime.Scope{Entity: realtime.EntityCheck, UID: "check-x1"}))
	other, err := hub.Subscribe("org-other")
	r.NoError(err)
	r.NoError(other.AddScope(realtime.Scope{Entity: realtime.EntityCheck, UID: "check-x1"}))

	pub := realtime.NewPublisher(t.Context(), bus, time.Second, nil)
	t.Cleanup(pub.Close)
	pub.PublishImmediate(context.Background(), "org-x", "check-x1", realtime.KindChecks, realtime.KindIncidents)

	updates := drainHint(t, sub)
	r.Equal([]realtime.ScopedUpdate{
		{
			Scope: realtime.Scope{Entity: realtime.EntityCheck, UID: "check-x1"},
			Kinds: []realtime.Kind{realtime.KindChecks, realtime.KindIncidents},
		},
	}, updates)

	select {
	case <-other.Signal():
		t.Fatal("hint for org-x must not reach an org-other subscriber")
	case <-time.After(100 * time.Millisecond):
	}
}
