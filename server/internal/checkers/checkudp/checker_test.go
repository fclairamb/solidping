// Package checkudp's first tests. Before this the package held checker.go,
// config.go and samples.go and no _test.go at all — the send/expect path, the
// only thing that makes a UDP check more than a no-op, was never exercised.
package checkudp

import (
	"context"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/miekg/dns"
	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

const localhost = "127.0.0.1"

// udpEcho starts a UDP listener on 127.0.0.1:0 and hands every datagram to
// `handle`, which writes back zero or more replies. Returns the bound port.
func udpEcho(t *testing.T, handle func(conn net.PacketConn, from net.Addr, payload []byte)) int {
	t.Helper()

	conn, err := net.ListenPacket("udp", "127.0.0.1:0")
	require.NoError(t, err)

	t.Cleanup(func() { _ = conn.Close() })

	go func() {
		buf := make([]byte, 4096)

		for {
			n, from, readErr := conn.ReadFrom(buf)
			if readErr != nil {
				return
			}

			payload := make([]byte, n)
			copy(payload, buf[:n])
			handle(conn, from, payload)
		}
	}()

	udpAddr, ok := conn.LocalAddr().(*net.UDPAddr)
	require.True(t, ok)

	return udpAddr.Port
}

func runUDP(t *testing.T, cfg *UDPConfig) *checkerdef.Result {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	result, err := (&UDPChecker{}).Execute(ctx, cfg)
	require.NoError(t, err)

	return result
}

func TestUDPChecker_Type(t *testing.T) {
	t.Parallel()

	require.Equal(t, checkerdef.CheckTypeUDP, (&UDPChecker{}).Type())
}

// TestUDPExchange_BinaryRequestAndReply is the whole point of the feature over
// UDP: a hex payload goes out as real bytes and the binary answer is asserted
// with a hex substring and with a regex.
func TestUDPExchange_BinaryRequestAndReply(t *testing.T) {
	t.Parallel()

	port := udpEcho(t, func(conn net.PacketConn, from net.Addr, payload []byte) {
		// Answer 0x5350 0x8180 + whatever was asked, like a resolver would.
		_, _ = conn.WriteTo(append([]byte{0x53, 0x50, 0x81, 0x80}, payload...), from)
	})

	tests := []struct {
		name string
		cfg  *UDPConfig
	}{
		{
			name: "hex expect_data",
			cfg: &UDPConfig{
				Host: localhost, Port: port, Timeout: 2 * time.Second,
				SendData: "de ad be ef", SendEncoding: checkerdef.PayloadEncodingHex,
				ExpectData: "53508180deadbeef", ExpectEncoding: checkerdef.PayloadEncodingHex,
			},
		},
		{
			name: "expect_pattern over the raw bytes",
			cfg: &UDPConfig{
				Host: localhost, Port: port, Timeout: 2 * time.Second,
				SendData: "deadbeef", SendEncoding: checkerdef.PayloadEncodingHex,
				ExpectPattern: `^\x53\x50`,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			result := runUDP(t, tt.cfg)

			r := require.New(t)
			r.Equal(checkerdef.StatusUp, result.Status, result.Output)
			r.Equal(4, result.Metrics[checkerdef.OutputKeyBytesSent])
			r.Equal(8, result.Metrics[checkerdef.OutputKeyBytesReceived])
			// Invalid UTF-8 is escaped rather than replaced with U+FFFD; the
			// printable ASCII bytes 0x53/0x50 stay readable as "SP".
			r.Equal(`SP\x81\x80\xde\xad\xbe\xef`, result.Output[checkerdef.OutputKeyReceivedData])
		})
	}
}

// TestUDPExchange_ReplySplitAcrossTwoDatagrams: each Read is one datagram, so
// an expectation that straddles the boundary only matches if the datagrams
// accumulate into a single buffer — the UDP half of the read-until-match loop.
func TestUDPExchange_ReplySplitAcrossTwoDatagrams(t *testing.T) {
	t.Parallel()

	port := udpEcho(t, func(conn net.PacketConn, from net.Addr, _ []byte) {
		_, _ = conn.WriteTo([]byte("STATUS: par"), from)
		time.Sleep(50 * time.Millisecond)
		_, _ = conn.WriteTo([]byte("tially-ok"), from)
	})

	result := runUDP(t, &UDPConfig{
		Host: localhost, Port: port, Timeout: 2 * time.Second,
		SendData: "ping", ExpectData: "partially-ok",
	})

	r := require.New(t)
	r.Equal(checkerdef.StatusUp, result.Status, result.Output)
	r.Equal(2, result.Output[checkerdef.OutputKeyReadCount])
	r.Equal("STATUS: partially-ok", result.Output[checkerdef.OutputKeyReceivedData])
}

// TestUDPExchange_SilenceIsTimeout: a listener that never answers is the
// canonical "port open, service mute". It must be Timeout, and it must land
// within the configured timeout rather than a multiple of it.
func TestUDPExchange_SilenceIsTimeout(t *testing.T) {
	t.Parallel()

	port := udpEcho(t, func(_ net.PacketConn, _ net.Addr, _ []byte) {})

	start := time.Now()
	result := runUDP(t, &UDPConfig{
		Host: localhost, Port: port, Timeout: 2 * time.Second,
		SendData: "ping", ExpectData: "pong",
	})
	elapsed := time.Since(start)

	r := require.New(t)
	r.Equal(checkerdef.StatusTimeout, result.Status, result.Output)
	r.Contains(result.Output[checkerdef.OutputKeyError], "no matching reply within 2s (0 bytes received)")
	r.Less(elapsed, 3*time.Second)
	r.Greater(elapsed, 1800*time.Millisecond)
}

// TestUDPExchange_SendWithoutExpectationSurvivesSilence keeps the pre-existing
// contract: no expectation means no failure on silence (and, for UDP, that the
// check can then only ever detect port-unreachable).
func TestUDPExchange_SendWithoutExpectationSurvivesSilence(t *testing.T) {
	t.Parallel()

	port := udpEcho(t, func(_ net.PacketConn, _ net.Addr, _ []byte) {})

	result := runUDP(t, &UDPConfig{
		Host: localhost, Port: port, Timeout: time.Second, SendData: "ping",
	})

	require.Equal(t, checkerdef.StatusUp, result.Status, result.Output)
}

// TestUDPExchange_WrongAnswerIsDownNotUp is the negative control for the two
// success cases above: the service answers, but with the wrong thing.
func TestUDPExchange_WrongAnswerIsDownNotUp(t *testing.T) {
	t.Parallel()

	port := udpEcho(t, func(conn net.PacketConn, from net.Addr, _ []byte) {
		_, _ = conn.WriteTo([]byte("SERVFAIL"), from)
	})

	result := runUDP(t, &UDPConfig{
		Host: localhost, Port: port, Timeout: 2 * time.Second,
		SendData: "ping", ExpectData: "NOERROR",
	})

	r := require.New(t)
	r.Equal(checkerdef.StatusTimeout, result.Status, result.Output)
	r.Contains(result.Output[checkerdef.OutputKeyError], "8 bytes received")
	r.Equal("SERVFAIL", result.Output[checkerdef.OutputKeyReceivedData])
}

func TestUDPConfig_FromMapAndGetConfig(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	stored := map[string]any{
		"host": "8.8.8.8", "port": float64(53), "timeout": "5s",
		"send_data": "5350", "send_encoding": "hex",
		"expect_data": "53508180", "expect_encoding": "hex",
		"expect_pattern": "^\x53",
	}

	cfg := &UDPConfig{}
	r.NoError(cfg.FromMap(stored))
	r.Equal("hex", cfg.SendEncoding)
	r.Equal("53508180", cfg.ExpectData)
	r.Equal("hex", cfg.ExpectEncoding)
	r.Equal("^\x53", cfg.ExpectPattern)

	out := cfg.GetConfig()
	for _, key := range []string{"send_data", "send_encoding", "expect_data", "expect_encoding", "expect_pattern"} {
		r.Equal(stored[key], out[key], key)
	}
}

func TestUDPValidate_RejectsBadPayloadConfig(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		config map[string]any
		want   string
	}{
		{
			name:   "uncompilable expect_pattern",
			config: map[string]any{"host": "8.8.8.8", "port": 53, "expect_pattern": "(unclosed"},
			want:   "expect_pattern",
		},
		{
			name: "odd-length hex payload",
			config: map[string]any{
				"host": "8.8.8.8", "port": 53, "send_data": "535", "send_encoding": "hex",
			},
			want: "send_data",
		},
		{
			name: "the shipped DNS sample validates",
			config: map[string]any{
				"host": "8.8.8.8", "port": 53,
				"send_data": SampleDNSQuery, "send_encoding": "hex",
				"expect_data": SampleDNSExpect, "expect_encoding": "hex",
			},
		},
		{
			name: "the shipped NTP sample validates",
			config: map[string]any{
				"host": "pool.ntp.org", "port": 123,
				"send_data": SampleNTPRequest, "send_encoding": "hex",
				"expect_pattern": SampleNTPExpect,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			r := require.New(t)
			err := (&UDPChecker{}).Validate(&checkerdef.CheckSpec{Config: tt.config})

			if tt.want == "" {
				r.NoError(err)

				return
			}

			r.Error(err)
			r.Contains(err.Error(), tt.want)
		})
	}
}

