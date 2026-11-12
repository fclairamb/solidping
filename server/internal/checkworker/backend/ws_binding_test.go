package backend_test

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/agents"
	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/checkers/registry"
	"github.com/fclairamb/solidping/server/internal/checkworker/backend"
	"github.com/fclairamb/solidping/server/internal/crypto/credentials"
	"github.com/fclairamb/solidping/server/internal/db/models"
)

// bindingServer serves one claim with the given wire jobs and records the
// error results the agent submits.
type bindingServer struct {
	*fakeAgentServer

	mu      sync.Mutex
	results []agents.ClientFrame
}

func newBindingServer(t *testing.T, jobs []agents.AgentJob) *bindingServer {
	t.Helper()

	fake := &bindingServer{fakeAgentServer: newFakeAgentServer(t, "@dc1")}
	fake.handle(func(_ int, conn *websocket.Conn) {
		for {
			var frame agents.ClientFrame
			if err := wsjson.Read(context.Background(), conn, &frame); err != nil {
				return
			}

			switch frame.Type {
			case agents.MsgTypeClaim:
				_ = wsjson.Write(context.Background(), conn,
					agents.ServerFrame{Type: agents.MsgTypeJobs, ID: frame.ID, Jobs: jobs})
			case agents.MsgTypeResult:
				fake.mu.Lock()
				fake.results = append(fake.results, frame)
				fake.mu.Unlock()

				_ = wsjson.Write(context.Background(), conn, agents.ServerFrame{Type: agents.MsgTypeAck, ID: frame.ID})
			default:
			}
		}
	})

	return fake
}

func (f *bindingServer) errorResults() []string {
	f.mu.Lock()
	defer f.mu.Unlock()

	out := make([]string, 0, len(f.results))
	for _, frame := range f.results {
		msg, _ := frame.Output[checkerdef.OutputKeyError].(string)
		out = append(out, msg)
	}

	return out
}

// browserBlob seals plain the way dash0 does, for the agent's key, and wraps
// it in a stored browser envelope.
func browserBlob(t *testing.T, recipient string, plain map[string]any) string {
	t.Helper()

	fps, ct, err := credentials.SealForClient([]string{recipient}, plain)
	require.NoError(t, err)

	envelope, err := credentials.BuildClientSealedEnvelope(fps, ct)
	require.NoError(t, err)

	return envelope
}

func httpTargetFields(t *testing.T) []string {
	t.Helper()

	cfg, ok := registry.ParseConfig(checkerdef.CheckTypeHTTP)
	require.True(t, ok)

	return credentials.TargetFieldsFor(cfg)
}

// claimWith runs one claim of these wire jobs on a fresh agent sealed to by
// sealFor, and returns the jobs handed to the worker and the error results.
func claimWith(
	t *testing.T, build func(recipient string) []agents.AgentJob, opts ...backend.Option,
) ([]*models.CheckJob, []string) {
	t.Helper()

	keys, err := agents.GenerateAgentKeys()
	require.NoError(t, err)

	fake := newBindingServer(t, build(keys.X25519Recipient))
	wsBackend := backend.NewWSBackend(fake.server.URL, "spe_token", "agent",
		&backend.Identity{AgentKeys: *keys}, nil, opts...)

	worker, err := wsBackend.Register(t.Context(), nil)
	require.NoError(t, err)

	jobs, _, err := wsBackend.ClaimJobs(t.Context(), worker.UID, nil, 5, 5, time.Minute)
	require.NoError(t, err)

	return jobs, fake.errorResults()
}

func TestTunnelKeyMatchesCheckerdef(t *testing.T) {
	t.Parallel()
	require.Equal(t, checkerdef.TunnelCheckUIDConfigKey, credentials.TunnelConfigKey)
}

// TestClaimRefusesSecretsSealedForAnotherTarget is the attack of spec
// 2026-11-11-01: the server points a sealed HTTP check at a host it controls.
// The agent must not run the check, so the secret never reaches that host.
func TestClaimRefusesSecretsSealedForAnotherTarget(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	var evilHits atomic.Int32

	evil := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { evilHits.Add(1) }))
	t.Cleanup(evil.Close)

	fields := httpTargetFields(t)
	original := map[string]any{"url": "https://db.acme.com"}

	jobs, results := claimWith(t, func(recipient string) []agents.AgentJob {
		blob := browserBlob(t, recipient, credentials.WithBinding(
			map[string]any{"basicAuth": "alice:hunter2"}, "http", fields, original))

		return []agents.AgentJob{{
			UID: "job-1", CheckUID: "check-1", Type: "http", Period: "1m",
			Config: map[string]any{"url": evil.URL}, ConfigSealed: &blob,
		}}
	})

	r.Empty(jobs, "the retargeted job must not reach the checker")
	r.Equal([]string{credentials.ErrSealedForAnotherTarget.Error()}, results)
	r.Zero(evilHits.Load(), "nothing may be sent to the new host")
}

// TestClaimRunsSecretsOnTheirOwnTarget is the positive control: the same blob
// on its original URL runs, and the checker receives the secret without the
// binding.
func TestClaimRunsSecretsOnTheirOwnTarget(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	fields := httpTargetFields(t)

	jobs, results := claimWith(t, func(recipient string) []agents.AgentJob {
		blob := browserBlob(t, recipient, credentials.WithBinding(
			map[string]any{"basicAuth": "alice:hunter2"}, "http", fields,
			map[string]any{"url": "https://db.acme.com"}))

		return []agents.AgentJob{{
			UID: "job-1", CheckUID: "check-1", Type: "http", Period: "1m",
			// Same target written differently, plus a non-target change.
			Config: map[string]any{"url": "HTTPS://db.acme.com:443", "timeout": "9s"}, ConfigSealed: &blob,
		}}
	})

	r.Empty(results)
	r.Len(jobs, 1)
	r.Equal("alice:hunter2", jobs[0].Config["basicAuth"])
	r.NotContains(jobs[0].Config, credentials.BindingKey)
	r.Equal("HTTPS://db.acme.com:443", jobs[0].Config["url"])
}

