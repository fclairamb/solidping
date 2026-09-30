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
	rfbPrefix = "RFB "
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

// securityTypeNames names the RFB security types this checker knows.
//
//nolint:gochecknoglobals // immutable lookup table
var securityTypeNames = map[uint8]string{
	secInvalid:  "Invalid",
	secNone:     "None",
	secVNCAuth:  "VNC Authentication",
	secRA2:      "RA2",
	secRA2ne:    "RA2ne",
	secTight:    "Tight",
	secUltra:    "Ultra",
	secTLS:      "TLS",
	secVeNCrypt: "VeNCrypt",
	secSASL:     "SASL",
	secMD5:      "MD5 hash",
	secXVP:      "xvp",
	secARD:      "Apple Remote Desktop",
	secMSLogon2: "MS Logon II",
}

// securityTypeName returns a human name for an RFB security type.
func securityTypeName(t uint8) string {
	if name, ok := securityTypeNames[t]; ok {
		return name
	}

	return "Unknown (" + strconv.Itoa(int(t)) + ")"
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
	match := bannerPattern.FindSubmatch(raw)
	if match == nil {
		return "", rfbVersion{}, fmt.Errorf("%w: unexpected banner %q", errNotRFB, sanitizeBanner(raw))
	}

	major, _ := strconv.Atoi(string(match[1]))
	minor, _ := strconv.Atoi(string(match[2]))
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
func handshake(stream io.ReadWriter) (*handshakeResult, error) {
	raw := make([]byte, bannerLen)
	if n, err := io.ReadFull(stream, raw); err != nil {
		// A short answer that does not even start like "RFB " is a foreign
		// protocol that closed on us, not a slow RFB server.
		if n > 0 && string(raw[:min(n, len(rfbPrefix))]) != rfbPrefix[:min(n, len(rfbPrefix))] {
			return nil, fmt.Errorf("%w: unexpected banner %q", errNotRFB, sanitizeBanner(raw[:n]))
		}

		return nil, fmt.Errorf("read server version: %w", err)
	}

	announced, version, err := parseBanner(raw)
	if err != nil {
		return nil, err
	}

	if _, err := stream.Write(version.banner()); err != nil {
		return nil, fmt.Errorf("write client version: %w", err)
	}

	result := &handshakeResult{serverVersion: announced, version: version}

	if version == version33 {
		// 3.3: the server decides and sends one uint32 security type.
		var secType uint32
		if err := binary.Read(stream, binary.BigEndian, &secType); err != nil {
			return nil, fmt.Errorf("read security type: %w", err)
		}

		if secType == uint32(secInvalid) {
			return nil, &ServerRefusedError{Reason: readReason(stream)}
		}

		if secType > 0xff {
			return nil, fmt.Errorf("%w: security type %d out of range", errNotRFB, secType)
		}

		result.securityTypes = []uint8{uint8(secType)}

		return result, nil
	}

	var count [1]byte
	if _, err := io.ReadFull(stream, count[:]); err != nil {
		return nil, fmt.Errorf("read security types count: %w", err)
	}

	if count[0] == 0 {
		return nil, &ServerRefusedError{Reason: readReason(stream)}
	}

	types := make([]uint8, count[0])
	if _, err := io.ReadFull(stream, types); err != nil {
		return nil, fmt.Errorf("read security types: %w", err)
	}

	result.securityTypes = types

	return result, nil
}

// readReason reads a uint32-length-prefixed reason string. Best effort: a
// server that closes before (or while) sending it yields what arrived.
func readReason(reader io.Reader) string {
	var length uint32
	if err := binary.Read(reader, binary.BigEndian, &length); err != nil {
		return ""
	}

	buf := make([]byte, min(length, maxReasonLen))
	n, _ := io.ReadFull(reader, buf)

	return strings.TrimSpace(string(buf[:n]))
}
