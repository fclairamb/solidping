// Package egressreport turns this process's live egress self-probe into the
// shape the worker row stores, for the in-process worker and the deported agent
// alike (spec 2026-08-15-11).
//
// It exists as its own package purely to keep the dependency direction clean:
// checkerdef owns the route lookup and must not know about database models,
// models must not know about checkers, and both the checkworker loop and the WS
// backend need the bridge.
package egressreport

import (
	"time"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/db/models"
)

// cacheTTL bounds how often the route lookup actually runs. The report is
// produced on every heartbeat and every agent claim frame; a fresh lookup each
// time would be pointless churn for an answer that changes approximately never.
// Kept well under one heartbeat interval so "probed at report time, not at
// process start" stays true — a host that gains or loses IPv6 converges without
// a restart.
const cacheTTL = 30 * time.Second

// shared is the process-wide cache. One worker process has one egress.
var shared = checkerdef.NewEgressCache(cacheTTL)

// Current probes (or reuses a fresh probe of) this host's egress families and
// renders them for the worker row.
//
// This value is a HINT for region selection. It never gates execution: the
// per-run pre-flight in checkerdef stays the authority, so a host whose IPv6
// route came back runs immediately and one that lost it still fails with
// ErrWorkerNoEgress instead of a false DOWN.
func Current() models.WorkerEgress {
	return From(shared.Get())
}

// From renders a probe result as the stored capability. Every family present in
// the map yields a non-nil pointer; a family the probe did not answer for stays
// nil, i.e. "unknown", never "no".
func From(families map[checkerdef.IPVersion]bool) models.WorkerEgress {
	out := models.WorkerEgress{}

	if v, ok := families[checkerdef.IPVersionIPv4]; ok {
		out.IPv4 = &v
	}

	if v, ok := families[checkerdef.IPVersionIPv6]; ok {
		out.IPv6 = &v
	}

	return out
}
