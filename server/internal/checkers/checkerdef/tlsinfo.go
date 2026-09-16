package checkerdef

import (
	"crypto/tls"
	"fmt"
)

// TLSVersionString renders a negotiated TLS version the way every SolidPing
// result reports it — the `tls_version` output field of the `tcp` check, and
// the `tls.version` field of a JS `tcp.connect()` handle.
//
// One function so those two can never disagree about what "TLS 1.3" is called.
func TLSVersionString(version uint16) string {
	switch version {
	case tls.VersionTLS10:
		return "TLS 1.0"
	case tls.VersionTLS11:
		return "TLS 1.1"
	case tls.VersionTLS12:
		return "TLS 1.2"
	case tls.VersionTLS13:
		return "TLS 1.3"
	default:
		return fmt.Sprintf("Unknown (0x%04x)", version)
	}
}
