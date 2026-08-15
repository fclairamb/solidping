package checksip

import (
	"context"
	"net"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

func TestSIPChecker_Type(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	r.Equal(checkerdef.CheckTypeSIP, (&SIPChecker{}).Type())
}

func TestSIPConfig_FromMap(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		configMap map[string]any
		wantErr   bool
		validate  func(*require.Assertions, *SIPConfig)
	}{
		{
			name: "full config with defaults parsing",
			configMap: map[string]any{
				"host":            "pbx.example.com",
				"port":            float64(5070),
				"transport":       "tcp",
				"mode":            "register",
				"domain":          "example.com",
				"username":        "1001",
				"password":        "secret",
				"auth_username":   "auth1001",
				"expect_status":   "200,405",
				"timeout":         "7s",
				"tls_verify":      true,
				"tls_server_name": "sip.example.com",
			},
			validate: func(r *require.Assertions, c *SIPConfig) {
				r.Equal("pbx.example.com", c.Host)
				r.Equal(5070, c.Port)
				r.Equal("tcp", c.Transport)
				r.Equal("register", c.Mode)
				r.Equal("example.com", c.Domain)
				r.Equal("1001", c.Username)
				r.Equal("secret", c.Password)
				r.Equal("auth1001", c.AuthUsername)
				r.Equal("200,405", c.ExpectStatus)
				r.Equal(7*time.Second, c.Timeout)
				r.True(c.TLSVerify)
				r.Equal("sip.example.com", c.TLSServerName)
			},
		},
		{
			name:      "minimal host only",
			configMap: map[string]any{"host": "sip.example.com"},
			validate: func(r *require.Assertions, c *SIPConfig) {
				r.Equal("sip.example.com", c.Host)
				r.Equal(0, c.Port)
				r.Empty(c.Transport)
			},
		},
		{
			name:      "bad host type",
			configMap: map[string]any{"host": 123},
			wantErr:   true,
		},
		{
			name:      "bad port type",
			configMap: map[string]any{"host": "x", "port": "5060"},
			wantErr:   true,
		},
		{
			name:      "bad timeout type",
			configMap: map[string]any{"host": "x", "timeout": 5},
			wantErr:   true,
		},
		{
			name:      "bad timeout value",
			configMap: map[string]any{"host": "x", "timeout": "not-a-duration"},
			wantErr:   true,
		},
		{
			name:      "bad tls_verify type",
			configMap: map[string]any{"host": "x", "tls_verify": "yes"},
			wantErr:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			r := require.New(t)
			cfg := &SIPConfig{}
			err := cfg.FromMap(tt.configMap)

			if tt.wantErr {
				r.Error(err)

				return
			}

			r.NoError(err)

			if tt.validate != nil {
				tt.validate(r, cfg)
			}
		})
	}
}

func TestSIPConfig_GetConfigRoundTrip(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	orig := &SIPConfig{
		Host:         "pbx.example.com",
		Port:         5070,
		Transport:    "tls",
		Mode:         "register",
		Domain:       "example.com",
		Username:     "1001",
		Password:     "secret",
		AuthUsername: "auth",
		ExpectStatus: "200",
		Timeout:      9 * time.Second,
		TLSVerify:    true,
	}

	restored := &SIPConfig{}
	r.NoError(restored.FromMap(orig.GetConfig()))
	r.Equal(orig, restored)
}

func TestSIPConfig_SecretFields(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	r.Equal([]string{"password"}, (&SIPConfig{}).SecretFields())
}

func TestSIPChecker_Validate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		config   map[string]any
		wantErr  bool
		wantSlug string
	}{
		{name: "valid options", config: map[string]any{"host": "sip.example.com"}, wantSlug: "sip-sip-example-com"},
		{name: "empty host", config: map[string]any{"transport": "udp"}, wantErr: true},
		{name: "bad transport", config: map[string]any{"host": "x", "transport": "sctp"}, wantErr: true},
		{name: "bad mode", config: map[string]any{"host": "x", "mode": "invite"}, wantErr: true},
		{
			name:    "register without credentials",
			config:  map[string]any{"host": "x", "mode": "register"},
			wantErr: true,
		},
		{
			name:    "register without password",
			config:  map[string]any{"host": "x", "mode": "register", "username": "1001"},
			wantErr: true,
		},
		{
			name:     "register with credentials",
			config:   map[string]any{"host": "x", "mode": "register", "username": "1001", "password": "p"},
			wantSlug: "sip-x",
		},
		{name: "port too high", config: map[string]any{"host": "x", "port": float64(70000)}, wantErr: true},
		{name: "port zero ok", config: map[string]any{"host": "x", "port": float64(0)}, wantSlug: "sip-x"},
		{name: "timeout too high", config: map[string]any{"host": "x", "timeout": "120s"}, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			r := require.New(t)
			spec := &checkerdef.CheckSpec{Config: tt.config}
			err := (&SIPChecker{}).Validate(spec)

			if tt.wantErr {
				r.Error(err)
			} else {
				r.NoError(err)
				r.NotEqual("x", spec.Slug, "auto-generated slug must never fall back to the bare sanitize-padding value")
				r.Equal(tt.wantSlug, spec.Slug)
			}
		})
	}
}

