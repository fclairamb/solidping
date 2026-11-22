package agentmode

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/agents"
	"github.com/fclairamb/solidping/server/internal/config"
)

// upgradeServer is a fake master: it accepts the agent's signed reconnect,
// hands out one slow HTTP job, and sends `upgrade` once that job is running.
type upgradeServer struct {
	mu            sync.Mutex
	upgradeSentAt time.Time
	claimsAfter   int
	results       []agents.ClientFrame
	jobGiven      bool
}

func (s *upgradeServer) serve(t *testing.T, targetURL string, jobStarted <-chan struct{}) http.Handler {
	t.Helper()

	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		conn, err := websocket.Accept(w, req, nil)
		if err != nil {
			return
		}
		defer func() { _ = conn.CloseNow() }()

		ctx := context.Background()

		if wsjson.Write(ctx, conn, agents.ServerFrame{
			Type: agents.MsgTypeHello, AgentUID: "agent-uid-1", WorkerUID: "worker-1", Region: "test-1",
			ServerVersion: "1.2.3",
		}) != nil {
			return
		}

		var writeMu sync.Mutex

		write := func(frame agents.ServerFrame) {
			writeMu.Lock()
			defer writeMu.Unlock()

			_ = wsjson.Write(ctx, conn, frame)
		}

		go func() {
			<-jobStarted

			s.mu.Lock()
			s.upgradeSentAt = time.Now()
			s.mu.Unlock()

			write(agents.ServerFrame{Type: agents.MsgTypeUpgrade, TargetVersion: "1.2.3"})
		}()

		for {
			var frame agents.ClientFrame
			if wsjson.Read(ctx, conn, &frame) != nil {
				return
			}

			switch frame.Type {
			case agents.MsgTypeClaim:
				write(s.claim(frame.ID, targetURL))
			case agents.MsgTypeResult:
				s.mu.Lock()
				s.results = append(s.results, frame)
				s.mu.Unlock()
				write(agents.ServerFrame{Type: agents.MsgTypeAck, ID: frame.ID})
			default:
			}
		}
	})
}

// claim answers a claim: the one job the first time, nothing afterwards, with
// a short retry hint so a still-claiming agent would show up quickly.
func (s *upgradeServer) claim(id, targetURL string) agents.ServerFrame {
	s.mu.Lock()
	defer s.mu.Unlock()

	if !s.upgradeSentAt.IsZero() && time.Since(s.upgradeSentAt) > 200*time.Millisecond {
		s.claimsAfter++
	}

	if s.jobGiven || targetURL == "" {
		return agents.ServerFrame{Type: agents.MsgTypeJobs, ID: id, RetryInMs: 50}
	}

	s.jobGiven = true
	now := time.Now()

	return agents.ServerFrame{Type: agents.MsgTypeJobs, ID: id, RetryInMs: 50, Jobs: []agents.AgentJob{{
		UID: "job-1", CheckUID: "check-1", Type: "http",
		Config: map[string]any{"url": targetURL},
		Period: "1m", ScheduledAt: &now,
	}}}
}

// TestAgentDrainsAndExitsOnUpgrade: on `upgrade` the agent stops claiming,
// submits the result of the job it was running, and Run returns nil (exit 0).
func TestAgentDrainsAndExitsOnUpgrade(t *testing.T) {
	t.Chdir(t.TempDir())
	r := require.New(t)

	jobStarted := make(chan struct{})

	var startOnce sync.Once

	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		startOnce.Do(func() { close(jobStarted) })
		time.Sleep(700 * time.Millisecond) // the job is in flight when `upgrade` lands
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(target.Close)

	fake := &upgradeServer{}
	master := httptest.NewServer(fake.serve(t, target.URL, jobStarted))
	t.Cleanup(master.Close)

	_, encoded := newTestIdentity(t)

	cfg := &config.Config{}
	cfg.Agent.Keys = encoded
	cfg.Agent.Name = "acme-agent"
	cfg.Agent.ServerURL = master.URL
	cfg.Agent.RewrapDisabled = true

	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()

	done := make(chan error, 1)

	go func() { done <- Run(ctx, cfg) }()

	select {
	case err := <-done:
		r.NoError(err, "an upgrade request ends Run cleanly: exit 0")
	case <-ctx.Done():
		r.FailNow("Run did not return after the upgrade request")
	}

	r.NoError(ctx.Err(), "Run returned because of the upgrade, not the test deadline")

	fake.mu.Lock()
	defer fake.mu.Unlock()

	r.False(fake.upgradeSentAt.IsZero(), "the upgrade was sent")
	r.Len(fake.results, 1, "the in-flight job's result is submitted before exiting")
	r.Equal("job-1", fake.results[0].JobUID)
	r.Zero(fake.claimsAfter, "no claim after the upgrade request")
}

// fakeUpgradeSource and fakeLoop drive runUntilUpgrade alone.
type fakeUpgradeSource struct {
	ch     chan struct{}
	closed bool
}

func (f *fakeUpgradeSource) UpgradeRequested() <-chan struct{} { return f.ch }
func (f *fakeUpgradeSource) Close()                            { f.closed = true }

type fakeLoop struct{}

func (fakeLoop) Run(ctx context.Context) error {
	<-ctx.Done()

	return ctx.Err()
}

// TestRunUntilUpgradeKeepsShutdownError: a shutdown (ctx canceled) without an
// upgrade request still reports the loop's error, and leaves the socket alone.
func TestRunUntilUpgradeKeepsShutdownError(t *testing.T) {
	t.Parallel()

	source := &fakeUpgradeSource{ch: make(chan struct{})}

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	err := runUntilUpgrade(ctx, bufferLogger(&bytes.Buffer{}), fakeLoop{}, source)
	require.ErrorIs(t, err, context.Canceled)
	require.False(t, source.closed)
}
