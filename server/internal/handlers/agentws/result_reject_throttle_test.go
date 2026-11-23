package agentws_test

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	agentcrypto "github.com/fclairamb/solidping/server/internal/agents"
	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/handlers/agentws"
)

const logResultRejected = "agent result rejected"

// testClock is a settable clock safe for the handler's goroutines.
type testClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *testClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.now
}

func (c *testClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.now = c.now.Add(d)
}

// jobLookupGuard wraps the database and counts GetCheckJobByUID calls, so a
// test can prove a frame was refused before any job lookup.
type jobLookupGuard struct {
	db.Service

	lookups atomic.Int64
	// forbid fails the test on any lookup when set.
	forbid atomic.Bool
	t      *testing.T
}

func (g *jobLookupGuard) GetCheckJobByUID(ctx context.Context, uid string) (*models.CheckJob, error) {
	g.lookups.Add(1)

	if g.forbid.Load() {
		g.t.Errorf("GetCheckJobByUID called with a %d-byte uid: the frame must be refused before any DB call", len(uid))
	}

	return g.Service.GetCheckJobByUID(ctx, uid)
}

// newRejectEnv is an env whose reject throttle runs on clock and whose logs
// and job lookups the test observes.
func newRejectEnv(t *testing.T) (*env, *logRecorder, *testClock, *jobLookupGuard) {
	t.Helper()

	logs := &logRecorder{}
	clock := &testClock{now: time.Date(2026, 11, 23, 0, 0, 0, 0, time.UTC)}
	guard := &jobLookupGuard{t: t}

	e := newEnvWrapped(t, nil, func(h *agentws.Handler) {
		h.SetLogger(slog.New(logs))
		h.SetRejectWarn(agentcrypto.NewDeprecationThrottleWith(time.Hour, 0, clock.Now))
	}, func(inner db.Service) db.Service {
		guard.Service = inner

		return guard
	})

	return e, logs, clock, guard
}

// TestRejectedResultFloodIsBounded: an agent streaming refused result frames,
// each with a fresh jobUid, used to add one throttle entry and one WARN line
// per frame (the key carried the jobUid). The throttle now holds one entry per
// (agent, reason) and writes one line per (agent, reason) per interval; the
// next line after the interval carries the suppressed count.
func TestRejectedResultFloodIsBounded(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	e, logs, clock, guard := newRejectEnv(t)

	conn, _, enrolled := e.enroll(e.mintToken(), "dc1-agent")

	const frames = 5000

	guard.forbid.Store(true)

	for i := range frames {
		resp := roundTrip(t, conn, agentcrypto.ClientFrame{
			Type:   agentcrypto.MsgTypeResult,
			ID:     "r",
			JobUID: uuid.NewString(),
			Status: 42,
		})
		r.Equal(agentcrypto.MsgTypeError, resp.Type, "frame %d", i)
		r.Equal("VALIDATION_ERROR", resp.Code)
	}

	r.Equal(1, e.handler.RejectWarnLen(), "one throttle entry per (agent, reason), not per jobUid")

	lines := logs.messages(logResultRejected)
	r.Len(lines, 1, "one WARN line per (agent, reason) per interval")
	r.Equal(enrolled.AgentUID, lines[0]["agent"])
	r.Equal("status out of range", lines[0]["reason"])
	r.Len(lines[0]["job_uid"], 36, "the first refusal is logged with its job UID")
	r.Equal("0", lines[0]["suppressed"])

	// Another reason for the same agent has its own budget: logged once.
	for range 3 {
		resp := roundTrip(t, conn, agentcrypto.ClientFrame{
			Type: agentcrypto.MsgTypeResult, ID: "r-bad-uid", JobUID: "job-1", Status: int(models.ResultStatusUp),
		})
		r.Equal("VALIDATION_ERROR", resp.Code)
	}

	r.Equal(2, e.handler.RejectWarnLen())

	lines = logs.messages(logResultRejected)
	r.Len(lines, 2)
	r.Equal("jobUid is not a job UID", lines[1]["reason"])
	r.Equal("job-1", lines[1]["job_uid"])

	// The next interval: one more line, carrying the swallowed count.
	clock.Advance(time.Hour)

	resp := roundTrip(t, conn, agentcrypto.ClientFrame{
		Type: agentcrypto.MsgTypeResult, ID: "r-next", JobUID: uuid.NewString(), Status: 42,
	})
	r.Equal(agentcrypto.MsgTypeError, resp.Type)

	lines = logs.messages(logResultRejected)
	r.Len(lines, 3)
	r.Equal("status out of range", lines[2]["reason"])
	r.Equal("4999", lines[2]["suppressed"])
	r.Zero(guard.lookups.Load(), "a status-refused frame never reaches the job lookup")
}

// TestMalformedJobUIDRefusedBeforeLookup: a jobUid that is not a canonical
// UUID (too long, or any other string) is refused with VALIDATION_ERROR
// before GetCheckJobByUID, and its log line keeps only a short prefix. The
// valid result afterwards is the positive control: the guard does let real
// lookups through and the result is recorded.
func TestMalformedJobUIDRefusedBeforeLookup(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	e, logs, _, guard := newRejectEnv(t)
	ctx := t.Context()

	check := e.createCheck("web", []string{testRegion})
	conn, _, _ := e.enroll(e.mintToken(), "dc1-agent")

	jobsResp := roundTrip(t, conn, agentcrypto.ClientFrame{Type: agentcrypto.MsgTypeClaim, ID: "c1", MaxJobs: 10})
	r.Len(jobsResp.Jobs, 1)

	guard.forbid.Store(true)

	for i, jobUID := range []string{
		strings.Repeat("a", 200_000),
		"job-1",
		"not-a-uuid-but-36-characters-long!!",
		"{" + jobsResp.Jobs[0].UID + "}",
	} {
		resp := roundTrip(t, conn, agentcrypto.ClientFrame{
			Type: agentcrypto.MsgTypeResult, ID: "bad", JobUID: jobUID, Status: int(models.ResultStatusUp),
		})
		r.Equal(agentcrypto.MsgTypeError, resp.Type, "malformed uid %d", i)
		r.Equal("VALIDATION_ERROR", resp.Code, "malformed uid %d", i)
		r.Equal("jobUid is not a job UID", resp.Title)
	}

	r.Zero(guard.lookups.Load())

	lines := logs.messages(logResultRejected)
	r.Len(lines, 1, "the same reason for the same agent is logged once")
	r.LessOrEqual(len(lines[0]["job_uid"]), 70, "the logged job UID is a short prefix")
	r.True(strings.HasPrefix(lines[0]["job_uid"], "aaaa"))

	// Positive control: a valid result for a job the agent owns is recorded.
	guard.forbid.Store(false)

	ack := roundTrip(t, conn, agentcrypto.ClientFrame{
		Type: agentcrypto.MsgTypeResult, ID: "ok", JobUID: jobsResp.Jobs[0].UID, Status: int(models.ResultStatusUp),
		Output: map[string]any{},
	})
	r.Equal(agentcrypto.MsgTypeAck, ack.Type, "got %q (%s)", ack.Type, ack.Title)
	r.Equal(int64(1), guard.lookups.Load())

	results, err := e.dbSvc.GetLastResultForChecks(ctx, e.org.UID, []string{check.UID})
	r.NoError(err)
	got, ok := results[check.UID]
	r.True(ok, "result row must exist")
	r.Equal(int(models.ResultStatusUp), *got.Status)
}
