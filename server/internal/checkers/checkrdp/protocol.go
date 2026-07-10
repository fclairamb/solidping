package checkrdp

import (
	"encoding/binary"
	"errors"
	"fmt"
)

// Wire-format constants for the pre-auth RDP negotiation exchange. Section
// references are to MS-RDPBCGR (Remote Desktop Protocol: Basic Connectivity
// and Graphics Remoting).
const (
	// tpktVersion is the fixed TPKT header version byte (RFC 1006).
	tpktVersion = 0x03
	// tpktHeaderLen is the size of the TPKT header (version, reserved,
	// 16-bit big-endian total length).
	tpktHeaderLen = 4

	// x224CRCode is the X.224 Connection Request TPDU code (CR = 0xE, CDT = 0).
	x224CRCode = 0xE0
	// x224CCCode is the X.224 Connection Confirm TPDU code (CC = 0xD, CDT = 0).
	// Only the high nibble identifies the TPDU class; the low nibble carries CDT.
	x224CCCode = 0xD0
	// x224FixedLen is the fixed part of a CR/CC TPDU after the length
	// indicator: code (1) + DST-REF (2) + SRC-REF (2) + class option (1).
	x224FixedLen = 6

	// negStructLen is the size of the RDP_NEG_REQ / RDP_NEG_RSP /
	// RDP_NEG_FAILURE structures (§2.2.1.1.1, §2.2.1.2.1, §2.2.1.2.2).
	negStructLen = 8

	// typeRDPNegReq identifies an RDP Negotiation Request (§2.2.1.1.1).
	typeRDPNegReq = 0x01
	// typeRDPNegRsp identifies an RDP Negotiation Response (§2.2.1.2.1).
	typeRDPNegRsp = 0x02
	// typeRDPNegFailure identifies an RDP Negotiation Failure (§2.2.1.2.2).
	typeRDPNegFailure = 0x03

	// maxResponseLen caps the TPKT length we are willing to read for the
	// Connection Confirm — the real response is at most 19 bytes, so anything
	// past this cap is not an RDP negotiation answer.
	maxResponseLen = 512
	// minResponseLen is the smallest valid Connection Confirm packet: TPKT
	// header + length indicator + the fixed X.224 part (a "plain" CC sent by
	// legacy standard-security servers carries no negotiation payload).
	minResponseLen = tpktHeaderLen + 1 + x224FixedLen
)

// Security protocol bits (§2.2.1.1.1 requestedProtocols /
// §2.2.1.2.1 selectedProtocol).
const (
	// ProtocolRDP is standard RDP security (no external security protocol).
	ProtocolRDP uint32 = 0x00000000
	// ProtocolSSL is TLS 1.0/1.1/1.2 (PROTOCOL_SSL).
	ProtocolSSL uint32 = 0x00000001
	// ProtocolHybrid is CredSSP / NLA (PROTOCOL_HYBRID). Implies TLS.
	ProtocolHybrid uint32 = 0x00000002
	// ProtocolRDSTLS is RDSTLS (PROTOCOL_RDSTLS).
	ProtocolRDSTLS uint32 = 0x00000004
	// ProtocolHybridEx is CredSSP with Early User Authorization Result
	// (PROTOCOL_HYBRID_EX). Implies TLS.
	ProtocolHybridEx uint32 = 0x00000008
)

// requestedProtocols is what the checker advertises: every TLS-capable
// protocol, so the server is free to pick its preferred one.
const requestedProtocols = ProtocolSSL | ProtocolHybrid | ProtocolRDSTLS | ProtocolHybridEx

// RDP_NEG_FAILURE failure codes (§2.2.1.2.2).
const (
	failureSSLRequiredByServer            uint32 = 0x00000001
	failureSSLNotAllowedByServer          uint32 = 0x00000002
	failureSSLCertNotOnServer             uint32 = 0x00000003
	failureInconsistentFlags              uint32 = 0x00000004
	failureHybridRequiredByServer         uint32 = 0x00000005
	failureSSLWithUserAuthRequiredByServe uint32 = 0x00000006
)

