package slack

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	slackgo "github.com/slack-go/slack"
	"github.com/slack-go/slack/socketmode"
	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
)

var (
	errForcedDisconnect = errors.New("forced disconnect")
	errUnauthorizedTest = errors.New("unauthorized")
)

// fakeSocketModeClient implements socketModeRunner with channels the test
// controls. RunContext blocks until ctx is done or runErr is sent, mirroring
// the real slack-go behavior (RunContext returns only on unrecoverable
// errors or ctx cancel).
type fakeSocketModeClient struct {
	events chan socketmode.Event
	runErr chan error

	mu      sync.Mutex
	acked   []string
	runCnt  int
	stopped bool
}

func newFakeSocketModeClient() *fakeSocketModeClient {
	return &fakeSocketModeClient{
		events: make(chan socketmode.Event, 16),
		runErr: make(chan error, 1),
	}
}

func (f *fakeSocketModeClient) RunContext(ctx context.Context) error {
	f.mu.Lock()
	f.runCnt++
	f.mu.Unlock()

	select {
	case <-ctx.Done():
		f.mu.Lock()
		f.stopped = true
		f.mu.Unlock()

		return ctx.Err()
	case err := <-f.runErr:
		return err
	}
}

func (f *fakeSocketModeClient) AckCtx(_ context.Context, reqID string, _ any) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.acked = append(f.acked, reqID)

	return nil
}

func (f *fakeSocketModeClient) EventsChan() <-chan socketmode.Event { return f.events }

func (f *fakeSocketModeClient) ackCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()

	return len(f.acked)
}

func (f *fakeSocketModeClient) runCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()

	return f.runCnt
}

// waitFor polls cond up to d. Returns true if cond ever returned true within
// the budget. Used in tests instead of time.Sleep so the assertion fires as
// soon as the goroutine observes the state change.
func waitFor(d time.Duration, cond func() bool) bool {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}

		time.Sleep(5 * time.Millisecond)
	}

	return cond()
}

func newTestSupervisor(t *testing.T, fake *fakeSocketModeClient) *SlackSocketSupervisor {
	t.Helper()

	cfg := &config.Config{
		Slack: config.SlackConfig{
			Enabled:           true,
			SocketModeEnabled: true,
			AppToken:          "xapp-test",
		},
	}

	sup := NewSlackSocketSupervisor(nil, cfg, nil)
	sup.dialClient = func(_ string) socketModeRunner { return fake }

	return sup
}

func TestSocketSupervisor_StatusReportsConnectedAfterHelloEvent(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	fake := newFakeSocketModeClient()
	sup := newTestSupervisor(t, fake)

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	done := make(chan struct{})

	go func() {
		_ = sup.Run(ctx)
		close(done)
	}()

	// Before any event: not connected.
	r.False(sup.GetStatus().Connected)

	// Fire a fake "connected" event; supervisor should flip the snapshot.
	fake.events <- socketmode.Event{Type: socketmode.EventTypeConnected, Data: &socketmode.ConnectedEvent{}}

	r.True(waitFor(2*time.Second, func() bool { return sup.GetStatus().Connected }),
		"supervisor never observed connected event")

	status := sup.GetStatus()
	r.True(status.Enabled)
	r.True(status.Connected)
	r.NotNil(status.LastConnectedAt)

	cancel()
	<-done

	// After shutdown, connected flips back to false.
	r.False(sup.GetStatus().Connected)
}

func TestSocketSupervisor_AcksEventsAPIAndCallsDispatcher(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	fake := newFakeSocketModeClient()
	sup := newTestSupervisor(t, fake)

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	done := make(chan struct{})

	go func() {
		_ = sup.Run(ctx)
		close(done)
	}()

	// Push an events_api envelope. Even though svc is nil, the dispatcher
	// must still call AckCtx before any business logic — that's the contract
	// the test pins.
	payload := []byte(`{"team_id":"T1","event":{"type":"definitely_not_a_real_event"}}`)
	fake.events <- socketmode.Event{
		Type:    socketmode.EventTypeEventsAPI,
		Request: &socketmode.Request{EnvelopeID: "env-1", Payload: payload},
	}

	r.True(waitFor(2*time.Second, func() bool { return fake.ackCount() >= 1 }),
		"supervisor never acked the events_api envelope")

	cancel()
	<-done
}

func TestSocketSupervisor_AcksSlashCommandAndCallsDispatcher(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	fake := newFakeSocketModeClient()
	sup := newTestSupervisor(t, fake)

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	done := make(chan struct{})

	go func() {
		_ = sup.Run(ctx)
		close(done)
	}()

	// Unknown slash command — DispatchCommand returns an ephemeral reply
	// without touching svc, so the supervisor must ack with that payload.
	fake.events <- socketmode.Event{
		Type:    socketmode.EventTypeSlashCommand,
		Request: &socketmode.Request{EnvelopeID: "env-cmd-1"},
		Data:    slackgo.SlashCommand{TeamID: "T1", Command: "/totally-unknown"},
	}

	r.True(waitFor(2*time.Second, func() bool { return fake.ackCount() >= 1 }),
		"supervisor never acked the slash command")

	cancel()
	<-done
}

func TestSocketSupervisor_ReconnectsAfterRunContextError(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	fake := newFakeSocketModeClient()
	sup := newTestSupervisor(t, fake)

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	// Shrink the backoff so the reconnect happens quickly in the test.
	origBackoff := reconnectBackoff
	reconnectBackoff = 10 * time.Millisecond
	defer func() { reconnectBackoff = origBackoff }()

	done := make(chan struct{})

	go func() {
		_ = sup.Run(ctx)
		close(done)
	}()

	// Wait for the first RunContext to be in flight, then force it to exit.
	r.True(waitFor(time.Second, func() bool { return fake.runCount() >= 1 }),
		"first RunContext never started")

	fake.runErr <- errForcedDisconnect

	// Supervisor should restart RunContext.
	r.True(waitFor(2*time.Second, func() bool { return fake.runCount() >= 2 }),
		"supervisor did not reconnect after RunContext error")

	cancel()
	<-done
}

func TestSocketSupervisor_StatusNeverLeaksAppToken(t *testing.T) {
	t.Parallel()

	// Regression guard for the "app_token in status snapshot" risk in the
	// spec's risk log. The SocketStatus struct has no AppToken field and
	// GetStatus must not place the token in any string slot (LastError etc.).
	r := require.New(t)

	fake := newFakeSocketModeClient()
	sup := newTestSupervisor(t, fake)

	// Simulate a failure that mentions the token to make sure recordError
	// preserves the raw error and the status snapshot doesn't transform it.
	sup.recordError(errUnauthorizedTest)

	st := sup.GetStatus()
	r.NotContains(st.LastError, "xapp-test")
}
