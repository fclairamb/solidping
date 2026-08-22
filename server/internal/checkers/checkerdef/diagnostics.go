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

	// Screenshot is the page capture of a FAILED browser check (opt-in:
	// `screenshot`). Nil on every other path.
	//
	// `json:"-"`, NOT `omitempty` — see the Screenshot doc comment. This is
	// the single line that keeps a megabyte-scale PNG out of the agent's JSON
	// control channel; an agent-side capture travels over the dedicated
	// upload endpoint (spec 2026-08-21-01 §6) instead.
	Screenshot *Screenshot `json:"-"`
}

// Screenshot is a PNG capture of what a failing browser check's page looked
// like, taken before the browser context is disposed.
//
// It exists in MEMORY ONLY until the incident pipeline decides this result
// opened or reopened an incident; every other failing run's capture is
// dropped on the floor. That is what keeps a flapping 30 s check from writing
// 2,880 blobs a day.
//
// SECURITY: a rendered page can carry anything the monitored service shows —
// customer data, session-bound content, internal hostnames. Like
// FailureResponse it is opt-in per check and must never reach a public
// surface (status pages, subscriber payloads).
type Screenshot struct {
	// PNG is the raw image. Bounded by MaxScreenshotBytes at capture time; an
	// over-cap capture is DROPPED, never truncated (a truncated PNG is not an
	// image, it is a corrupt file that renders as a broken icon).
	PNG []byte
	// CapturedAt is when the probe took the shot. Necessarily a moment AFTER
	// failure detection, which is why every surface that shows it labels it
	// as such rather than as the failure frame.
	CapturedAt time.Time
}

// MaxScreenshotBytes caps a single capture. Deliberately tighter than
// files.MaxFileSize (25 MB): a full-page PNG of a real error page is tens to
// a few hundred KB, and anything past a few MB is a pathological page whose
// image is worth less than the memory it would hold on every worker.
const MaxScreenshotBytes = 4 * 1024 * 1024

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
