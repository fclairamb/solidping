package checkhttp

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

const (
	// maxCaptureBodyBytes is the hard cap on a captured body. Together with
	// the status line and the redacted headers this bounds one capture at
	// roughly 20 KiB, which is what lets the feature ship with no storage
	// quota or entitlement work: a capture is persisted only on an incident
	// open/reopen, never per result.
	maxCaptureBodyBytes = 16 * 1024

	// redactedMarker replaces the value of any header that could carry a
	// credential or a session.
	redactedMarker = "[redacted]"

	// truncationMarker is appended to a body that was cut at the cap, so a
	// reader can never mistake a truncated page for the whole thing.
	truncationMarker = "\n…[truncated]"
)

// redactedHeaderNames are response headers whose value is ALWAYS replaced,
// matched case-insensitively on the whole name.
var redactedHeaderNames = map[string]struct{}{
	"set-cookie":          {},
	"authorization":       {},
	"proxy-authenticate":  {},
	"proxy-authorization": {},
	"www-authenticate":    {},
}

// redactedHeaderSubstrings are matched anywhere in a lowercased header name,
// which is what catches the long tail of vendor headers (`X-Api-Key`,
// `X-Amz-Security-Token`, `X-Client-Secret`, …).
var redactedHeaderSubstrings = []string{"token", "key", "secret"}

// textLikeContentTypes / textLikeContentTypeSuffixes gate whether a body may
// be stored as text at all. Anything else is treated as binary and reduced to
// metadata.
var textLikeContentTypes = map[string]struct{}{
	"application/json":                  {},
	"application/xml":                   {},
	"application/xhtml+xml":             {},
	"application/javascript":            {},
	"application/ecmascript":            {},
	"application/x-www-form-urlencoded": {},
	"application/problem+json":          {},
}

var textLikeContentTypeSuffixes = []string{"+json", "+xml"}

// shouldRedactHeader reports whether this response header's value must be
// replaced with redactedMarker.
func shouldRedactHeader(name string) bool {
	lower := strings.ToLower(name)

	if _, ok := redactedHeaderNames[lower]; ok {
		return true
	}

	for _, needle := range redactedHeaderSubstrings {
		if strings.Contains(lower, needle) {
			return true
		}
	}

	return false
}

// redactHeaders flattens the RESPONSE header map, joining multi-valued headers
// and replacing sensitive values. Request headers are never passed here — they
// carry the check's own credentials and are not captured at all.
func redactHeaders(header http.Header) map[string]string {
	if len(header) == 0 {
		return nil
	}

	out := make(map[string]string, len(header))

	for name, values := range header {
		if shouldRedactHeader(name) {
			out[name] = redactedMarker

			continue
		}

		out[name] = strings.Join(values, ", ")
	}

	return out
}

// isTextLikeContentType reports whether a Content-Type may be stored as text.
// An ABSENT content type is treated as text-like: the UTF-8 validity check
// downstream is the real guard, and refusing to capture an untyped text error
// page would lose the most common case of all.
func isTextLikeContentType(contentType string) bool {
	mediaType := strings.ToLower(strings.TrimSpace(contentType))
	if idx := strings.IndexByte(mediaType, ';'); idx >= 0 {
		mediaType = strings.TrimSpace(mediaType[:idx])
	}

	if mediaType == "" {
		return true
	}

	if strings.HasPrefix(mediaType, "text/") {
		return true
	}

	if _, ok := textLikeContentTypes[mediaType]; ok {
		return true
	}

	for _, suffix := range textLikeContentTypeSuffixes {
		if strings.HasSuffix(mediaType, suffix) {
			return true
		}
	}

	return false
}

// truncateUTF8 cuts b at no more than maxCaptureBodyBytes, backing off to the
// previous rune boundary so the stored string is always valid UTF-8. The
// second return reports whether anything was dropped.
func truncateUTF8(b []byte) (string, bool) {
	if len(b) <= maxCaptureBodyBytes {
		return string(b), false
	}

	cut := b[:maxCaptureBodyBytes]
	for len(cut) > 0 && !utf8.Valid(cut) {
		cut = cut[:len(cut)-1]
	}

	return string(cut) + truncationMarker, true
}

// buildFailureCapture assembles the capture for a FAILING probe that did get a
// response. It returns nil when the check did not opt in, or when there is no
// response to describe — both of which are ordinary, not errors.
//
// bodyBytes is the body the checker already read for its own matching (the
// whole point of this feature is that no second request and no extra I/O is
// needed); an empty slice simply yields a capture with no body.
func buildFailureCapture(cfg *HTTPConfig, resp *http.Response, bodyBytes []byte) *checkerdef.Diagnostics {
	if cfg == nil || !cfg.CaptureFailureResponse || resp == nil {
		return nil
	}

	sum := sha256.Sum256(bodyBytes)

	capture := &checkerdef.FailureResponse{
		URL:           cfg.URL,
		StatusLine:    statusLine(resp),
		StatusCode:    resp.StatusCode,
		Headers:       redactHeaders(resp.Header),
		ContentLength: resp.ContentLength,
		ContentType:   resp.Header.Get("Content-Type"),
		BodyBytes:     len(bodyBytes),
		BodySHA256:    hex.EncodeToString(sum[:]),
		CapturedAt:    time.Now().UTC(),
	}

	// Binary guard: a body that is not text-like by content type, or that is
	// not valid UTF-8, is reduced to metadata. Raw bytes are never stored —
	// they are unreadable in a JSONB column and would defeat the size cap.
	if len(bodyBytes) > 0 &&
		(!isTextLikeContentType(capture.ContentType) || !utf8.Valid(bodyBytes)) {
		capture.Binary = true

		return &checkerdef.Diagnostics{FailureResponse: capture}
	}

	capture.Body, capture.Truncated = truncateUTF8(bodyBytes)

	return &checkerdef.Diagnostics{FailureResponse: capture}
}

// statusLine rebuilds the response status line. net/http populates Status with
// "503 Service Unavailable"; Proto gives the "HTTP/2.0" half.
func statusLine(resp *http.Response) string {
	status := resp.Status
	if status == "" {
		status = http.StatusText(resp.StatusCode)
	}

	if resp.Proto == "" {
		return strings.TrimSpace(status)
	}

	return strings.TrimSpace(resp.Proto + " " + status)
}
