package checkerdef

import (
	"context"
	"errors"
	"net"
	"strings"
	"syscall"
)

// Network-reachability failure classes (spec 2026-08-21-10).
//
// These name the ways a probe can fail to REACH its target — the cases where a
// path trace has something to say. They deliberately do NOT cover
// application-level failures (an HTTP 500, a keyword mismatch, a certificate
// about to expire): the target answered, the path is fine, and a traceroute
// would only add noise to the incident.
const (
	// NetFailureConnectTimeout is a connect that never completed: no SYN-ACK,
	// no refusal, nothing — the classic silent drop of a firewall or a black
	// hole somewhere on the path.
	NetFailureConnectTimeout = "connect-timeout"
	// NetFailureConnectionRefused is an RST: something answered, and said no.
	// The path is reachable; the trace still tells you which hops it crossed.
	NetFailureConnectionRefused = "connection-refused"
	// NetFailureNetworkUnreachable is ICMP/errno "network unreachable" — a
	// routing failure, usually local or one hop out.
	NetFailureNetworkUnreachable = "network-unreachable"
	// NetFailureHostUnreachable is ICMP/errno "host unreachable".
	NetFailureHostUnreachable = "host-unreachable"
	// NetFailureICMPTimeout is an ICMP check whose echoes all went unanswered.
	NetFailureICMPTimeout = "icmp-timeout"
	// NetFailureICMPUnreachable is an ICMP check that got a destination
	// unreachable back rather than silence.
	NetFailureICMPUnreachable = "icmp-unreachable"
	// NetFailureTLSHandshakeTimeout is a TCP connection that came up and then
	// stalled inside the TLS handshake — a middlebox, an MTU black hole, or a
	// server that accepts and never speaks.
	NetFailureTLSHandshakeTimeout = "tls-handshake-timeout"
)

// NetworkFailure is the checker's statement that this failure was about
// REACHING the target, plus the exact endpoint it was trying to reach.
//
// It is what makes "trace only network failures" a structural property rather
// than a string match on an error message: it is set at transport-error sites
// and nowhere else, so a check that got a response — any response — never
// carries one.
//
// UNLIKE Screenshot's bytes, this DOES ride the agent WS result frame. It is a
// few dozen bytes of scalars, and the server needs it to decide whether to ask
// for a trace at all.
//
// Address is the IP the probe actually dialed, not the configured hostname.
// That is deliberate: it is what lets the trace follow the same IP family the
// check pinned via `ipVersion`, and what stops a round-robin DNS record from
// producing a trace to a different machine than the one that failed.
type NetworkFailure struct {
	// Class is one of the NetFailure* constants.
	Class string `json:"class"`
	// Host is the configured hostname, for display. May be empty, and may be
	// the same as Address when the check was configured with a literal IP.
	Host string `json:"host,omitempty"`
	// Address is the resolved IP the probe dialed.
	Address string `json:"address,omitempty"`
	// Port is the TCP/UDP port the probe dialed. Zero for ICMP checks, which
	// is exactly what makes the TCP fallback prober unusable for them.
	Port int `json:"port,omitempty"`
}

// NewNetworkFailure builds a marker, or nil when class is empty.
//
// Returning nil for an unclassified error is the whole point: a caller wires
// this into its error path unconditionally and gets a marker only for the
// classes that warrant a trace.
func NewNetworkFailure(class, host, address string, port int) *NetworkFailure {
	if class == "" {
		return nil
	}

	return &NetworkFailure{Class: class, Host: host, Address: address, Port: port}
}