// TestClaimRefusesABlobOverridingAPublicField: a blob may only carry secret
// fields of the type, so it can never replace the public URL.
func TestClaimRefusesABlobOverridingAPublicField(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	fields := httpTargetFields(t)
	public := map[string]any{"url": "https://db.acme.com"}

	for name, plain := range map[string]map[string]any{
		"bound": credentials.WithBinding(
			map[string]any{"basicAuth": "a:b", "url": "https://evil.example"}, "http", fields, public),
		"unbound": {"basicAuth": "a:b", "url": "https://evil.example"},
	} {
		jobs, results := claimWith(t, func(recipient string) []agents.AgentJob {
			blob := browserBlob(t, recipient, plain)

			return []agents.AgentJob{{
				UID: "job-1", CheckUID: "check-" + name, Type: "http", Period: "1m",
				Config: public, ConfigSealed: &blob,
			}}
		})

		r.Empty(jobs, name)
		r.Len(results, 1, name)
		r.Contains(results[0], credentials.ErrSealedKeyNotSecret.Error(), name)
	}
}

// TestClaimAcceptsLegacyUnboundBlobWithAWarning: a browser blob sealed before
// the binding still runs for now (resolved open question), with one warning
// per check.
func TestClaimAcceptsLegacyUnboundBlobWithAWarning(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	handler := newCapturingHandler()

	jobs, results := claimWith(t, func(recipient string) []agents.AgentJob {
		blob := browserBlob(t, recipient, map[string]any{"basicAuth": "alice:hunter2"})

		return []agents.AgentJob{{
			UID: "job-1", CheckUID: "check-legacy-unbound", Type: "http", Period: "1m",
			Config: map[string]any{"url": "https://anywhere.acme.com"}, ConfigSealed: &blob,
		}}
	}, backend.WithLogger(slog.New(handler)))

	r.Empty(results)
	r.Len(jobs, 1)
	r.Equal("alice:hunter2", jobs[0].Config["basicAuth"])
	r.True(handler.hasWarn("sealed secrets are not bound to their target: seal them again (Re-seal page) to bind them"))
}

// agentBlob seals plain the way an agent's peer re-wrap does and stores it
// over a server envelope: what the server keeps (spec 2026-11-12-03).
func agentBlob(t *testing.T, recipient string, plain map[string]any) string {
	t.Helper()

	serverEnvelope, err := credentials.SealForRecipients([]string{recipient}, map[string]any{"basicAuth": "a:b"})
	require.NoError(t, err)

	fps, ct, err := credentials.SealForClient([]string{recipient}, plain)
	require.NoError(t, err)

	envelope, err := credentials.RewrapSealedEnvelope(serverEnvelope, fps, ct)
	require.NoError(t, err)
	require.Equal(t, credentials.SealOriginAgent, credentials.SealedOrigin(envelope))

	return envelope
}

// TestClaimRefusesAnAgentBlobOverridingAPublicField: a server blob re-sealed
// by a peer agent may only carry secret fields of the type (spec
// 2026-11-12-03), so the agent that re-sealed it cannot inject a URL.
func TestClaimRefusesAnAgentBlobOverridingAPublicField(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	jobs, results := claimWith(t, func(recipient string) []agents.AgentJob {
		blob := agentBlob(t, recipient, map[string]any{"basicAuth": "a:b", "url": "https://evil.example"})

		return []agents.AgentJob{{
			UID: "job-1", CheckUID: "check-agent-blob", Type: "http", Period: "1m",
			Config: map[string]any{"url": "https://db.acme.com"}, ConfigSealed: &blob,
		}}
	})

	r.Empty(jobs)
	r.Len(results, 1)
	r.Contains(results[0], credentials.ErrSealedKeyNotSecret.Error())
}

// TestClaimRunsAgentAndServerBlobsWithSecretsOnly is the positive control: an
// agent blob, and a server blob, with only secret fields run, without the
// legacy-unbound warning.
func TestClaimRunsAgentAndServerBlobsWithSecretsOnly(t *testing.T) {
	t.Parallel()

	for name, seal := range map[string]func(*testing.T, string, map[string]any) string{
		"agent": agentBlob,
		"server": func(t *testing.T, recipient string, plain map[string]any) string {
			t.Helper()

			envelope, err := credentials.SealForRecipients([]string{recipient}, plain)
			require.NoError(t, err)

			return envelope
		},
	} {
		r := require.New(t)
		handler := newCapturingHandler()

		jobs, results := claimWith(t, func(recipient string) []agents.AgentJob {
			blob := seal(t, recipient, map[string]any{"basicAuth": "alice:hunter2"})

			return []agents.AgentJob{{
				UID: "job-1", CheckUID: "check-" + name, Type: "http", Period: "1m",
				Config: map[string]any{"url": "https://db.acme.com"}, ConfigSealed: &blob,
			}}
		}, backend.WithLogger(slog.New(handler)))

		r.Empty(results, name)
		r.Len(jobs, 1, name)
		r.Equal("alice:hunter2", jobs[0].Config["basicAuth"], name)
		r.False(handler.hasWarn("sealed secrets are not bound to their target: seal them again (Re-seal page) to bind them"), name)
	}
}