func TestParseStatusLine(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		raw        string
		wantCode   int
		wantReason string
		wantOK     bool
	}{
		{name: "200 OK", raw: "SIP/2.0 200 OK\r\nVia: x\r\n", wantCode: 200, wantReason: "OK", wantOK: true},
		{
			name: "401 with multiword reason", raw: "SIP/2.0 401 Unauthorized\r\n",
			wantCode: 401, wantReason: "Unauthorized", wantOK: true,
		},
		{
			name: "405 method not allowed", raw: "SIP/2.0 405 Method Not Allowed\r\n",
			wantCode: 405, wantReason: "Method Not Allowed", wantOK: true,
		},
		{name: "garbage", raw: "HELLO WORLD\r\n", wantOK: false},
		{name: "empty", raw: "", wantOK: false},
		{name: "bad code", raw: "SIP/2.0 abc Oops\r\n", wantOK: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			r := require.New(t)
			code, reason, ok := parseStatusLine(tt.raw)
			r.Equal(tt.wantOK, ok)

			if tt.wantOK {
				r.Equal(tt.wantCode, code)
				r.Equal(tt.wantReason, reason)
			}
		})
	}
}

func TestStatusMatches(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	r.True(statusMatches("200", 200))
	r.True(statusMatches("200,405", 405))
	r.True(statusMatches(" 200 , 405 ", 200))
	r.False(statusMatches("200", 405))
	r.False(statusMatches("", 200))
}

func TestParseAuthChallenge(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	header := `Digest realm="asterisk", nonce="1a2b3c", qop="auth", algorithm=MD5, opaque="abcd"`
	ch := parseAuthChallenge(header)
	r.Equal("asterisk", ch["realm"])
	r.Equal("1a2b3c", ch["nonce"])
	r.Equal("auth", ch["qop"])
	r.Equal("MD5", ch["algorithm"])
	r.Equal("abcd", ch["opaque"])

	// qop with comma inside quotes must not split the param.
	ch2 := parseAuthChallenge(`Digest realm="r", nonce="n", qop="auth,auth-int"`)
	r.Equal("auth,auth-int", ch2["qop"])
	r.Equal("r", ch2["realm"])
}

// TestBuildDigestAuthorization uses the canonical RFC 2617 §3.5 example vectors.
func TestBuildDigestAuthorization(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	// RFC 2617 example: realm="testrealm@host.com", user "Mufasa",
	// password "Circle Of Life", nonce "dcd98b7102dd2f0e8b11d0f600bfb0c093",
	// method GET, uri "/dir/index.html", qop=auth, nc=00000001,
	// cnonce "0a4f113b" -> response 6629fae49393a05397450978507c4ef1.
	ch := map[string]string{
		"realm":     "testrealm@host.com",
		"nonce":     "dcd98b7102dd2f0e8b11d0f600bfb0c093",
		"qop":       "auth",
		"algorithm": "MD5",
		"cnonce":    "0a4f113b",
	}

	auth := buildDigestAuthorization(ch, "GET", "/dir/index.html", "Mufasa", "Circle Of Life")
	r.Contains(auth, `response="6629fae49393a05397450978507c4ef1"`)
	r.Contains(auth, `nc=00000001`)
	r.Contains(auth, `cnonce="0a4f113b"`)
	r.Contains(auth, `qop=auth`)

	// Legacy (no qop) variant. HA1=MD5(Mufasa:testrealm@host.com:Circle Of Life),
	// HA2=MD5(GET:/dir/index.html), response=MD5(HA1:nonce:HA2).
	chLegacy := map[string]string{
		"realm": "testrealm@host.com",
		"nonce": "dcd98b7102dd2f0e8b11d0f600bfb0c093",
	}
	authLegacy := buildDigestAuthorization(chLegacy, "GET", "/dir/index.html", "Mufasa", "Circle Of Life")
	ha1 := md5Hex("Mufasa:testrealm@host.com:Circle Of Life")
	ha2 := md5Hex("GET:/dir/index.html")
	want := md5Hex(strings.Join([]string{ha1, "dcd98b7102dd2f0e8b11d0f600bfb0c093", ha2}, ":"))
	r.Contains(authLegacy, `response="`+want+`"`)
	r.NotContains(authLegacy, "qop=")
}

