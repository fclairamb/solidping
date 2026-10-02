package checkerdef

import (
	"errors"
	"fmt"
	"strings"
)

// HTTPVersion is the HTTP protocol version an HTTP check requires the target
// to speak (spec 2026-10-03-01). The zero value means HTTPVersion11.
type HTTPVersion string

const (
	// HTTPVersion11 is the default: HTTP/1.1 over the shared, pooled check
	// transport (spec 2026-09-28-04).
	HTTPVersion11 HTTPVersion = "1.1"

	// HTTPVersion2 forces HTTP/2: ALPN h2 over TLS, h2c with prior knowledge
	// over plain http://. The transport is built per probe and never pooled.
	HTTPVersion2 HTTPVersion = "2"

	// HTTPVersion3 forces HTTP/3 over QUIC (UDP). There is no TCP fallback.
	HTTPVersion3 HTTPVersion = "3"
)

// HTTPVersionConfigKey is the HTTP check config key holding the version.
const HTTPVersionConfigKey = "httpVersion"

// ErrInvalidHTTPVersion is returned by ParseHTTPVersion for an unknown value.
var ErrInvalidHTTPVersion = errors.New("invalid httpVersion")

// ErrHTTP3OverTunnel is the probe-time refusal of an HTTP/3 check routed
// through an SSH tunnel: the tunnel forwards TCP only, QUIC runs over UDP.
// Validation rejects the combination; this is the runtime backstop.
var ErrHTTP3OverTunnel = errors.New("HTTP/3 runs over UDP and cannot go through an SSH tunnel")

// ParseHTTPVersion parses a config value. "" means the default (1.1).
func ParseHTTPVersion(raw string) (HTTPVersion, error) {
	switch HTTPVersion(raw) {
	case "", HTTPVersion11:
		return HTTPVersion11, nil
	case HTTPVersion2:
		return HTTPVersion2, nil
	case HTTPVersion3:
		return HTTPVersion3, nil
	default:
		return HTTPVersion11, fmt.Errorf("%w: must be one of \"1.1\", \"2\" or \"3\", got %q",
			ErrInvalidHTTPVersion, raw)
	}
}

// Normalized maps the zero value to HTTPVersion11.
func (v HTTPVersion) Normalized() HTTPVersion {
	if v == "" {
		return HTTPVersion11
	}

	return v
}

// ProtoMajor is the http.Response.ProtoMajor a response over this version has.
func (v HTTPVersion) ProtoMajor() int {
	switch v.Normalized() {
	case HTTPVersion2:
		return 2
	case HTTPVersion3:
		return 3
	default:
		return 1
	}
}

// Label is the human spelling used in result messages ("HTTP/2").
func (v HTTPVersion) Label() string {
	return "HTTP/" + string(v.Normalized())
}

// protocolNegotiationMarkers are the fragments of the errors net/http and
// quic-go return when the target does not speak the forced version: a TLS
// alert for an ALPN refusal (h2 or h3), a non-h2 ALPN answer, and an h2c
// preface answered by an HTTP/1.1 server.
var protocolNegotiationMarkers = []string{ //nolint:gochecknoglobals // constant lookup table
	"no application protocol",
	"ALPN",
	"looked like an HTTP/1.1",
}

// IsProtocolNegotiationError reports whether err says the target refused (or
// did not understand) the HTTP version the probe forced, as opposed to a
// reachability failure.
func IsProtocolNegotiationError(err error) bool {
	if err == nil {
		return false
	}

	msg := err.Error()
	for _, marker := range protocolNegotiationMarkers {
		if strings.Contains(msg, marker) {
			return true
		}
	}

	return false
}
