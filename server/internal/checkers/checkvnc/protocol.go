package checkvnc

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// RFB (RFC 6143) pre-auth handshake: version negotiation and the
// security-types list.

const (
	bannerLen = 12
	// maxReasonLen caps any server-supplied reason string. RFC 6143 puts no
	// limit on it; a hostile server must not make us allocate gigabytes.
	maxReasonLen = 4096
)

// RFB security types (RFC 6143 §7.1.2 and the IANA registry).
const (
	secInvalid  uint8 = 0
	secNone     uint8 = 1
	secVNCAuth  uint8 = 2
	secRA2      uint8 = 5
	secRA2ne    uint8 = 6
	secTight    uint8 = 16
	secUltra    uint8 = 17
	secTLS      uint8 = 18
	secVeNCrypt uint8 = 19
	secSASL     uint8 = 20
	secMD5      uint8 = 21
	secXVP      uint8 = 22
	secARD      uint8 = 30
	secMSLogon2 uint8 = 113
)

// securityTypeName returns a human name for an RFB security type.
func securityTypeName(t uint8) string {
	switch t {
	case secInvalid:
		return "Invalid"
	case secNone:
		return "None"
	case secVNCAuth:
		return "VNC Authentication"
	case secRA2:
		return "RA2"
	case secRA2ne:
		return "RA2ne"
	case secTight:
		return "Tight"
	case secUltra:
		return "Ultra"
	case secTLS:
		return "TLS"
	case secVeNCrypt:
		return "VeNCrypt"
	case secSASL:
		return "SASL"
	case secMD5:
		return "MD5 hash"
	case secXVP:
		return "xvp"
	case secARD:
		return "Apple Remote Desktop"
	case secMSLogon2:
		return "MS Logon II"
	default:
		return "Unknown (" + strconv.Itoa(int(t)) + ")"
	}
}

// describeSecurityTypes renders a list as "VeNCrypt (19), Apple Remote Desktop (30)".
func describeSecurityTypes(types []uint8) string {
	parts := make([]string, 0, len(types))
	for _, t := range types {
		parts = append(parts, fmt.Sprintf("%s (%d)", securityTypeName(t), t))
	}

	return strings.Join(parts, ", ")
}

// securityTypesOutput is the output-details shape of the offered list.
func securityTypesOutput(types []uint8) []map[string]any {
	out := make([]map[string]any, 0, len(types))
	for _, t := range types {
		out = append(out, map[string]any{"number": int(t), "name": securityTypeName(t)})
	}

	return out
}

// rfbVersion is a negotiated protocol version (3.3, 3.7 or 3.8).
type rfbVersion struct {
	major, minor int
}

func (v rfbVersion) String() string {
	return fmt.Sprintf("%d.%d", v.major, v.minor)
}

// banner is the 12-byte ProtocolVersion message: "RFB xxx.yyy\n".
func (v rfbVersion) banner() []byte {
	return fmt.Appendf(nil, "RFB %03d.%03d\n", v.major, v.minor)
}

//nolint:gochecknoglobals // compiled once
var (
	bannerPattern = regexp.MustCompile(`^RFB (\d{3})\.(\d{3})\n$`)
	version33     = rfbVersion{3, 3}
	version37     = rfbVersion{3, 7}
	version38     = rfbVersion{3, 8}
)

// Handshake failures. Each maps to a stable failure code in the checker.
var (
	// errNotRFB is a banner that is not "RFB xxx.yyy\n" (an SSH server, an
	// HTTP server, a TLS listener...).
	errNotRFB = errors.New("not an RFB server")
)

// ServerRefusedError is a server that answers the handshake with an empty
// security-types list (type 0) and a reason string instead of any method.
type ServerRefusedError struct {
	Reason string
}

func (e *ServerRefusedError) Error() string {
	if e.Reason == "" {
		return "server refused the connection"
	}

	return "server refused the connection: " + e.Reason
}

// handshakeResult is what the pre-auth exchange learned.
type handshakeResult struct {
	// serverVersion is the version the server announced, e.g. "3.889".
	serverVersion string
	// version is the version negotiated (the highest both sides speak).
	version rfbVersion
	// securityTypes is the offered list (3.7+) or the single type the server
	// chose (3.3).
	securityTypes []uint8
}

