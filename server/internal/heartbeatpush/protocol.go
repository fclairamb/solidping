// Package heartbeatpush implements the embedded push transport for heartbeat
// checks: a one-line-per-beat wire protocol plus the TCP and UDP listeners
// that carry it.
//
// The protocol exists because the HTTPS ingest shuts out the senders heartbeat
// semantics fit best — microcontrollers, AT-command cellular modems, battery
// sensors, legacy PLCs. TLS costs tens of KB of RAM, a CA store and a correct
// clock; HTTP framing is pure overhead for a message that means "I'm alive".
//
// # Security posture (stated here on purpose)
//
//   - SP1 puts the check token in cleartext on the wire. A captured beat can be
//     replayed to MASK A REAL OUTAGE. That is what the per-check `require_hmac`
//     option exists to close, and why flipping it on should be paired with a
//     token rotation — the token is also the SP2 signing key.
//   - SP2 authenticates the MESSAGE, not the transport. An attacker can still
//     drop datagrams (UDP guarantees nothing anyway); the HMAC protects against
//     forged aliveness, not against censorship.
//   - UDP source addresses are spoofable. The source IP is recorded for
//     forensics and used as a rate-limiting bucket key; it is NEVER identity.
package heartbeatpush

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"math"
	"strconv"
	"strings"
)

// Wire limits. They bound what a single unauthenticated datagram can cost
// before anything is looked up in the database.
const (
	// MaxLineBytes is the largest accepted beat line, excluding the newline.
	// A bare SP2 datagram is around 80 bytes; 512 leaves generous room for an
	// annotation and still fits any sane MTU.
	MaxLineBytes = 512
	// MaxOrgBytes / MaxIdentifierBytes bound the two halves of the target.
	MaxOrgBytes        = 64
	MaxIdentifierBytes = 128
	// MACBytes is the truncated HMAC-SHA256 length. 16 bytes (128 bits) of a
	// SHA-256 tag is the usual truncation floor and keeps the datagram small.
	MACBytes = 16
)

// Protocol versions, as they appear literally at the start of a beat line.
const (
	// ProtoSP1 is the plaintext-token form:
	// `SP1 <org>/<ident> <token> [annotation]`.
	ProtoSP1 = "SP1"
	// ProtoSP2 is the HMAC-signed form:
	// `SP2 <org>/<ident> <ts> <ctr> [annotation] <mac>`.
	ProtoSP2 = "SP2"
)

// ErrMalformed is returned for anything that is not a well-formed beat line.
// It is deliberately the ONLY parse error: the listeners must never let a
// caller distinguish "bad syntax" from "unknown check" from "bad MAC", so
// there is nothing here worth differentiating.
var ErrMalformed = errors.New("malformed beat line")

// Beat is one parsed beat line.
//
// Signed carries the exact bytes an SP2 MAC covers — the whole line up to but
// excluding the final space and the MAC itself. Keeping it as the literal
// prefix (rather than re-serializing the parsed fields) is what makes the
// annotation tamper-proof under SP2: whatever bytes the device signed are the
// bytes that get verified, including an annotation this parser did not
// understand.
type Beat struct {
	// Version is 1 for SP1, 2 for SP2.
	Version int
	// Org and Identifier are the two halves of `<org>/<check-identifier>`.
	// The identifier is a check slug or UID, resolved exactly like the HTTP
	// ingest's :identifier path parameter.
	Org        string
	Identifier string
	// Token is the plaintext token, SP1 only.
	Token string
	// Timestamp is the device's unix-seconds clock, SP2 only. 0 means "this
	// device has no clock" and skips the freshness window.
	Timestamp int64
	// Counter is the strictly-increasing replay counter, SP2 only.
	Counter int64
	// MAC is the raw 16-byte truncated HMAC-SHA256, SP2 only.
	MAC []byte
	// Signed is the exact signed prefix, SP2 only.
	Signed string
	// Annotation is the raw optional annotation text ("" when absent). It is
	// intentionally left unparsed here: annotation parsing must never be able
	// to fail a beat, so it is a separate, total function.
	Annotation string
}

// ParseLine parses one beat line. The line must NOT contain a newline; trailing
// CR and horizontal whitespace are trimmed first (so a CRLF sender and an LF
// sender sign the same bytes), and fields are separated by exactly one space —
// a run of spaces is malformed rather than silently collapsed, because
// collapsing would make the SP2 signed bytes ambiguous.
func ParseLine(line []byte) (*Beat, error) {
	if len(line) == 0 || len(line) > MaxLineBytes {
		return nil, ErrMalformed
	}

	text := strings.TrimRight(string(line), " \t\r\n")
	if text == "" {
		return nil, ErrMalformed
	}

	parts := strings.Split(text, " ")
	for _, part := range parts {
		if part == "" {
			return nil, ErrMalformed
		}
	}

	switch parts[0] {
	case ProtoSP1:
		return parseSP1(parts)
	case ProtoSP2:
		return parseSP2(parts, text)
	default:
		return nil, ErrMalformed
	}
}

