package agentws_test

import (
	"net/http"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/stretchr/testify/require"

	agentcrypto "github.com/fclairamb/solidping/server/internal/agents"
	"github.com/fclairamb/solidping/server/internal/db/models"
)

// reconnectBoot dials a signed reconnect carrying bootID (none when empty)
// and reads the hello frame, after which any lease release has happened.
func (e *env) reconnectBoot(keys *agentcrypto.AgentKeys, agentUID, nonce, bootID string) *websocket.Conn {
	e.t.Helper()
	r := require.New(e.t)

	timestamp := time.Now().UTC().Format(time.RFC3339)
	signature, err := keys.Sign(http.MethodGet, "/api/v1/agent/ws", timestamp, nonce)
	r.NoError(err)

	headers := http.Header{}
	headers.Set("X-Sp-Agent-Uid", agentUID)
	headers.Set("X-Sp-Timestamp", timestamp)
	headers.Set("X-Sp-Nonce", nonce)
	headers.Set("X-Sp-Signature", signature)

	if bootID != "" {
		headers.Set(agentcrypto.HeaderBootID, bootID)
	}

	conn, _, err := e.dial(headers)
	r.NoError(err)
	e.t.Cleanup(func() { _ = conn.CloseNow() })

	var hello agentcrypto.ServerFrame
	r.NoError(wsjson.Read(e.t.Context(), conn, &hello))
	r.Equal(agentcrypto.MsgTypeHello, hello.Type)

	return conn
}

func (e *env) workerBootID(workerUID string) *string {
	e.t.Helper()

	row := new(models.Worker)
	require.NoError(e.t, e.dbSvc.DB().NewSelect().Model(row).Where("uid = ?", workerUID).Scan(e.t.Context()))

	return row.BootID
}

// TestAgentRestartFreesLeasesReconnectKeepsThem covers spec 2026-11-14-03 on
// the agent path: a reconnect from the same process (same boot ID) keeps its
// in-flight lease and its late result is accepted, an agent that sends no
// boot ID keeps today's behavior, and a new process frees the leases its
// predecessor left so its first claim gets the job back.
func TestAgentRestartFreesLeasesReconnectKeepsThem(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	e := newEnv(t)
	ctx := t.Context()

	check := e.createCheck("web", []string{testRegion})

	conn, keys, enrolled := e.enrollBoot(e.mintToken(), "dc1-agent", &websocket.DialOptions{}, "boot-1")
	r.NotEmpty(enrolled.WorkerUID)

	bootID := e.workerBootID(enrolled.WorkerUID)
	r.NotNil(bootID, "the enroll frame's boot ID is stored")
	r.Equal("boot-1", *bootID)

	jobs := roundTrip(t, conn, agentcrypto.ClientFrame{Type: agentcrypto.MsgTypeClaim, ID: "c1", MaxJobs: 5})
	r.Len(jobs.Jobs, 1)
	jobUID := jobs.Jobs[0].UID

	// Network blip: the same process reconnects with the same boot ID.
	_ = conn.Close(websocket.StatusNormalClosure, "blip")
	conn2 := e.reconnectBoot(keys, enrolled.AgentUID, "nonce-1", "boot-1")

	job := e.jobFor(check.UID)
	r.NotNil(job.LeaseWorkerUID, "a reconnect from the same process keeps its in-flight lease")
	r.Equal(enrolled.WorkerUID, *job.LeaseWorkerUID)

	ack := roundTrip(t, conn2, agentcrypto.ClientFrame{
		Type:   agentcrypto.MsgTypeResult,
		ID:     "r1",
		JobUID: jobUID,
		Status: int(models.ResultStatusUp),
		Output: map[string]any{},
	})
	r.Equal(agentcrypto.MsgTypeAck, ack.Type, "the late result of the same process is still accepted")

	// The old process leased the job again before it died: due, valid lease.
	leaseAgain := func() {
		due := time.Now().Add(-2 * time.Second)
		_, err := e.dbSvc.DB().NewUpdate().Model((*models.CheckJob)(nil)).
			Set("scheduled_at = ?", due).
			Set("effective_scheduled_at = ?", due).
			Set("lease_worker_uid = ?", enrolled.WorkerUID).
			Set("lease_expires_at = ?", time.Now().Add(time.Hour)).
			Set("lease_starts = 1").
			Set("updated_at = ?", time.Now().Add(-time.Second)).
			Where("uid = ?", jobUID).
			Exec(ctx)
		r.NoError(err)
	}

	leaseAgain()
	_ = conn2.Close(websocket.StatusNormalClosure, "bye")

	// An agent too old to send a boot ID: no release, as before.
	conn3 := e.reconnectBoot(keys, enrolled.AgentUID, "nonce-2", "")
	r.NotNil(e.jobFor(check.UID).LeaseWorkerUID, "no boot ID: the lease waits for expiry")
	_ = conn3.Close(websocket.StatusNormalClosure, "bye")

	// The agent restarted: a new process, a new boot ID.
	conn4 := e.reconnectBoot(keys, enrolled.AgentUID, "nonce-3", "boot-2")

	job = e.jobFor(check.UID)
	r.Nil(job.LeaseWorkerUID, "a new process frees the leases its predecessor left")
	r.Nil(job.LeaseExpiresAt)
	r.Equal("boot-2", *e.workerBootID(enrolled.WorkerUID))

	again := roundTrip(t, conn4, agentcrypto.ClientFrame{Type: agentcrypto.MsgTypeClaim, ID: "c2", MaxJobs: 5})
	r.Len(again.Jobs, 1, "the restarted agent claims the job at once")
	r.Equal(jobUID, again.Jobs[0].UID)
}
