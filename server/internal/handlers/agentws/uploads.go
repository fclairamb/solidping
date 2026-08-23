package agentws

import (
	"context"
	"sync"
	"time"

	agentcrypto "github.com/fclairamb/solidping/server/internal/agents"
	"github.com/fclairamb/solidping/server/internal/handlers/incidents"
)

// The server->agent upload-request side of the agent protocol (spec
// 2026-08-21-05).
//
// A deported agent's binary captures cannot ride the JSON control channel, so a
// result frame carries only a marker. When (and only when) such a result opens
// or reopens an incident, the incident pipeline calls RequestScreenshotUpload
// and the agent POSTs the bytes to /api/v1/agent/attachments.
//
// EVERYTHING HERE IS BEST-EFFORT AND IN-PROCESS. There is no pending-request
// record and no retry: the incident pipeline runs synchronously inside the very
// result submission that arrived on the agent's socket, so in practice the
// connection is right here; and when it is not (agent reconnected, another
// replica is processing, agent shut down), the capture is simply lost. That is
// the spec's explicit decision — a missing screenshot is a papercut, a blocked
// or failed incident is an outage nobody is paged for.

// outboundBuffer is how many unsolicited server->agent frames may queue for one
// connection. Small on purpose: these are best-effort notifications, and a
// connection that is not draining them is a connection whose agent is not
// going to answer anyway.
const outboundBuffer = 4

// connRegistry maps a live agent connection's WORKER uid to its outbound frame
// queue.
//
// Keyed by worker uid rather than agent uid because that is the identity the
// result row carries (`results.worker_uid`), so the incident pipeline can
// address the connection without a lookup of its own.
type connRegistry struct {
	mu       sync.Mutex
	byWorker map[string]chan agentcrypto.ServerFrame
}

func newConnRegistry() *connRegistry {
	return &connRegistry{byWorker: map[string]chan agentcrypto.ServerFrame{}}
}

// add registers a connection and returns its outbound queue. A reconnect that
// races its own predecessor's teardown simply replaces the entry — see remove
// for why that is safe.
func (r *connRegistry) add(workerUID string) chan agentcrypto.ServerFrame {
	outbound := make(chan agentcrypto.ServerFrame, outboundBuffer)

	if workerUID == "" {
		return outbound
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	r.byWorker[workerUID] = outbound

	return outbound
}

// remove unregisters a connection, but ONLY if the registered queue is still
// this connection's own.
//
// The identity check is load-bearing: an agent that reconnects before its
// previous socket's read loop has noticed the drop would otherwise have its
// brand-new connection unregistered by the old one's deferred cleanup, and
// would silently stop receiving upload requests until it reconnected again.
func (r *connRegistry) remove(workerUID string, outbound chan agentcrypto.ServerFrame) {
	if workerUID == "" {
		return
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if current, ok := r.byWorker[workerUID]; ok && current == outbound {
		delete(r.byWorker, workerUID)
	}
}

// send queues one frame for a worker's live connection. It NEVER blocks:
// an unknown worker or a full queue is a drop, reported as false.
func (r *connRegistry) send(workerUID string, frame *agentcrypto.ServerFrame) bool {
	if workerUID == "" {
		return false
	}

	r.mu.Lock()
	outbound, ok := r.byWorker[workerUID]
	r.mu.Unlock()

	if !ok {
		return false
	}

	select {
	case outbound <- *frame:
		return true
	default:
		return false
	}
}

// RequestScreenshotUpload asks the agent behind workerUID to upload a capture it
// advertised. It implements incidents.AgentUploadRequester.
//
// Returns nothing, exactly like the publication hook: there is no failure mode
// the caller could act on, and the incident must not be able to notice.
//
// topic is caller-supplied and MUST be server-generated (the incident pipeline
// builds it from the incident row it just wrote). captureID is echoed from the
// agent's own marker, which is safe because it only ever indexes a map in that
// agent's memory.
func (h *Handler) RequestScreenshotUpload(ctx context.Context, workerUID, captureID, topic string) {
	if workerUID == "" || captureID == "" || topic == "" {
		return
	}

	if h.conns.send(workerUID, &agentcrypto.ServerFrame{
		Type:      agentcrypto.MsgTypeUploadRequest,
		CaptureID: captureID,
		Topic:     topic,
	}) {
		return
	}

	// No live connection on this replica, or its queue is backed up. The
	// capture is lost; the incident is already open regardless.
	h.logger.DebugContext(ctx, "no live agent connection for capture upload request",
		"worker_uid", workerUID, "topic", topic)
}

// SendTraceRequest asks the agent behind workerUID to RUN a path trace and
// upload the capture (spec 2026-08-21-10). It implements
// tracediag.AgentTraceSender.
//
// UNLIKE RequestScreenshotUpload THIS RETURNS A BOOLEAN, and the difference is
// not cosmetic. A screenshot the agent already holds is either delivered or
// lost, and the caller has nothing to decide. A trace has not run yet, so
// "there is no live connection for this worker" is genuinely actionable: it
// tells the dispatcher to consider tracing from THIS process instead — which it
// may only do when the worker is one of its own, because a trace from the wrong
// host describes a route the probe never took.
func (h *Handler) SendTraceRequest(
	ctx context.Context, workerUID string, req *incidents.TraceRequest,
) bool {
	if workerUID == "" || req.Topic == "" || req.Failure.Address == "" {
		return false
	}

	frame := agentcrypto.ServerFrame{
		Type:  agentcrypto.MsgTypeTraceRequest,
		Topic: req.Topic,
		Trace: &agentcrypto.TraceRequestFrame{
			Host:     req.Failure.Host,
			Address:  req.Failure.Address,
			Port:     req.Failure.Port,
			Rounds:   h.traceRounds,
			MaxHops:  h.traceMaxHops,
			BudgetMs: h.traceBudget.Milliseconds(),
		},
	}

	if h.conns.send(workerUID, &frame) {
		return true
	}

	h.logger.DebugContext(ctx, "no live agent connection for a path trace request",
		"worker_uid", workerUID, "topic", req.Topic)

	return false
}

// SetTraceSettings pins the trace parameters the server sends to agents, so the
// knobs live in one config rather than in every deployed agent's own flags.
func (h *Handler) SetTraceSettings(rounds, maxHops int, budget time.Duration) {
	h.traceRounds = rounds
	h.traceMaxHops = maxHops
	h.traceBudget = budget
}