// parseSP1 handles `SP1 <org>/<ident> <token> [annotation]`.
func parseSP1(parts []string) (*Beat, error) {
	const minParts = 3
	if len(parts) < minParts {
		return nil, ErrMalformed
	}

	org, identifier, err := splitTarget(parts[1])
	if err != nil {
		return nil, err
	}

	token := parts[2]
	if token == "" || len(token) > MaxLineBytes {
		return nil, ErrMalformed
	}

	return &Beat{
		Version:    1,
		Org:        org,
		Identifier: identifier,
		Token:      token,
		Annotation: strings.Join(parts[minParts:], " "),
	}, nil
}

// parseSP2 handles `SP2 <org>/<ident> <ts> <ctr> [annotation] <mac>`.
//
// The MAC is the LAST space-separated token, so the line is parsed from both
// ends and everything in between is the annotation — which is therefore covered
// by the signature and cannot be tampered with on an authenticated beat.
func parseSP2(parts []string, text string) (*Beat, error) {
	const minParts = 5 // SP2, target, ts, ctr, mac
	if len(parts) < minParts {
		return nil, ErrMalformed
	}

	org, identifier, err := splitTarget(parts[1])
	if err != nil {
		return nil, err
	}

	timestamp, err := strconv.ParseInt(parts[2], 10, 64)
	if err != nil || timestamp < 0 {
		return nil, ErrMalformed
	}

	// Counters are unsigned on the wire (the documented device recipe is
	// boot_count<<32 | seconds_since_boot) but stored in a signed BIGINT
	// column, so anything above MaxInt64 is refused here rather than wrapping
	// negative and defeating the strictly-greater rule.
	counter, err := strconv.ParseUint(parts[3], 10, 64)
	if err != nil || counter > math.MaxInt64 {
		return nil, ErrMalformed
	}

	macHex := parts[len(parts)-1]
	if len(macHex) != MACBytes*2 {
		return nil, ErrMalformed
	}

	mac, err := hex.DecodeString(macHex)
	if err != nil {
		return nil, ErrMalformed
	}

	signed := text[:len(text)-len(macHex)-1]

	return &Beat{
		Version:    2,
		Org:        org,
		Identifier: identifier,
		Timestamp:  timestamp,
		Counter:    int64(counter),
		MAC:        mac,
		Signed:     signed,
		Annotation: strings.Join(parts[4:len(parts)-1], " "),
	}, nil
}

// splitTarget splits `<org>/<check-identifier>`. Exactly one slash: an org slug
// never contains one, and a check identifier is a slug or a UID, neither of
// which does either.
func splitTarget(target string) (string, string, error) {
	org, identifier, ok := strings.Cut(target, "/")
	if !ok || org == "" || identifier == "" || strings.Contains(identifier, "/") {
		return "", "", ErrMalformed
	}

	if len(org) > MaxOrgBytes || len(identifier) > MaxIdentifierBytes {
		return "", "", ErrMalformed
	}

	return org, identifier, nil
}

// ComputeMAC returns the truncated HMAC-SHA256 of signed under key.
//
// The canonical-string-then-HMAC shape deliberately mirrors internal/servicesig
// (HMAC-SHA256 over `<timestamp>.<METHOD>.<path>.<sha256 body>`) — one signing
// idiom in the project, not two. Here the canonical string simply IS the wire
// bytes, because on a 32 KB microcontroller the cheapest canonical form is the
// one the device already has in its send buffer.
func ComputeMAC(key, signed string) []byte {
	mac := hmac.New(sha256.New, []byte(key))
	mac.Write([]byte(signed))

	return mac.Sum(nil)[:MACBytes]
}

// VerifyMAC reports whether mac is the correct tag for signed under key, in
// constant time.
func VerifyMAC(key, signed string, mac []byte) bool {
	if len(mac) != MACBytes {
		return false
	}

	return hmac.Equal(ComputeMAC(key, signed), mac)
}

// SignSP2 builds a complete SP2 line for the given fields. It exists so tests,
// documentation examples and any future first-party sender agree with the
// parser by construction rather than by transcription.
func SignSP2(org, identifier, token string, timestamp, counter int64, annotation string) string {
	var builder strings.Builder

	builder.WriteString(ProtoSP2)
	builder.WriteString(" ")
	builder.WriteString(org)
	builder.WriteString("/")
	builder.WriteString(identifier)
	builder.WriteString(" ")
	builder.WriteString(strconv.FormatInt(timestamp, 10))
	builder.WriteString(" ")
	builder.WriteString(strconv.FormatInt(counter, 10))

	if annotation != "" {
		builder.WriteString(" ")
		builder.WriteString(annotation)
	}

	signed := builder.String()

	return signed + " " + hex.EncodeToString(ComputeMAC(token, signed))
}