// hostPort splits an "ip:port" address into host and integer port.
func hostPort(t *testing.T, addr string) (string, int) {
	t.Helper()

	host, portStr, err := net.SplitHostPort(addr)
	require.NoError(t, err)

	port, err := strconv.Atoi(portStr)
	require.NoError(t, err)

	return host, port
}

// fakeUDPServer answers SIP datagrams with a fixed response. If respond is
// empty it stays silent (to exercise the timeout path).
func fakeUDPServer(t *testing.T, respond string) (string, int) {
	t.Helper()

	var lc net.ListenConfig

	pc, err := lc.ListenPacket(context.Background(), "udp", "127.0.0.1:0")
	require.NoError(t, err)

	t.Cleanup(func() { _ = pc.Close() })

	go func() {
		buf := make([]byte, 4096)

		for {
			_, addr, err := pc.ReadFrom(buf)
			if err != nil {
				return
			}

			if respond != "" {
				_, _ = pc.WriteTo([]byte(respond), addr)
			}
		}
	}()

	return hostPort(t, pc.LocalAddr().String())
}

// fakeTCPServer answers each incoming connection with the next response in
// order (one response per connection). The checker dials a fresh connection
// per SIP transaction, so a REGISTER handshake consumes two responses.
func fakeTCPServer(t *testing.T, responses []string) (string, int) {
	t.Helper()

	var lc net.ListenConfig

	ln, err := lc.Listen(context.Background(), "tcp", "127.0.0.1:0")
	require.NoError(t, err)

	t.Cleanup(func() { _ = ln.Close() })

	go func() {
		idx := 0

		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}

			resp := ""
			if idx < len(responses) {
				resp = responses[idx]
			}

			idx++

			go handleTCPConn(conn, resp)
		}
	}()

	return hostPort(t, ln.Addr().String())
}

func handleTCPConn(conn net.Conn, resp string) {
	defer func() { _ = conn.Close() }()

	buf := make([]byte, 4096)
	if _, err := conn.Read(buf); err != nil {
		return
	}

	_, _ = conn.Write([]byte(resp))
}

func runExecute(t *testing.T, cfg *SIPConfig) *checkerdef.Result {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	res, err := (&SIPChecker{}).Execute(ctx, cfg)
	require.NoError(t, err)
	require.NotNil(t, res)

	return res
}

func TestSIPChecker_Execute_Options(t *testing.T) {
	t.Parallel()

	const ok200 = "SIP/2.0 200 OK\r\nServer: Asterisk PBX 18.10.0\r\nContent-Length: 0\r\n\r\n"
	const nok405 = "SIP/2.0 405 Method Not Allowed\r\nContent-Length: 0\r\n\r\n"
	const garbage = "this is not sip at all\r\n\r\n"

	type tc struct {
		name         string
		respond      string
		expectStatus string
		wantStatus   checkerdef.Status
		wantSIPCode  int
	}

	cases := []tc{
		{name: "200 OK -> Up", respond: ok200, wantStatus: checkerdef.StatusUp, wantSIPCode: 200},
		{name: "405 -> Up (inversion)", respond: nok405, wantStatus: checkerdef.StatusUp, wantSIPCode: 405},
		{
			name: "405 with expect_status=200 -> Down", respond: nok405,
			expectStatus: "200", wantStatus: checkerdef.StatusDown, wantSIPCode: 405,
		},
		{name: "garbage -> Down", respond: garbage, wantStatus: checkerdef.StatusDown},
	}

	for _, transport := range []string{"udp", "tcp"} {
		for _, c := range cases {
			t.Run(transport+"/"+c.name, func(t *testing.T) {
				t.Parallel()

				r := require.New(t)

				var host string

				var port int

				if transport == "udp" {
					host, port = fakeUDPServer(t, c.respond)
				} else {
					host, port = fakeTCPServer(t, []string{c.respond})
				}

				cfg := &SIPConfig{
					Host:         host,
					Port:         port,
					Transport:    transport,
					Mode:         "options",
					ExpectStatus: c.expectStatus,
					Timeout:      2 * time.Second,
				}

				res := runExecute(t, cfg)
				r.Equal(c.wantStatus, res.Status, "output: %v", res.Output)
				r.Contains(res.Metrics, "response_time_ms")
				r.Equal(transport, res.Output["transport"])

				if c.wantSIPCode != 0 {
					r.Equal(c.wantSIPCode, res.Output["sip_status"])
					r.Equal(c.wantSIPCode, res.Metrics["sip_status_code"])
				}
			})
		}
	}
}

func TestSIPChecker_Execute_Options_Timeout(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	host, port := fakeUDPServer(t, "") // silent server

	cfg := &SIPConfig{Host: host, Port: port, Transport: "udp", Mode: "options", Timeout: 500 * time.Millisecond}
	res := runExecute(t, cfg)
	r.Equal(checkerdef.StatusTimeout, res.Status)
}

