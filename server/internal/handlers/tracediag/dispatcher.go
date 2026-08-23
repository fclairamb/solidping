// Package tracediag routes and runs the MTR-style path capture the incident
// pipeline asks for when a check goes down on a network-reachability failure
// (spec 2026-08-21-10).
//
// It is where three concerns that do NOT belong in the incident state machine
// live: the per-organization rate limit, the choice of WHERE to trace from, and
// the goroutine that makes the whole thing asynchronous.
package tracediag

import (
	"context"
	"log/slog"
	"net"
	"time"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/handlers/attachments"
	"github.com/fclairamb/solidping/server/internal/handlers/incidents"
	"github.com/fclairamb/solidping/server/internal/nettrace"
)

// AttachmentWriter is the storage side. Small on purpose, exactly like
// incidents.AttachmentStore: this package must not be able to fail an incident.
type AttachmentWriter interface {
	// PutIncidentTraceroute stores (replacing any previous one) the serialized
	// capture for an incident.
	PutIncidentTraceroute(
		ctx context.Context, orgUID, incidentUID string, capture []byte, details models.JSONMap,
	) (string, error)
}

// AgentTraceSender is the deported-agent side: it asks the agent that produced
// the failing result to run the trace ITSELF and upload the capture.
//
// It reports whether the request was actually handed to a live connection.
// That boolean is the routing decision — see Dispatcher.RequestTrace.
type AgentTraceSender interface {
	// SendTraceRequest queues a trace-request frame for a worker's live agent
	// connection. False means there was none, and the request is dropped.
	SendTraceRequest(ctx context.Context, workerUID string, req *incidents.TraceRequest) bool
}

// LocalWorkerResolver answers "is this worker one THIS process runs?".
//
// Without it the dispatcher cannot tell a result produced by the in-process
// worker from one produced by a deported agent that happens to be offline, and
// the two need opposite handling: trace locally, or do nothing. Tracing from
// the master for a check that failed in another region would produce a path the
// probe never took, which is worse than no capture at all.
type LocalWorkerResolver interface {
	// IsLocalWorker reports whether workerUID belongs to this process.
	IsLocalWorker(workerUID string) bool
}

// LocalWorkerFunc adapts a function to LocalWorkerResolver.
type LocalWorkerFunc func(workerUID string) bool

// IsLocalWorker implements LocalWorkerResolver.
func (f LocalWorkerFunc) IsLocalWorker(workerUID string) bool { return f(workerUID) }

// Dispatcher implements incidents.TraceRequester.
type Dispatcher struct {
	cfg     config.TracerouteConfig
	store   AttachmentWriter
	agents  AgentTraceSender
	local   LocalWorkerResolver
	limiter *orgLimiter
	logger  *slog.Logger

	// run is the trace itself, swapped in tests. Production is nettrace.Run.
	run func(ctx context.Context, opts *nettrace.Options) (*nettrace.Capture, error)
	// started, when non-nil, is signaled after each dispatched trace finishes.
	// Tests only — production leaves it nil and never waits for anything.
	done chan struct{}
}

// New builds a dispatcher. store may be nil (no file storage), in which case
// nothing is ever traced — there would be nowhere to put the result.
func New(cfg config.TracerouteConfig, store AttachmentWriter, logger *slog.Logger) *Dispatcher {
	if logger == nil {
		logger = slog.Default()
	}

	return &Dispatcher{
		cfg:     cfg,
		store:   store,
		limiter: newOrgLimiter(cfg.Limit),
		logger:  logger,
		run:     nettrace.Run,
	}
}

// SetAgentSender wires the deported-agent route. Optional.
func (d *Dispatcher) SetAgentSender(sender AgentTraceSender) { d.agents = sender }

// SetLocalWorkerResolver wires the "did this process run the check?" test.
// Optional: with no resolver, nothing is ever traced locally.
func (d *Dispatcher) SetLocalWorkerResolver(resolver LocalWorkerResolver) { d.local = resolver }