// offers reports whether the server offered security type t.
func (h *handshakeResult) offers(t uint8) bool {
	return slices.Contains(h.securityTypes, t)
}

// parseBanner validates the server's ProtocolVersion message and picks the
// version to answer with. Servers announce odd minors (3.5 from old clients,
// 3.889 from macOS, 4.x/5.x from RealVNC): anything below 3.7 is spoken as
// 3.3, 3.7 as 3.7, anything above as 3.8 (RFC 6143 §7.1.1).
func parseBanner(raw []byte) (string, rfbVersion, error) {
	m := bannerPattern.FindSubmatch(raw)
	if m == nil {
		return "", rfbVersion{}, fmt.Errorf("%w: unexpected banner %q", errNotRFB, sanitizeBanner(raw))
	}

	major, _ := strconv.Atoi(string(m[1]))
	minor, _ := strconv.Atoi(string(m[2]))
	announced := fmt.Sprintf("%d.%d", major, minor)

	switch {
	case major < 3:
		return announced, rfbVersion{}, fmt.Errorf("%w: unsupported RFB version %s", errNotRFB, announced)
	case major > 3 || minor >= 8:
		return announced, version38, nil
	case minor == 7:
		return announced, version37, nil
	default:
		return announced, version33, nil
	}
}

// sanitizeBanner keeps a printable excerpt of a bad banner for the message.
func sanitizeBanner(raw []byte) string {
	return strings.TrimSpace(strings.Map(func(r rune) rune {
		if r < 0x20 || r > 0x7e {
			return -1
		}

		return r
	}, string(raw)))
}

// handshake reads the server's version, answers with the negotiated one and
// reads the security types. It never selects a type: the caller decides
// whether to go further (authenticate) or just close.
func handshake(rw io.ReadWriter) (*handshakeResult, error) {
	raw := make([]byte, bannerLen)
	if n, err := io.ReadFull(rw, raw); err != nil {
		// A short answer that does not even start like "RFB " is a foreign
		// protocol that closed on us, not a slow RFB server.
		if n > 0 && !strings.HasPrefix("RFB ", string(raw[:min(n, 4)])) {
			return nil, fmt.Errorf("%w: unexpected banner %q", errNotRFB, sanitizeBanner(raw[:n]))
		}

		return nil, fmt.Errorf("read server version: %w", err)
	}

	announced, version, err := parseBanner(raw)
	if err != nil {
		return nil, err
	}

	if _, err := rw.Write(version.banner()); err != nil {
		return nil, fmt.Errorf("write client version: %w", err)
	}

	result := &handshakeResult{serverVersion: announced, version: version}

	if version == version33 {
		// 3.3: the server decides and sends one uint32 security type.
		var secType uint32
		if err := binary.Read(rw, binary.BigEndian, &secType); err != nil {
			return nil, fmt.Errorf("read security type: %w", err)
		}

		if secType == uint32(secInvalid) {
			return nil, &ServerRefusedError{Reason: readReason(rw)}
		}

		if secType > 0xff {
			return nil, fmt.Errorf("%w: security type %d out of range", errNotRFB, secType)
		}

		result.securityTypes = []uint8{uint8(secType)}

		return result, nil
	}

	var count [1]byte
	if _, err := io.ReadFull(rw, count[:]); err != nil {
		return nil, fmt.Errorf("read security types count: %w", err)
	}

	if count[0] == 0 {
		return nil, &ServerRefusedError{Reason: readReason(rw)}
	}

	types := make([]uint8, count[0])
	if _, err := io.ReadFull(rw, types); err != nil {
		return nil, fmt.Errorf("read security types: %w", err)
	}

	result.securityTypes = types

	return result, nil
}

// readReason reads a uint32-length-prefixed reason string. Best effort: a
// server that closes before (or while) sending it yields what arrived.
func readReason(r io.Reader) string {
	var length uint32
	if err := binary.Read(r, binary.BigEndian, &length); err != nil {
		return ""
	}

	buf := make([]byte, min(length, maxReasonLen))
	n, _ := io.ReadFull(r, buf)

	return strings.TrimSpace(string(buf[:n]))
}
