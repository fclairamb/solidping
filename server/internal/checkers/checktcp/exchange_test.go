package checktcp

import (
	"context"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

// listenerFunc serves exactly one connection with the given handler and returns
// the port it bound. Everything runs on 127.0.0.1:0 so these tests are part of
// `make test`, not the slow layer.
func listenerFunc(t *testing.T, handle func(net.Conn)) int {
	t.Helper()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	t.Cleanup(func() { _ = listener.Close() })

	go func() {
		for {
			conn, acceptErr := listener.Accept()
			if acceptErr != nil {
				return
			}

			go func() {
				defer func() { _ = conn.Close() }()
				handle(conn)
			}()
		}
	}()

	port, err := strconv.Atoi(strings.Split(listener.Addr().String(), ":")[1])
	require.NoError(t, err)

	return port
}

func runTCP(t *testing.T, cfg *TCPConfig) *checkerdef.Result {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	result, err := (&TCPChecker{}).Execute(ctx, cfg)
	require.NoError(t, err)

	return result
}

// TestTCPExchange_BannerSplitAcrossTwoWrites is gap 3: one conn.Read is not
// "wait for a reply". The old checker matched whatever the FIRST segment
// carried, so a banner written in two write()s failed nondeterministically.
func TestTCPExchange_BannerSplitAcrossTwoWrites(t *testing.T) {
	t.Parallel()

	port := listenerFunc(t, func(conn net.Conn) {
		_, _ = conn.Write([]byte("220 mail.acme.com"))
		time.Sleep(100 * time.Millisecond)
		_, _ = conn.Write([]byte(" ESMTP Postfix\r\n"))
		time.Sleep(time.Second)
	})

	result := runTCP(t, &TCPConfig{
		Host:          localhost,
		Port:          port,
		Timeout:       3 * time.Second,
		ExpectPattern: `^220 .* ESMTP`,
	})

	r := require.New(t)
	r.Equal(checkerdef.StatusUp, result.Status, result.Output)
	r.GreaterOrEqual(result.Output[checkerdef.OutputKeyReadCount].(int), 2)
	r.Contains(result.Output[checkerdef.OutputKeyReceivedData], "ESMTP Postfix")
}

// TestTCPExchange_MatchPastTheOutputCap is gap 4: the match used to run against
// the 1 KB copy kept for the OUTPUT field, so an expectation arriving after
// byte 1024 could never match. `positive control` below is the same reply with
// the marker INSIDE the first kilobyte — it must match either way, which is
// what proves the first case is measuring the cap and not the plumbing.
func TestTCPExchange_MatchPastTheOutputCap(t *testing.T) {
	t.Parallel()

	const marker = "SOLIDPING-MARKER"

	tests := []struct {
		name   string
		body   string
		expect checkerdef.Status
	}{
		{
			// 3 KB reply, marker at offset ~2000: the regression case.
			name:   "marker past offset 1024",
			body:   strings.Repeat("x", 2000) + marker + strings.Repeat("y", 1000),
			expect: checkerdef.StatusUp,
		},
		{
			// Positive control: the same shape with the marker inside the cap.
			name:   "marker inside the first kilobyte",
			body:   strings.Repeat("x", 100) + marker + strings.Repeat("y", 2900),
			expect: checkerdef.StatusUp,
		},
		{
			// Negative control: the marker is genuinely absent.
			name:   "marker absent",
			body:   strings.Repeat("x", 3000),
			expect: checkerdef.StatusDown,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			body := tt.body
			port := listenerFunc(t, func(conn net.Conn) {
				_, _ = conn.Write([]byte(body))
				time.Sleep(1500 * time.Millisecond)
			})

			result := runTCP(t, &TCPConfig{
				Host:       localhost,
				Port:       port,
				Timeout:    2 * time.Second,
				ExpectData: marker,
			})

			r := require.New(t)
			r.Equal(tt.expect, result.Status, result.Output)

			// Whatever the verdict, the OUTPUT copy stays capped at 1 KB.
			r.LessOrEqual(
				len(result.Output[checkerdef.OutputKeyReceivedData].(string)),
				checkerdef.MaxExchangeOutputSize)
		})
	}
}

// TestTCPExchange_SilentPeerIsTimeout is gap 6 and gap 7 at once: an open port
// behind a mute service is Timeout (not a generic Down), it lands within the
// configured timeout, and the WHOLE check — dial, write, wait — respects that
// one timeout instead of re-arming it per stage.
func TestTCPExchange_SilentPeerIsTimeout(t *testing.T) {
	t.Parallel()

	port := listenerFunc(t, func(_ net.Conn) {
		time.Sleep(10 * time.Second) // accept, then say nothing
	})

	start := time.Now()
	result := runTCP(t, &TCPConfig{
		Host:         localhost,
		Port:         port,
		Timeout:      2 * time.Second,
		SendData:     `EHLO acme\r\n`,
		SendEncoding: checkerdef.PayloadEncodingEscaped,
		ExpectData:   "250",
	})
	elapsed := time.Since(start)

	r := require.New(t)
	r.Equal(checkerdef.StatusTimeout, result.Status, result.Output)
	r.Contains(result.Output[checkerdef.OutputKeyError], "no matching reply within 2s")
	r.Contains(result.Output[checkerdef.OutputKeyError], "(0 bytes received)")
	// Three independent `now + timeout` deadlines would have let this run ~6s.
	r.Less(elapsed, 3*time.Second, "the whole exchange must share one deadline")
	r.Greater(elapsed, 1800*time.Millisecond)
}