func TestSIPChecker_Execute_Options_Refused(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	// Bind a TCP listener then close it to get a refused/closed port.
	var lc net.ListenConfig

	ln, err := lc.Listen(context.Background(), "tcp", "127.0.0.1:0")
	r.NoError(err)
	_, port := hostPort(t, ln.Addr().String())
	r.NoError(ln.Close())

	cfg := &SIPConfig{Host: "127.0.0.1", Port: port, Transport: "tcp", Mode: "options", Timeout: 2 * time.Second}
	res := runExecute(t, cfg)
	r.Equal(checkerdef.StatusDown, res.Status)
}

func TestSIPChecker_Execute_Register(t *testing.T) {
	t.Parallel()

	challenge := "SIP/2.0 401 Unauthorized\r\n" +
		`WWW-Authenticate: Digest realm="asterisk", nonce="abc123", qop="auth", algorithm=MD5` + "\r\n" +
		"Content-Length: 0\r\n\r\n"
	ok200 := "SIP/2.0 200 OK\r\nServer: Kamailio\r\nContent-Length: 0\r\n\r\n"

	t.Run("401 then 200 -> Up with valid Authorization and incremented CSeq", func(t *testing.T) {
		t.Parallel()

		r := require.New(t)

		var (
			mu       sync.Mutex
			requests []string
		)

		var lc net.ListenConfig

		ln, err := lc.Listen(context.Background(), "tcp", "127.0.0.1:0")
		r.NoError(err)

		t.Cleanup(func() { _ = ln.Close() })

		// The checker dials a fresh connection per transaction, so the server
		// accepts two connections: the first gets the challenge, the second
		// (carrying the digest) gets the 200 OK.
		responses := []string{challenge, ok200}

		go func() {
			for _, resp := range responses {
				conn, err := ln.Accept()
				if err != nil {
					return
				}

				buf := make([]byte, 8192)

				n, err := conn.Read(buf)
				if err != nil {
					_ = conn.Close()

					return
				}

				mu.Lock()
				requests = append(requests, string(buf[:n]))
				mu.Unlock()

				_, _ = conn.Write([]byte(resp))
				_ = conn.Close()
			}
		}()

		_, port := hostPort(t, ln.Addr().String())
		cfg := &SIPConfig{
			Host:      "127.0.0.1",
			Port:      port,
			Transport: "tcp",
			Mode:      "register",
			Domain:    "asterisk",
			Username:  "1001",
			Password:  "secret",
			Timeout:   2 * time.Second,
		}

		res := runExecute(t, cfg)
		r.Equal(checkerdef.StatusUp, res.Status, "output: %v", res.Output)
		r.Equal(200, res.Output["sip_status"])

		mu.Lock()
		defer mu.Unlock()
		r.Len(requests, 2)

		// First request carries no Authorization.
		r.NotContains(requests[0], "Authorization:")

		// Second request carries a correct digest Authorization.
		r.Contains(requests[1], "Authorization: Digest")
		r.Contains(requests[1], `username="1001"`)
		r.Contains(requests[1], `realm="asterisk"`)
		r.Contains(requests[1], `nonce="abc123"`)

		// CSeq must increment from 1 to 2.
		r.Equal(1, cseqOf(requests[0]))
		r.Equal(2, cseqOf(requests[1]))
	})

	t.Run("401 twice -> Down", func(t *testing.T) {
		t.Parallel()

		r := require.New(t)
		host, port := fakeTCPServer(t, []string{challenge, challenge})

		cfg := &SIPConfig{
			Host:      host,
			Port:      port,
			Transport: "tcp",
			Mode:      "register",
			Domain:    "asterisk",
			Username:  "1001",
			Password:  "wrong",
			Timeout:   2 * time.Second,
		}

		res := runExecute(t, cfg)
		r.Equal(checkerdef.StatusDown, res.Status, "output: %v", res.Output)
	})
}

// cseqOf extracts the numeric CSeq sequence from a raw SIP request.
func cseqOf(raw string) int {
	val := headerValue(raw, "CSeq")
	fields := strings.Fields(val)

	if len(fields) == 0 {
		return -1
	}

	n, err := strconv.Atoi(fields[0])
	if err != nil {
		return -1
	}

	return n
}

func TestSIPChecker_GetSampleConfigs(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	samples := (&SIPChecker{}).GetSampleConfigs(nil)
	r.Len(samples, 2)

	for _, s := range samples {
		r.NotEmpty(s.Name)
		r.NotEmpty(s.Slug)
		r.NotEmpty(s.Config["host"])
	}
}