// TestDNSSampleDecodesToAWellFormedQuery unpacks the shipped sample's bytes
// with a real DNS library: a sample whose payload is wrong would otherwise only
// be caught by someone watching the check flap in production.
func TestDNSSampleDecodesToAWellFormedQuery(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	raw, err := checkerdef.DecodePayload(SampleDNSQuery, checkerdef.PayloadEncodingHex)
	r.NoError(err)

	msg := new(dns.Msg)
	r.NoError(msg.Unpack(raw))

	r.Equal(uint16(0x5350), msg.Id)
	r.False(msg.Response)
	r.True(msg.RecursionDesired)
	r.Len(msg.Question, 1)
	r.Equal("solidping.io.", msg.Question[0].Name)
	r.Equal(dns.TypeA, msg.Question[0].Qtype)
	r.Equal(dns.ClassINET, int(msg.Question[0].Qclass))

	// The expectation is this query's ID echoed with QR/RD/RA set and RCODE 0.
	expect, err := checkerdef.DecodePayload(SampleDNSExpect, checkerdef.PayloadEncodingHex)
	r.NoError(err)
	r.Equal([]byte{0x53, 0x50, 0x81, 0x80}, expect)
}

// TestNTPSampleIsAFortyEightByteClientRequest checks the other binary sample by
// hand: NTP has no unpacker in our dependency set, but its shape is fixed.
func TestNTPSampleIsAFortyEightByteClientRequest(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	raw, err := checkerdef.DecodePayload(SampleNTPRequest, checkerdef.PayloadEncodingHex)
	r.NoError(err)
	r.Len(raw, 48)
	r.Equal(byte(0x1b), raw[0])                          // LI=0, VN=3, Mode=3 (client)
	r.Equal(strings.Repeat("\x00", 47), string(raw[1:])) // the rest is zeroed
}