// RDP_NEG_RSP flags (§2.2.1.2.1).
const (
	flagExtendedClientDataSupported         uint8 = 0x01
	flagDynVCGFXProtocolSupported           uint8 = 0x02
	flagNegRspReserved                      uint8 = 0x04
	flagRestrictedAdminModeSupported        uint8 = 0x08
	flagRedirectedAuthenticationModeSupport uint8 = 0x10
)

// Parse errors returned by parseConnectionConfirm.
var (
	errResponseTooShort   = errors.New("response too short for an RDP connection confirm")
	errBadTPKTVersion     = errors.New("not an RDP response (bad TPKT version)")
	errBadTPKTLength      = errors.New("invalid TPKT length")
	errNotConnectConfirm  = errors.New("not an X.224 connection confirm")
	errBadX224Length      = errors.New("invalid X.224 length indicator")
	errBadNegotiationType = errors.New("unknown RDP negotiation structure type")
	errBadNegotiationLen  = errors.New("invalid RDP negotiation structure length")
)

// buildConnectionRequest returns the 19-byte X.224 Connection Request PDU
// carrying an RDP_NEG_REQ (§2.2.1.1): TPKT header + X.224 CR TPDU + the
// 8-byte negotiation request advertising every TLS-capable protocol.
func buildConnectionRequest() []byte {
	packet := make([]byte, 0, tpktHeaderLen+1+x224FixedLen+negStructLen)

	// TPKT header (RFC 1006): version, reserved, total length (big-endian).
	totalLen := tpktHeaderLen + 1 + x224FixedLen + negStructLen // 19
	packet = append(packet, tpktVersion, 0x00, byte(totalLen>>8), byte(totalLen&0xFF))

	// X.224 CR TPDU: LI counts everything after itself.
	li := x224FixedLen + negStructLen // 14
	packet = append(packet,
		byte(li),
		x224CRCode, // CR TPDU code
		0x00, 0x00, // DST-REF
		0x00, 0x00, // SRC-REF
		0x00, // class 0
	)

	// RDP_NEG_REQ (§2.2.1.1.1): type, flags, length (LE), requestedProtocols (LE).
	packet = append(packet, typeRDPNegReq, 0x00, negStructLen, 0x00)
	packet = binary.LittleEndian.AppendUint32(packet, requestedProtocols)

	return packet
}

// negotiationResult is the parsed outcome of an X.224 Connection Confirm.
type negotiationResult struct {
	// HasNegotiation is false when the server answered with a plain CC (no
	// RDP_NEG_RSP payload) — legacy servers running standard RDP security.
	HasNegotiation bool
	// Failure is true when the server answered with RDP_NEG_FAILURE.
	Failure bool
	// SelectedProtocol is the server's chosen security protocol (valid when
	// HasNegotiation && !Failure; a plain CC implies ProtocolRDP).
	SelectedProtocol uint32
	// ServerFlags carries the RDP_NEG_RSP flags byte.
	ServerFlags uint8
	// FailureCode is the RDP_NEG_FAILURE code (valid when Failure).
	FailureCode uint32
}

