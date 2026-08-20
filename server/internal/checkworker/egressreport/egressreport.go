// Package egressreport turns this process's live egress self-probe into the
// shape the worker row stores, for the in-process worker and the deported agent
// alike (spec 2026-08-15-11).
//
// It exists as its own package purely to keep the dependency direction clean:
// checkerdef owns the route lookup and must not know about database models,
// models must not know about checkers, and both the checkworker loop and the WS
// backend need the bridge. Since spec 2026-08-19-03 it also carries the
// `browser` capability, which is the same kind of self-reported fact and rides
// the same carrier.
package egressreport

import (
	"context"
	"time"

	"github.com/fclairamb/solidping/server/internal/checkers/checkbrowser"
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
var shared = checkerdef.NewEgressCache(cacheTTL) //nolint:gochecknoglobals // deliberate process-lifetime cache

// Current probes (or reuses a fresh probe of) this host's egress families and
// browser backend, and renders them for the worker row.
//
// This value is a HINT for region selection. It never gates execution: the
// per-run pre-flight in checkerdef stays the authority, so a host whose IPv6
// route came back runs immediately and one that lost it still fails with
// ErrWorkerNoEgress instead of a false DOWN. Same for `browser`: the capability
// is advertised, never enforced.
func Current(ctx context.Context) []string {
	return WithBrowser(From(shared.Get()), checkbrowser.Available(ctx))
}

// From renders a probe result as the stored capability set: the names of the
// families this host HAS.
//
// THE NIL/EMPTY DISTINCTION IS LOAD-BEARING. A probe that answered for no
// family at all yields nil — "unknown", which must never be rendered as "no
// IPv6". A probe that answered and found nothing yields a non-nil EMPTY slice —
// a real "none". Everything else yields the subset that came back true, and
// absence from that non-nil slice means "no".
func From(families map[checkerdef.IPVersion]bool) []string {
	if len(families) == 0 {
		return nil
	}

	out := make([]string, 0, len(families))

	if families[checkerdef.IPVersionIPv4] {
		out = append(out, models.CapabilityIPv4)
	}

	if families[checkerdef.IPVersionIPv6] {
		out = append(out, models.CapabilityIPv6)
	}

	return out
}

// WithBrowser folds the browser verdict into an egress set.
//
// A NIL BASE STAYS NIL, even when a browser is available. The tri-state lives
// on the SET, not on each name: a non-nil set is closed, so appending "browser"
// to a nil (nothing-probed) set would silently turn ipv4/ipv6 from "unknown"
// into a hard "no" — the exact lie spec 2026-08-15-11 exists to prevent. An
// egress probe that answered nothing is a broken probe, and losing one
// advisory capability is far cheaper than mislabeling two.
func WithBrowser(base []string, hasBrowser bool) []string {
	if base == nil || !hasBrowser {
		return base
	}

	return append(base, models.CapabilityBrowser)
}
