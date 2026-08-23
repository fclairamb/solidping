package backend

import (
	"context"
	"net"
	"time"

	"github.com/fclairamb/solidping/server/internal/agents"
	"github.com/fclairamb/solidping/server/internal/nettrace"
)

// The agent half of traceroute-on-failure (spec 2026-08-21-10).
//
// WHY THE AGENT RUNS IT AND NOT THE SERVER: the whole value of a path trace is
// that it follows the route the failing probe took. A deported agent sits in a
// network the server has never seen — behind a VPN, inside a customer VPC, on
// the far side of an SD-WAN — so a trace run on the master would describe a
// completely different path and confidently mislabel it as the check's.
//
// Everything here is best-effort in both directions. No capability, a refused
// socket, a blown budget, or a rejected upload are all the same outcome: no
// path capture on that incident. None of them is retried, and none of them can
// affect a check, a result, or the incident.

// mimeTraceCapture is the content type a path capture is uploaded with.
const mimeTraceCapture = "application/json"

// traceUploadTimeout bounds the upload attempt AFTER the trace has finished. It
// is separate from the trace's own budget so a slow upload cannot eat the time
// the capture needed.
const traceUploadTimeout = 30 * time.Second

// handleTraceRequest runs one server-requested path trace and POSTs the result.
func (b *WSBackend) handleTraceRequest(ctx context.Context, frame *agents.ServerFrame) {
	if frame.Trace == nil || frame.Topic == "" {
		return
	}

	address := net.ParseIP(frame.Trace.Address)
	if address == nil {
		b.logger.DebugContext(ctx, "trace request carried no usable address",
			"address", frame.Trace.Address, "topic", frame.Topic)

		return
	}

	budget := time.Duration(frame.Trace.BudgetMs) * time.Millisecond
	if budget <= 0 {
		budget = nettrace.DefaultBudget
	}

	traceCtx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()

	capture, err := nettrace.Run(traceCtx, &nettrace.Options{
		Host:    frame.Trace.Host,
		Address: address,
		Port:    frame.Trace.Port,
		Rounds:  frame.Trace.Rounds,
		MaxHops: frame.Trace.MaxHops,
		Budget:  budget,
	})
	if err != nil {
		// The ordinary case here is an unprivileged container with no ICMP
		// socket and an ICMP check (so no port to fall back to). Debug, not
		// warn: it is a deployment shape, not a fault.
		b.logger.DebugContext(ctx, "path trace produced nothing",
			"topic", frame.Topic, "error", err)

		return
	}

	// Region and trigger are DELIBERATELY not set here. The server stamps them
	// from the persisted result row when it renders the attachment; an agent
	// must never be the authority on where it ran.
	body, err := capture.Marshal()
	if err != nil {
		b.logger.WarnContext(ctx, "path trace did not serialize", "topic", frame.Topic, "error", err)

		return
	}

	uploadCtx, uploadCancel := context.WithTimeout(ctx, traceUploadTimeout)
	defer uploadCancel()

	if err := b.uploadAttachment(uploadCtx, frame.Topic, mimeTraceCapture, body); err != nil {
		b.logger.WarnContext(ctx, "path trace upload failed",
			"topic", frame.Topic, "bytes", len(body), "error", err)

		return
	}

	b.logger.DebugContext(ctx, "path trace uploaded",
		"topic", frame.Topic, "mode", capture.Mode, "hops", len(capture.Hops))
}
