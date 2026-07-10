package checkrdp

import (
	"encoding/binary"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestBuildConnectionRequest pins the exact 19-byte wire format of the X.224
// Connection Request + RDP_NEG_REQ (MS-RDPBCGR §2.2.1.1).
func TestBuildConnectionRequest(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	got := buildConnectionRequest()

	want := []byte{
		0x03, 0x00, 0x00, 0x13, // TPKT: version 3, reserved, length 19 (BE)
		0x0E,       // X.224 LI = 14
		0xE0,       // X.224 CR TPDU
		0x00, 0x00, // DST-REF
		0x00, 0x00, // SRC-REF
		0x00,       // class 0
		0x01,       // TYPE_RDP_NEG_REQ
		0x00,       // flags
		0x08, 0x00, // length 8 (LE)
		0x0F, 0x00, 0x00, 0x00, // requestedProtocols = SSL|HYBRID|RDSTLS|HYBRID_EX (LE)
	}
	r.Equal(want, got)
}

// ccPacket builds a TPKT + X.224 Connection Confirm with the given payload.
func ccPacket(payload []byte) []byte {
	total := tpktHeaderLen + 1 + x224FixedLen + len(payload)
	packet := []byte{tpktVersion, 0x00, byte(total >> 8), byte(total & 0xFF)}
	packet = append(packet,
		byte(x224FixedLen+len(payload)), // LI
		x224CCCode,                      // CC TPDU
		0x00, 0x00,                      // DST-REF
		0x00, 0x00, // SRC-REF
		0x00, // class 0
	)

	return append(packet, payload...)
}

// negRspPayload builds an RDP_NEG_RSP structure (§2.2.1.2.1).
func negRspPayload(flags uint8, protocol uint32) []byte {
	payload := []byte{typeRDPNegRsp, flags, negStructLen, 0x00}

	return binary.LittleEndian.AppendUint32(payload, protocol)
}

// negFailurePayload builds an RDP_NEG_FAILURE structure (§2.2.1.2.2).
func negFailurePayload(code uint32) []byte {
	payload := []byte{typeRDPNegFailure, 0x00, negStructLen, 0x00}

	return binary.LittleEndian.AppendUint32(payload, code)
}

func TestParseConnectionConfirm(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		packet  []byte
		want    *negotiationResult
		wantErr error
	}{
		{
			name:   "neg rsp standard rdp",
			packet: ccPacket(negRspPayload(0, ProtocolRDP)),
			want:   &negotiationResult{HasNegotiation: true, SelectedProtocol: ProtocolRDP},
		},
		{
			name:   "neg rsp ssl",
			packet: ccPacket(negRspPayload(0, ProtocolSSL)),
			want:   &negotiationResult{HasNegotiation: true, SelectedProtocol: ProtocolSSL},
		},
		{
			name:   "neg rsp hybrid",
			packet: ccPacket(negRspPayload(0, ProtocolHybrid)),
			want:   &negotiationResult{HasNegotiation: true, SelectedProtocol: ProtocolHybrid},
		},
		{
			name:   "neg rsp rdstls",
			packet: ccPacket(negRspPayload(0, ProtocolRDSTLS)),
			want:   &negotiationResult{HasNegotiation: true, SelectedProtocol: ProtocolRDSTLS},
		},
		{
			name:   "neg rsp hybrid ex with flags",
			packet: ccPacket(negRspPayload(flagRestrictedAdminModeSupported|flagExtendedClientDataSupported, ProtocolHybridEx)),
			want: &negotiationResult{
				HasNegotiation:   true,
				SelectedProtocol: ProtocolHybridEx,
				ServerFlags:      flagRestrictedAdminModeSupported | flagExtendedClientDataSupported,
			},
		},
		{
			name:   "neg failure ssl required",
			packet: ccPacket(negFailurePayload(failureSSLRequiredByServer)),
			want:   &negotiationResult{HasNegotiation: true, Failure: true, FailureCode: failureSSLRequiredByServer},
		},
		{
			name:   "neg failure ssl not allowed",
			packet: ccPacket(negFailurePayload(failureSSLNotAllowedByServer)),
			want:   &negotiationResult{HasNegotiation: true, Failure: true, FailureCode: failureSSLNotAllowedByServer},
		},
		{
			name:   "neg failure cert not on server",
			packet: ccPacket(negFailurePayload(failureSSLCertNotOnServer)),
			want:   &negotiationResult{HasNegotiation: true, Failure: true, FailureCode: failureSSLCertNotOnServer},
		},
		{
			name:   "neg failure inconsistent flags",
			packet: ccPacket(negFailurePayload(failureInconsistentFlags)),
			want:   &negotiationResult{HasNegotiation: true, Failure: true, FailureCode: failureInconsistentFlags},
		},
		{
			name:   "neg failure hybrid required",
			packet: ccPacket(negFailurePayload(failureHybridRequiredByServer)),
			want:   &negotiationResult{HasNegotiation: true, Failure: true, FailureCode: failureHybridRequiredByServer},
		},
		{
			name:   "neg failure ssl with user auth required",
			packet: ccPacket(negFailurePayload(failureSSLWithUserAuthRequiredByServe)),
			want:   &negotiationResult{HasNegotiation: true, Failure: true, FailureCode: failureSSLWithUserAuthRequiredByServe},
		},
		{
			name:   "plain cc without negotiation payload",
			packet: ccPacket(nil),
			want:   &negotiationResult{HasNegotiation: false, SelectedProtocol: ProtocolRDP},
		},
		{
			name:    "truncated packet",
			packet:  []byte{0x03, 0x00, 0x00},
			wantErr: errResponseTooShort,
		},
		{
			name:    "wrong tpkt version",
			packet:  append([]byte{0x02}, ccPacket(nil)[1:]...),
			wantErr: errBadTPKTVersion,
		},
		{
			name:    "declared length larger than packet",
			packet:  []byte{0x03, 0x00, 0x01, 0x00, 0x06, 0xD0, 0x00, 0x00, 0x00, 0x00, 0x00},
			wantErr: errBadTPKTLength,
		},
		{
			name:    "declared length above cap",
			packet:  append([]byte{0x03, 0x00, 0xFF, 0xFF}, ccPacket(nil)[4:]...),
			wantErr: errBadTPKTLength,
		},
		{
			name:    "garbage http response",
			packet:  []byte("HTTP/1.1 400 Bad Request\r\n\r\n"),
			wantErr: errBadTPKTVersion,
		},
		{
			name: "connection request instead of confirm",
			packet: func() []byte {
				packet := ccPacket(nil)
				packet[5] = x224CRCode

				return packet
			}(),
			wantErr: errNotConnectConfirm,
		},
		{
			name: "x224 length indicator too small",
			packet: func() []byte {
				packet := ccPacket(nil)
				packet[4] = 0x02

				return packet
			}(),
			wantErr: errBadX224Length,
		},
		{
			name:    "unknown negotiation type",
			packet:  ccPacket([]byte{0x7F, 0x00, negStructLen, 0x00, 0x00, 0x00, 0x00, 0x00}),
			wantErr: errBadNegotiationType,
		},
		{
			name:    "negotiation payload too short",
			packet:  ccPacket([]byte{typeRDPNegRsp, 0x00, 0x04}),
			wantErr: errBadNegotiationLen,
		},
		{
			name:    "negotiation struct length wrong",
			packet:  ccPacket([]byte{typeRDPNegRsp, 0x00, 0x0C, 0x00, 0x00, 0x00, 0x00, 0x00}),
			wantErr: errBadNegotiationLen,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)

			got, err := parseConnectionConfirm(tc.packet)

			if tc.wantErr != nil {
				r.ErrorIs(err, tc.wantErr)
				r.Nil(got)

				return
			}

			r.NoError(err)
			r.Equal(tc.want, got)
		})
	}
}

