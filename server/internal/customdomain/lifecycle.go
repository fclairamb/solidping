// Package customdomain holds the lifecycle state machine for a status page's
// custom domain: how a domain earns the right to be served, how it degrades
// when re-verification starts failing, and how it earns that right back.
//
// It lives on its own, away from both the sweep job and the status-page
// service, for two reasons. The transition is a pure function of the stored
// state plus one observation, so it is directly testable without a database, a
// resolver or a job runner. And BOTH callers — the periodic sweep and the
// operator's synchronous "Verify" button — must agree on it: a manual verify
// that hard-demoted a page the sweep would only have put in grace was one of
// the ways a status page went dark for a blip.
//
// The states are defined in internal/db/models (CustomDomainState*), next to
// the column they are stored in.
package customdomain

import (
	"time"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

// Thresholds of the state machine. All three are deliberately asymmetric:
// degrading is cheap and reversible, going dark is expensive and slow, and
// coming back is slower still.
const (
	// GraceAfterFailures is the consecutive-failure count at which a verified
	// domain enters `grace`. It KEEPS SERVING there — this is the whole point.
	// At the 6h sweep cadence that is ~18h of sustained failure before the
	// state even changes name, and a visitor never notices.
	GraceAfterFailures = 3

	// HardDemoteAfterFailures is the consecutive-failure count at which the
	// verification is finally cleared and the domain stops being served. At
	// the 6h cadence that is ~3 days of uninterrupted failure. A domain that
	// was genuinely released or transferred away crosses it; a DNS blip, a
	// resolver outage or a registrar hiccup does not.
	//
	// The takeover protection this replaces fired at 3. Widening it trades a
	// slower reaction to a genuinely released domain (which is only a problem
	// if someone else has ALSO already re-pointed it at us and claimed it —
	// the global unique index on custom_domain is the actual arbiter there)
	// against not taking a paying customer's status page offline for a blip.
	HardDemoteAfterFailures = 12

	// RepromoteSuccesses is how many CONSECUTIVE successful re-checks a
	// demoted domain needs before the sweep serves it again. More than one on
	// purpose: re-promotion must be harder to earn than demotion was to
	// trigger, so a single lucky lookup against a hijacked resolver is not
	// enough.
	RepromoteSuccesses = 3
)

// State is the stored half of a domain's lifecycle — everything the sweep
// reads off the status_pages row.
type State struct {
	// Lifecycle is one of models.CustomDomainState*.
	Lifecycle string
	// VerifiedAt is the serving gate: non-nil means the domain is served.
	// `grace` keeps it non-nil; only a hard demotion clears it.
	VerifiedAt *time.Time
	// Failures / Successes are the two consecutive-run counters. At most one
	// of them is ever non-zero.
	Failures  int
	Successes int
	// GraceSince is when degradation started; nil outside grace.
	GraceSince *time.Time
}

// Observation is one re-verification result plus the facts the decision needs
// that do not live on the row.
type Observation struct {
	// OK is whether the CNAME check passed in the configured mode.
	OK bool
	// CertValid reports whether an unexpired certificate for this hostname is
	// still held in tls_storage. It gates RE-PROMOTION only: a domain we no
	// longer hold a certificate for is one we have not served in a long time,
	// and it must go back through an operator's Verify rather than
	// resurrecting itself.
	CertValid bool
	// Now is the clock, injected so transitions are deterministic in tests.
	Now time.Time
}

// Outcome is the next State plus the transitions that just happened, so the
// caller can act on them (page the operator, write an audit event) without
// re-deriving them by comparing before and after.
type Outcome struct {
	State
	// EnteredGrace is true on the transition INTO grace, not while in it.
	EnteredGrace bool
	// HardDemoted is true on the transition that cleared VerifiedAt. This is
	// the one that must alert a human: the page has gone dark.
	HardDemoted bool
	// Repromoted is true when a demoted domain earned its way back.
	Repromoted bool
	// Recovered is true when a domain in grace passed a check again — the
	// blip ended, and nobody outside ever saw it.
	Recovered bool
}

// Normalize fills in a lifecycle state for a row written before the state
// column existed (or by a caller that left it blank), deriving it from the
// columns that already encoded the same information. Without this, every
// upgraded installation's domains would start the first sweep in an unknown
// state and be treated as `pending`, which would make them ineligible for
// re-promotion forever — the exact bug this state machine exists to fix.
func Normalize(current State, hasDomain, everChecked bool) State {
	if models.ValidCustomDomainState(current.Lifecycle) &&
		current.Lifecycle != models.CustomDomainStateNone {
		return current
	}

	switch {
	case !hasDomain:
		current.Lifecycle = models.CustomDomainStateNone
	case current.VerifiedAt != nil:
		current.Lifecycle = models.CustomDomainStateActive
	case everChecked:
		// Configured, checked at least once, and not verified: this row was
		// demoted by the old one-way sweep. Treating it as `demoted` rather
		// than `pending` is what lets it recover on its own.
		current.Lifecycle = models.CustomDomainStateDemoted
	default:
		current.Lifecycle = models.CustomDomainStatePending
	}

	return current
}

// Next applies one observation to a state and returns the next one.
func Next(current State, obs Observation) Outcome {
	if obs.OK {
		return onSuccess(current, obs)
	}

	return onFailure(current, obs)
}

// onSuccess handles a passing re-check.
func onSuccess(current State, obs Observation) Outcome {
	out := Outcome{State: current}
	out.Failures = 0
	out.Successes = current.Successes + 1

	// Still served (active or grace): one success is enough to end the
	// degradation, because nothing was lost — the page never stopped serving.
	if current.VerifiedAt != nil {
		out.Recovered = current.Lifecycle == models.CustomDomainStateGrace
		out.Lifecycle = models.CustomDomainStateActive
		out.GraceSince = nil
		out.Successes = 0

		return out
	}

	// Not served. Only a domain that WAS ours may earn its way back: a page
	// that has never verified stays pending until an operator clicks Verify,
	// so a hostname someone parked a CNAME on cannot bootstrap itself into
	// being served.
	if current.Lifecycle != models.CustomDomainStateDemoted {
		return out
	}

	if out.Successes < RepromoteSuccesses || !obs.CertValid {
		return out
	}

	repromotedAt := obs.Now
	out.VerifiedAt = &repromotedAt
	out.Lifecycle = models.CustomDomainStateActive
	out.Successes = 0
	out.GraceSince = nil
	out.Repromoted = true

	return out
}

// onFailure handles a failing re-check.
func onFailure(current State, obs Observation) Outcome {
	out := Outcome{State: current}
	out.Successes = 0
	out.Failures = current.Failures + 1

	// Already not served — nothing left to demote. The counter keeps running
	// so the diagnostic stays honest.
	if current.VerifiedAt == nil {
		out.GraceSince = nil

		return out
	}

	switch {
	case out.Failures >= HardDemoteAfterFailures:
		out.VerifiedAt = nil
		out.Lifecycle = models.CustomDomainStateDemoted
		out.GraceSince = nil
		out.HardDemoted = true
	case out.Failures >= GraceAfterFailures:
		out.EnteredGrace = current.Lifecycle != models.CustomDomainStateGrace
		out.Lifecycle = models.CustomDomainStateGrace

		if out.GraceSince == nil {
			since := obs.Now
			out.GraceSince = &since
		}
	default:
		out.Lifecycle = models.CustomDomainStateActive
		out.GraceSince = nil
	}

	return out
}