// TestTCPExchange_PeerClosesWithoutMatch is exit 3 of the read loop.
func TestTCPExchange_PeerClosesWithoutMatch(t *testing.T) {
	t.Parallel()

	port := listenerFunc(t, func(conn net.Conn) {
		_, _ = conn.Write([]byte("500 go away\r\n"))
	})

	result := runTCP(t, &TCPConfig{
		Host:       localhost,
		Port:       port,
		Timeout:    2 * time.Second,
		ExpectData: "250",
	})

	r := require.New(t)
	r.Equal(checkerdef.StatusDown, result.Status, result.Output)
	r.Contains(result.Output[checkerdef.OutputKeyError], "connection closed after 13 bytes")
	r.Contains(result.Output[checkerdef.OutputKeyError], "without a matching reply")
}

// TestTCPExchange_NoMatchInFourKilobytes is exit 2 of the read loop: the 4 KB
// cap stays, and hitting it is a distinct, named failure.
func TestTCPExchange_NoMatchInFourKilobytes(t *testing.T) {
	t.Parallel()

	port := listenerFunc(t, func(conn net.Conn) {
		for range 5 {
			if _, err := conn.Write([]byte(strings.Repeat("z", 1024))); err != nil {
				return
			}
		}

		time.Sleep(2 * time.Second)
	})

	result := runTCP(t, &TCPConfig{
		Host:       localhost,
		Port:       port,
		Timeout:    3 * time.Second,
		ExpectData: "never-appears",
	})

	r := require.New(t)
	r.Equal(checkerdef.StatusDown, result.Status, result.Output)
	r.Contains(result.Output[checkerdef.OutputKeyError], "no match in the first 4096 bytes of the reply")
}

// TestTCPExchange_BinaryReplyIsEscapedAndHexMatchable is gaps 5 and 6: a binary
// payload goes out as bytes, a binary reply is asserted with a hex substring,
// and the output field renders it readably instead of as U+FFFD soup.
func TestTCPExchange_BinaryReplyIsEscapedAndHexMatchable(t *testing.T) {
	t.Parallel()

	port := listenerFunc(t, func(conn net.Conn) {
		buf := make([]byte, 16)

		n, err := conn.Read(buf)
		if err != nil {
			return
		}

		// Echo the request back with a 0xff 0xfe prefix — invalid UTF-8.
		_, _ = conn.Write(append([]byte{0xff, 0xfe}, buf[:n]...))
		time.Sleep(time.Second)
	})

	result := runTCP(t, &TCPConfig{
		Host:           localhost,
		Port:           port,
		Timeout:        2 * time.Second,
		SendData:       "de ad be ef",
		SendEncoding:   checkerdef.PayloadEncodingHex,
		ExpectData:     "fffedeadbeef",
		ExpectEncoding: checkerdef.PayloadEncodingHex,
	})

	r := require.New(t)
	r.Equal(checkerdef.StatusUp, result.Status, result.Output)
	r.Equal(`\xff\xfe\xde\xad\xbe\xef`, result.Output[checkerdef.OutputKeyReceivedData])
	r.Equal(4, result.Metrics[checkerdef.OutputKeyBytesSent])
	r.Equal(6, result.Metrics[checkerdef.OutputKeyBytesReceived])
}

// TestTCPExchange_SendWithoutExpectationSurvivesSilence keeps today's contract:
// a payload with nothing asserted about the reply is a diagnostics-only read
// and must NOT fail when the peer says nothing.
func TestTCPExchange_SendWithoutExpectationSurvivesSilence(t *testing.T) {
	t.Parallel()

	port := listenerFunc(t, func(_ net.Conn) {
		time.Sleep(3 * time.Second)
	})

	start := time.Now()
	result := runTCP(t, &TCPConfig{
		Host:     localhost,
		Port:     port,
		Timeout:  1 * time.Second,
		SendData: "hello",
	})

	r := require.New(t)
	r.Equal(checkerdef.StatusUp, result.Status, result.Output)
	r.Less(time.Since(start), 2*time.Second)
}

// TestTCPValidate_RejectsBadPayloadConfig proves the VALIDATION_ERROR contract:
// an uncompilable pattern or a malformed payload is refused at SAVE time.
func TestTCPValidate_RejectsBadPayloadConfig(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		config map[string]any
		want   string
	}{
		{
			name:   "uncompilable expect_pattern",
			config: map[string]any{"host": exampleHost, "port": 443, "expect_pattern": "(unclosed"},
			want:   "expect_pattern",
		},
		{
			name: "odd-length hex payload",
			config: map[string]any{
				"host": exampleHost, "port": 443, "send_data": "abc", "send_encoding": "hex",
			},
			want: "send_data",
		},
		{
			name: "unknown encoding",
			config: map[string]any{
				"host": exampleHost, "port": 443, "send_data": "x", "send_encoding": "base64",
			},
			want: "send_encoding",
		},
		{
			name: "a valid pattern is accepted",
			config: map[string]any{
				"host": exampleHost, "port": 443, "expect_pattern": `^HTTP/1\.[01] 200`,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			r := require.New(t)
			spec := &checkerdef.CheckSpec{Config: tt.config}

			err := (&TCPChecker{}).Validate(spec)
			if tt.want == "" {
				r.NoError(err)

				return
			}

			r.Error(err)
			r.Contains(err.Error(), tt.want)
		})
	}
}
