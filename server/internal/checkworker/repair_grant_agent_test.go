package checkworker

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/agents"
	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/checkers/checkjs"
	jsconfig "github.com/fclairamb/solidping/server/internal/checkers/checkjs/config"
	"github.com/fclairamb/solidping/server/internal/checkworker/backend"
	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/crypto/credentials"
	"github.com/fclairamb/solidping/server/internal/db/models"
)

// agentGrantServer is a fake master: it hands one job to the agent, then
// records the result frame it gets back.
type agentGrantServer struct {
	job    agents.AgentJob
	mu     sync.Mutex
	result *agents.ClientFrame
	done   chan struct{}
}

func (s *agentGrantServer) serve(writer http.ResponseWriter, req *http.Request) {
	conn, err := websocket.Accept(writer, req, &websocket.AcceptOptions{CompressionMode: websocket.CompressionDisabled})
	if err != nil {
		return
	}

	ctx := req.Context()
	hello := agents.ServerFrame{Type: agents.MsgTypeHello, AgentUID: "agent-1", WorkerUID: "worker-1", Region: "r"}

	if req.Header.Get("Authorization") != "" {
		first := hello
		first.Protocol = agents.ProtocolVersion
		var enroll agents.ClientFrame

		if wsjson.Write(ctx, conn, first) != nil || wsjson.Read(ctx, conn, &enroll) != nil ||
			wsjson.Write(ctx, conn, agents.ServerFrame{
				Type: agents.MsgTypeEnrolled, ID: enroll.ID, AgentUID: "agent-1",
				WorkerUID: "worker-1", Region: "r", Fingerprint: "fp",
			}) != nil {
			return
		}
	}

	if wsjson.Write(ctx, conn, hello) != nil {
		return
	}

	for {
		var frame agents.ClientFrame
		if wsjson.Read(ctx, conn, &frame) != nil {
			return
		}

		switch frame.Type {
		case agents.MsgTypeClaim:
			_ = wsjson.Write(ctx, conn, agents.ServerFrame{
				Type: agents.MsgTypeJobs, ID: frame.ID, Jobs: []agents.AgentJob{s.job},
			})
		case agents.MsgTypeResult:
			s.mu.Lock()
			s.result = &frame
			s.mu.Unlock()
			close(s.done)

			_ = wsjson.Write(ctx, conn, agents.ServerFrame{Type: agents.MsgTypeAck, ID: frame.ID})

			_ = conn.Close(websocket.StatusNormalClosure, "done")

			return
		default:
		}
	}
}

// runAgentGrantJob runs an auto-repaired js check (grant over the stored
// script and env) through the real agent claim: the stored config holds a
// `${param:tag}` reference, the sealed envelope holds the overlay the server
// resolved, and the agent merges it over the config before executeJob.
func runAgentGrantJob(t *testing.T, script string, env map[string]any, overlayKey string) *agents.ClientFrame {
	t.Helper()
	r := require.New(t)

	keys, err := agents.GenerateAgentKeys()
	r.NoError(err)

	plainEnv := map[string]string{}
	for key, value := range env {
		plainEnv[key], _ = value.(string)
	}

	stored := map[string]any{
		"script": script,
		"env":    env,
		"ai": map[string]any{
			"repair":       "auto",
			"repair_hosts": []any{"127.0.0.1"},
			"repair_grant": jsconfig.RepairDigest(script, plainEnv),
		},
	}

	overlay := map[string]any{"secrets": map[string]any{"TOKEN": "s3cr3t-token-value"}}

	switch overlayKey {
	case "script":
		overlay["script"] = strings.ReplaceAll(script, "${param:tag}", "resolved-tag")
	default:
		resolvedEnv := map[string]any{}
		for key, value := range env {
			text, _ := value.(string)
			resolvedEnv[key] = strings.ReplaceAll(text, "${param:tag}", "resolved-tag")
		}

		overlay["env"] = resolvedEnv
	}

	sealed, err := credentials.SealForRecipients([]string{keys.X25519Recipient}, overlay)
	r.NoError(err)

	due := time.Now()
	region := "r"
	fake := &agentGrantServer{
		done: make(chan struct{}),
		job: agents.AgentJob{
			UID: "job-1", CheckUID: "check-1", Type: string(checkerdef.CheckTypeJS),
			Config: stored, ConfigSealed: &sealed, Period: "1m", ScheduledAt: &due, Region: &region,
		},
	}

	srv := httptest.NewServer(http.HandlerFunc(fake.serve))
	t.Cleanup(srv.Close)

	wsb := backend.NewWSBackend(srv.URL, "spe_token", "agent", &backend.Identity{AgentKeys: *keys}, nil,
		backend.WithPingInterval(400*time.Millisecond))
	runner := NewAgentCheckWorker(&config.Config{}, wsb)
	runner.setWorker(models.NewWorker("agent-w", "Agent"))

	ctx, cancel := context.WithCancel(context.Background())
	defer func() { cancel(); time.Sleep(1500 * time.Millisecond) }()

	jobs, _, err := wsb.ClaimJobs(ctx, "worker-1", nil, 1, 1, 0)
	r.NoError(err)
	r.Len(jobs, 1)
	r.NoError(runner.executeJob(ctx, runner.logger, jobs[0]))

	<-fake.done

	fake.mu.Lock()
	defer fake.mu.Unlock()

	return fake.result
}

// Regression (spec 2026-10-25-01): on a deported agent the sealed envelope
// carries the resolved `${param:…}` values and is merged over the job config
// before executeJob. The repair grant is decided on the config as claimed, so
// the granted host still runs up and any other host is still refused.
func TestRepairGrantHoldsOnTheAgentPathWithAParamReference(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		suffix     string
		env        map[string]any
		overlayKey string
	}{
		"param in the script": {suffix: "\n// run ${param:tag}", overlayKey: "script"},
		"param in env":        {env: map[string]any{"TAG": "${param:tag}"}, overlayKey: "env"},
	}

	for name, tc := range cases {
		for _, fire := range []bool{false, true} {
			t.Run(fmt.Sprint(name, " fire=", fire), func(t *testing.T) {
				t.Parallel()
				r := require.New(t)

				acmePort, acmeHits := repairSink(t)
				evilPort, evilHits := repairSink(t)

				env := map[string]any{"FIRE": "no"}
				if fire {
					env["FIRE"] = "yes"
				}

				for key, value := range tc.env {
					env[key] = value
				}

				frame := runAgentGrantJob(t, grantScript(acmePort, evilPort)+tc.suffix, env, tc.overlayKey)
				r.NotNil(frame)

				if fire {
					r.Equal(int(checkerdef.StatusError), frame.Status, "%v", frame.Output)
					r.Equal(true, frame.Output[checkjs.OutputKeyRepairRefused])
					r.Zero(evilHits.Load(), "the refused host received nothing")
				} else {
					r.Equal(int(checkerdef.StatusUp), frame.Status, "%v", frame.Output)
					r.Zero(evilHits.Load())
				}

				r.Equal(int32(1), acmeHits.Load(), "the granted host was reached")
			})
		}
	}
}