// parseConnectionConfirm parses a full TPKT packet (header included) expected
// to contain an X.224 Connection Confirm with an optional RDP_NEG_RSP /
// RDP_NEG_FAILURE payload. It never reads past the declared, capped length.
func parseConnectionConfirm(packet []byte) (*negotiationResult, error) {
	if len(packet) < minResponseLen {
		return nil, errResponseTooShort
	}

	if packet[0] != tpktVersion {
		return nil, errBadTPKTVersion
	}

	declaredLen := int(binary.BigEndian.Uint16(packet[2:4]))
	if declaredLen < minResponseLen || declaredLen > maxResponseLen || declaredLen > len(packet) {
		return nil, errBadTPKTLength
	}

	// X.224: length indicator counts everything after itself.
	li := int(packet[tpktHeaderLen])
	if li < x224FixedLen || tpktHeaderLen+1+li > declaredLen {
		return nil, errBadX224Length
	}

	if packet[tpktHeaderLen+1]&0xF0 != x224CCCode {
		return nil, errNotConnectConfirm
	}

	payload := packet[tpktHeaderLen+1+x224FixedLen : declaredLen]
	if len(payload) == 0 {
		// Plain CC without negotiation payload: legacy standard-security server.
		return &negotiationResult{HasNegotiation: false, SelectedProtocol: ProtocolRDP}, nil
	}

	if len(payload) < negStructLen {
		return nil, errBadNegotiationLen
	}

	structLen := binary.LittleEndian.Uint16(payload[2:4])
	if structLen != negStructLen {
		return nil, errBadNegotiationLen
	}

	value := binary.LittleEndian.Uint32(payload[4:8])

	switch payload[0] {
	case typeRDPNegRsp:
		return &negotiationResult{
			HasNegotiation:   true,
			SelectedProtocol: value,
			ServerFlags:      payload[1],
		}, nil
	case typeRDPNegFailure:
		return &negotiationResult{
			HasNegotiation: true,
			Failure:        true,
			FailureCode:    value,
		}, nil
	default:
		return nil, errBadNegotiationType
	}
}

// isTLSBased reports whether the selected protocol starts with a TLS
// handshake (everything except legacy standard RDP security does).
func isTLSBased(protocol uint32) bool {
	return protocol != ProtocolRDP
}

// isNLA reports whether the selected protocol performs Network Level
// Authentication (CredSSP).
func isNLA(protocol uint32) bool {
	return protocol == ProtocolHybrid || protocol == ProtocolHybridEx
}

// protocolName renders a selected protocol as the stable output label.
func protocolName(protocol uint32) string {
	switch protocol {
	case ProtocolRDP:
		return "rdp"
	case ProtocolSSL:
		return "tls"
	case ProtocolHybrid:
		return "nla"
	case ProtocolRDSTLS:
		return "rdstls"
	case ProtocolHybridEx:
		return "nla_ex"
	default:
		return fmt.Sprintf("unknown (0x%08x)", protocol)
	}
}

// failureCodeName renders an RDP_NEG_FAILURE code as its MS-RDPBCGR name.
func failureCodeName(code uint32) string {
	switch code {
	case failureSSLRequiredByServer:
		return "SSL_REQUIRED_BY_SERVER"
	case failureSSLNotAllowedByServer:
		return "SSL_NOT_ALLOWED_BY_SERVER"
	case failureSSLCertNotOnServer:
		return "SSL_CERT_NOT_ON_SERVER"
	case failureInconsistentFlags:
		return "INCONSISTENT_FLAGS"
	case failureHybridRequiredByServer:
		return "HYBRID_REQUIRED_BY_SERVER"
	case failureSSLWithUserAuthRequiredByServe:
		return "SSL_WITH_USER_AUTH_REQUIRED_BY_SERVER"
	default:
		return fmt.Sprintf("UNKNOWN (0x%08x)", code)
	}
}

// serverFlagNames expands the RDP_NEG_RSP flags byte into MS-RDPBCGR names.
func serverFlagNames(flags uint8) []string {
	named := []struct {
		bit  uint8
		name string
	}{
		{flagExtendedClientDataSupported, "EXTENDED_CLIENT_DATA_SUPPORTED"},
		{flagDynVCGFXProtocolSupported, "DYNVC_GFX_PROTOCOL_SUPPORTED"},
		{flagNegRspReserved, "NEGRSP_FLAG_RESERVED"},
		{flagRestrictedAdminModeSupported, "RESTRICTED_ADMIN_MODE_SUPPORTED"},
		{flagRedirectedAuthenticationModeSupport, "REDIRECTED_AUTHENTICATION_MODE_SUPPORTED"},
	}

	result := make([]string, 0, len(named))

	for _, f := range named {
		if flags&f.bit != 0 {
			result = append(result, f.name)
		}
	}

	return result
}
