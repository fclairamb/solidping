package checkerdef

import "time"

// Diagnostics carries optional, opt-in operational evidence about a check
// execution — what the probe actually saw at the moment it decided the target
// was unhealthy.
//
// It is DELIBERATELY NOT part of Result.Output. Output is persisted verbatim
// as the `results.output` JSONB column on every single execution, and raw
// result rows are reaped by the aggregation job 24 h later; writing a response
// body there would cost thousands of kilobyte-scale rows per flapping check
// per day for evidence nobody keeps. Diagnostics instead rides the wire next
// to Output and is persisted ONLY when the incident pipeline decides this
// result opened (or reopened) an incident — the object a human actually opens
// three days later.
//
// Everything here is `omitempty` in both directions, so an agent that predates
// the field simply sends nothing and a new agent talking to an older server
// loses nothing but the capture.
type Diagnostics struct {
	// FailureResponse is the captured response of a FAILED probe. Nil unless
	// the check opted in (HTTP: `capture_failure_response`) AND a response
	// actually existed — a timeout, DNS failure or TLS error produces no
	// response, so the capture degrades to absent and the existing error
	// output remains the only evidence.
	FailureResponse *FailureResponse `json:"failureResponse,omitempty"`

	// Screenshot is the capture of what a FAILING browser check's page looked
	// like. Nil unless the check opted in (browser: `screenshot`) and the
	// capture succeeded.
	//
	// See Screenshot's own doc comment for why its bytes never cross the agent
	// WS control channel.
	Screenshot *Screenshot `json:"screenshot,omitempty"`

	// NetworkFailure states that this probe failed to REACH its target, and
	// names the endpoint it was dialing (spec 2026-08-21-10). It is set at
	// transport-error sites only, which is what makes "an application-level
	// failure never triggers a path trace" a property of where the field is
	// written rather than of a string match on an error message.
	//
	// It is the only member of Diagnostics that is not opt-in per check: it
	// costs a few dozen bytes and only ever exists on a failing probe. The
	// opt-in is on the ACTION it enables (`tracerouteOnFailure`), decided
	// server-side at the incident transition — see NetworkFailure's doc.
	NetworkFailure *NetworkFailure `json:"networkFailure,omitempty"`
}

// Screenshot is a PNG capture of the page a failing browser check was looking
// at, taken before the browser context is disposed.
//
// HONESTY ABOUT WHAT THIS IS: it is what the page looked like a moment AFTER
// the check decided the target was unhealthy, not the frame at the instant of
// failure. Every surface that renders it must say so — presenting it as "the
// failure" invites an operator to conclude the wrong thing from a page that
// finished loading half a second later.
//
// THE BYTES NEVER CROSS THE CONTROL CHANNEL. Diagnostics is serialized onto
// the agent WebSocket result frame (internal/agents/protocol.go), which is
// JSON: a megabyte PNG would become a multi-megabyte base64 blob on the socket
// every agent uses to claim work. PNG is therefore `json:"-"` — a deported
// agent uploads its bytes out-of-band to POST /api/v1/agent/attachments and
// advertises only the marker fields below. The IN-PROCESS worker path keeps
// the bytes in memory and never serializes them at all, which is why the field
// works there with no wire representation.
type Screenshot struct {
	// PNG is the raw image. NEVER SERIALIZED — see the type doc.
	PNG []byte `json:"-"`
	// CapturedAt is when the screenshot was taken.
	CapturedAt time.Time `json:"capturedAt,omitzero"`
	// Available is the agent-side MARKER: "I hold a capture for this result".
	// It is what crosses the wire in place of the bytes, so the server can ask
	// for the upload if (and only if) this result opens an incident.
	Available bool `json:"available,omitempty"`
	// CaptureID names the capture in the agent's local LRU, so the server's
	// upload request can identify which one it wants.
	CaptureID string `json:"captureId,omitempty"`
	// Region is the probing region. Filled SERVER-SIDE from the persisted
	// result row, never by the checker — a deported agent must not be the
	// authority on where it ran.
	Region string `json:"region,omitempty"`
}

// FailureResponse is the textual capture of the response a failing probe
// received. It is bounded by construction (see the caps in the capturing
// checker) so no storage quota work is needed.
//
// SECURITY: this may carry internal hostnames, stack traces or PII from the
// monitored service, which is exactly why it is opt-in per check and why it
// must never reach a public surface (status pages, subscriber payloads). The
// REQUEST side is never captured at all — request headers carry the check's
// own credentials.
type FailureResponse struct {
	// URL is the URL the probe requested.
	URL string `json:"url,omitempty"`
	// StatusLine is the response status line, e.g. "HTTP/2.0 503 Service Unavailable".
	StatusLine string `json:"statusLine,omitempty"`
	// StatusCode is the numeric response status.
	StatusCode int `json:"statusCode,omitempty"`
	// Headers are the RESPONSE headers with sensitive values replaced by the
	// redaction marker. Multi-valued headers are joined with ", ".
	Headers map[string]string `json:"headers,omitempty"`
	// Body is the captured response body, truncated to the capture cap. Empty
	// when Binary is true — a non-text or non-UTF-8 body is never stored raw.
	Body string `json:"body,omitempty"`
	// Truncated reports that Body holds only the leading bytes of a larger body.
	Truncated bool `json:"truncated,omitempty"`
	// ContentLength is the response's declared Content-Length, or -1 when the
	// response did not declare one (chunked). It is what tells a reader how
	// much a truncated Body is missing.
	ContentLength int64 `json:"contentLength,omitempty"`
	// ContentType is the raw Content-Type response header.
	ContentType string `json:"contentType,omitempty"`
	// BodyBytes is the size in bytes of the body the probe actually read.
	BodyBytes int `json:"bodyBytes,omitempty"`
	// BodySHA256 is the hex SHA-256 of the bytes the probe read. It is the
	// only identity a binary body gets, and it lets two captures be compared
	// without storing either.
	BodySHA256 string `json:"bodySha256,omitempty"`
	// Binary reports that the body was not text-like or not valid UTF-8, so
	// only metadata (ContentType, BodyBytes, BodySHA256) was kept.
	Binary bool `json:"binary,omitempty"`
	// CapturedAt is when the probe captured this response.
	CapturedAt time.Time `json:"capturedAt,omitzero"`
	// RemoteAddr is the address the probe actually connected to, when the
	// transport reported one.
	RemoteAddr string `json:"remoteAddr,omitempty"`
	// Region is the probing region. Filled SERVER-SIDE from the persisted
	// result row rather than by the checker, so it cannot be influenced by a
	// deported agent's own idea of where it runs.
	Region string `json:"region,omitempty"`
}