func TestProtocolName(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	r.Equal("rdp", protocolName(ProtocolRDP))
	r.Equal("tls", protocolName(ProtocolSSL))
	r.Equal("nla", protocolName(ProtocolHybrid))
	r.Equal("rdstls", protocolName(ProtocolRDSTLS))
	r.Equal("nla_ex", protocolName(ProtocolHybridEx))
	r.Equal("unknown (0x00000040)", protocolName(0x40))
}

func TestFailureCodeName(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	r.Equal("SSL_REQUIRED_BY_SERVER", failureCodeName(failureSSLRequiredByServer))
	r.Equal("SSL_NOT_ALLOWED_BY_SERVER", failureCodeName(failureSSLNotAllowedByServer))
	r.Equal("SSL_CERT_NOT_ON_SERVER", failureCodeName(failureSSLCertNotOnServer))
	r.Equal("INCONSISTENT_FLAGS", failureCodeName(failureInconsistentFlags))
	r.Equal("HYBRID_REQUIRED_BY_SERVER", failureCodeName(failureHybridRequiredByServer))
	r.Equal("SSL_WITH_USER_AUTH_REQUIRED_BY_SERVER", failureCodeName(failureSSLWithUserAuthRequiredByServe))
	r.Equal("UNKNOWN (0x000000ff)", failureCodeName(0xFF))
}

func TestServerFlagNames(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	r.Empty(serverFlagNames(0))
	r.Equal(
		[]string{"EXTENDED_CLIENT_DATA_SUPPORTED", "RESTRICTED_ADMIN_MODE_SUPPORTED"},
		serverFlagNames(flagExtendedClientDataSupported|flagRestrictedAdminModeSupported),
	)
	r.Equal(
		[]string{
			"EXTENDED_CLIENT_DATA_SUPPORTED",
			"DYNVC_GFX_PROTOCOL_SUPPORTED",
			"NEGRSP_FLAG_RESERVED",
			"RESTRICTED_ADMIN_MODE_SUPPORTED",
			"REDIRECTED_AUTHENTICATION_MODE_SUPPORTED",
		},
		serverFlagNames(0x1F),
	)
}