// RequestTrace implements incidents.TraceRequester.
//
// IT RETURNS IMMEDIATELY, ALWAYS. The failing result has already been written
// and the incident has already opened by the time this is called; a trace takes
// up to the whole budget, and nothing about the incident may wait on it.
func (d *Dispatcher) RequestTrace(ctx context.Context, req *incidents.TraceRequest) {
	if !d.cfg.Enabled {
		return
	}

	address := net.ParseIP(req.Failure.Address)
	if address == nil {
		return
	}

	// The rate limit is charged BEFORE routing, so a mass outage cannot be
	// answered by hundreds of concurrent sweeps whether they run here or on a
	// hundred different agents.
	if !d.limiter.allow(req.OrgUID, time.Now()) {
		d.logger.DebugContext(ctx, "traceroute rate limit reached for organization",
			"organization_uid", req.OrgUID, "incident_uid", req.IncidentUID)

		return
	}

	// ROUTING. The agent that produced the result is the only host whose path
	// to the target is the one that failed, so it gets first refusal; the
	// in-process worker is the other case; anything else is dropped rather
	// than traced from the wrong vantage point.
	if d.agents != nil && d.agents.SendTraceRequest(ctx, req.WorkerUID, req) {
		return
	}

	if d.local == nil || !d.local.IsLocalWorker(req.WorkerUID) {
		d.logger.DebugContext(ctx, "no vantage point for a path trace",
			"worker_uid", req.WorkerUID, "incident_uid", req.IncidentUID)

		return
	}

	if d.store == nil {
		return
	}

	//nolint:contextcheck // detaching from the caller context is the point (see dispatchLocal)
	d.dispatchLocal(req, address)
}

// dispatchLocal runs the capture on this host, off the caller's goroutine.
func (d *Dispatcher) dispatchLocal(req *incidents.TraceRequest, address net.IP) {
	go func() {
		// A FRESH CONTEXT, deliberately detached from the caller's. The
		// incident pipeline's context belongs to the result submission that is
		// already finishing; inheriting it would cancel the trace the moment
		// the HTTP request or the worker's job context ends — which is to say,
		// immediately and always.
		ctx, cancel := context.WithTimeout(context.WithoutCancel(context.Background()), d.cfg.Budget)
		defer cancel()

		d.runAndStore(ctx, req, address)

		if d.done != nil {
			d.done <- struct{}{}
		}
	}()
}

// runAndStore performs the capture and writes it as the incident's attachment.
//
// EVERY failure here is logged at debug/warn and swallowed. The incident is
// already open and correct; a missing path capture is a papercut.
func (d *Dispatcher) runAndStore(ctx context.Context, req *incidents.TraceRequest, address net.IP) {
	capture, err := d.run(ctx, &nettrace.Options{
		Host:    req.Failure.Host,
		Address: address,
		Port:    req.Failure.Port,
		Rounds:  d.cfg.Rounds,
		MaxHops: d.cfg.Hops,
		Budget:  d.cfg.Budget,
	})
	if err != nil {
		// ErrNoModeAvailable is the ordinary unprivileged-container case, not a
		// fault: debug, not warn, or every incident on such a host logs noise.
		d.logger.DebugContext(ctx, "path trace produced nothing",
			"incident_uid", req.IncidentUID, "error", err)

		return
	}

	// Region and trigger are stamped HERE, from the incident pipeline's view of
	// the persisted result — never from anything the tracer decided about
	// itself.
	capture.Region = req.Region
	capture.Trigger = req.Trigger

	body, err := capture.Marshal()
	if err != nil {
		d.logger.WarnContext(ctx, "path trace did not serialize",
			"incident_uid", req.IncidentUID, "error", err)

		return
	}

	details := models.JSONMap{
		attachments.DetailKeyTrigger:  req.Trigger,
		attachments.DetailKeyCheckUID: req.CheckUID,
		attachments.DetailKeyCapturedAt: capture.StartedAt.UTC().
			Format(time.RFC3339),
	}

	if req.Region != "" {
		details[attachments.DetailKeyRegion] = req.Region
	}

	if _, err := d.store.PutIncidentTraceroute(ctx, req.OrgUID, req.IncidentUID, body, details); err != nil {
		d.logger.WarnContext(ctx, "failed to store path trace",
			"incident_uid", req.IncidentUID, "error", err)
	}
}