// ClassifyDialError maps a failed dial to a reachability class, or "" when the
// error is not one.
//
// timedOut is the caller's own answer to "did MY deadline fire?" — a probe
// context that expired is a connect timeout even when the underlying error is
// a bare `context.DeadlineExceeded` with no syscall attached.
//
// DNS failures classify as "" on purpose. A name that does not resolve has no
// address to trace to, and resolution diagnostics are a different capture with
// its own spec.
func ClassifyDialError(err error, timedOut bool) string {
	if err == nil && !timedOut {
		return ""
	}

	// A resolution failure is not a reachability failure. Checked FIRST because
	// a DNSError can also report Timeout() == true, which would otherwise be
	// misfiled as a connect timeout.
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return ""
	}

	switch {
	case errors.Is(err, syscall.ECONNREFUSED):
		return NetFailureConnectionRefused
	case errors.Is(err, syscall.ENETUNREACH):
		return NetFailureNetworkUnreachable
	case errors.Is(err, syscall.EHOSTUNREACH):
		return NetFailureHostUnreachable
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, syscall.ETIMEDOUT):
		return NetFailureConnectTimeout
	}

	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return NetFailureConnectTimeout
	}

	// Text fallback. errors.Is against syscall constants is the authority on
	// every platform SolidPing ships on, but a wrapped error that lost its
	// syscall along the way (or a platform whose errno set differs) should
	// still classify rather than silently produce no trace.
	if class := classifyErrorText(err); class != "" {
		return class
	}

	if timedOut {
		return NetFailureConnectTimeout
	}

	return ""
}

// ClassifyTLSHandshakeError maps a failure that happened AFTER the TCP
// connection came up.
//
// Only the stall is a reachability class. A certificate that is expired,
// self-signed, or for the wrong name is an application-level answer — the
// server is right there and talking — and must never trigger a trace.
func ClassifyTLSHandshakeError(err error, timedOut bool) string {
	if timedOut {
		return NetFailureTLSHandshakeTimeout
	}

	if err == nil {
		return ""
	}

	if errors.Is(err, context.DeadlineExceeded) {
		return NetFailureTLSHandshakeTimeout
	}

	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return NetFailureTLSHandshakeTimeout
	}

	return ""
}

// classifyErrorText is the last-resort textual match. Kept narrow and
// lowercase-normalized; anything it does not recognize stays unclassified.
func classifyErrorText(err error) string {
	if err == nil {
		return ""
	}

	text := strings.ToLower(err.Error())

	switch {
	case strings.Contains(text, "connection refused"):
		return NetFailureConnectionRefused
	case strings.Contains(text, "network is unreachable"):
		return NetFailureNetworkUnreachable
	case strings.Contains(text, "no route to host"), strings.Contains(text, "host is unreachable"):
		return NetFailureHostUnreachable
	case strings.Contains(text, "i/o timeout"), strings.Contains(text, "timeout"):
		return NetFailureConnectTimeout
	default:
		return ""
	}
}

// SetNetworkFailure hangs a marker on a result, allocating Diagnostics only
// when there is something to hang. A nil failure is a no-op, so callers can
// pass the result of NewNetworkFailure straight through.
func (r *Result) SetNetworkFailure(failure *NetworkFailure) {
	if r == nil || failure == nil {
		return
	}

	if r.Diagnostics == nil {
		r.Diagnostics = &Diagnostics{}
	}

	r.Diagnostics.NetworkFailure = failure
}

// LocateNetworkFailure fills in the endpoint on a marker a lower layer already
// classified.
//
// The split exists because the two facts are known in different places: the
// dial helper sees the error, and only the caller that did the name resolution
// knows which address it handed that helper. A result with no marker is left
// alone — this never invents one.
func LocateNetworkFailure(r *Result, host, address string, port int) {
	if r == nil || r.Diagnostics == nil || r.Diagnostics.NetworkFailure == nil {
		return
	}

	r.Diagnostics.NetworkFailure.Host = host
	r.Diagnostics.NetworkFailure.Address = address
	r.Diagnostics.NetworkFailure.Port = port
}

// DropNetworkFailure removes a marker a lower layer recorded.
//
// Its one caller is the tunneled probe path: the failure is real, but it
// happened on the far side of a bastion, so a trace run from this worker would
// describe a route the probe never took. Better no evidence than misleading
// evidence.
func DropNetworkFailure(r *Result) {
	if r == nil || r.Diagnostics == nil {
		return
	}

	r.Diagnostics.NetworkFailure = nil
}
