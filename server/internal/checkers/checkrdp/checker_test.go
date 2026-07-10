package checkrdp

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"io"
	"math/big"
	"net"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

// makeTLSCert builds a self-signed server certificate expiring at notAfter —
// the shape RDP servers typically present.
func makeTLSCert(t *testing.T, notAfter time.Time) tls.Certificate {
	t.Helper()
	r := require.New(t)

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	r.NoError(err)

	template := x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "rdp-test"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     notAfter,
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
	}

	der, err := x509.CreateCertificate(rand.Reader, &template, &template, &key.PublicKey, key)
	r.NoError(err)

	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}

// fakeServerOpts drives the canned behavior of the in-process fake RDP server.
type fakeServerOpts struct {
	// response is written after the 19-byte Connection Request is read.
	// nil with silent=false closes the connection right away.
	response []byte
	// tlsCert, when set, continues with a server-side TLS handshake after the
	// response is written (the way a real TLS/NLA-selecting server would).
	tlsCert *tls.Certificate
	// silent accepts the connection and never answers (nor closes) until the
	// test ends — the "unresponsive listener" case.
	silent bool
}

// startFakeRDP starts a single-connection fake RDP server on 127.0.0.1:0 and
// returns its host and port. The real dial path is exercised — no injectable
// function seam is needed.
func startFakeRDP(t *testing.T, opts fakeServerOpts) (string, int) {
	t.Helper()
	r := require.New(t)

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	r.NoError(err)

	done := make(chan struct{})
	t.Cleanup(func() {
		_ = listener.Close()
		close(done)
	})

	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}

		defer func() { _ = conn.Close() }()

		// Read the client's 19-byte Connection Request.
		request := make([]byte, len(buildConnectionRequest()))
		if _, err := io.ReadFull(conn, request); err != nil {
			return
		}

		if opts.silent {
			<-done

			return
		}

		if opts.response != nil {
			if _, err := conn.Write(opts.response); err != nil {
				return
			}
		}

		if opts.tlsCert != nil {
			tlsConn := tls.Server(conn, &tls.Config{
				Certificates: []tls.Certificate{*opts.tlsCert},
				MinVersion:   tls.VersionTLS12,
			})
			if err := tlsConn.Handshake(); err != nil {
				return
			}
		}

		// Hold the connection until the client is done with it.
		<-done
	}()

	host, portStr, err := net.SplitHostPort(listener.Addr().String())
	r.NoError(err)

	port, err := strconv.Atoi(portStr)
	r.NoError(err)

	return host, port
}

// executeRDP runs the checker against host:port with the extra config merged in.
func executeRDP(t *testing.T, host string, port int, extra map[string]any) *checkerdef.Result {
	t.Helper()
	r := require.New(t)

	configMap := map[string]any{
		"host":    host,
		"port":    port,
		"timeout": "2s",
	}
	for k, v := range extra {
		configMap[k] = v
	}

	cfg := &RDPConfig{}
	r.NoError(cfg.FromMap(configMap))
	r.NoError(cfg.Validate())

	checker := &RDPChecker{}
	result, err := checker.Execute(context.Background(), cfg)
	r.NoError(err)
	r.NotNil(result)

	return result
}

func TestExecute_NegRspNLA_Up(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	cert := makeTLSCert(t, time.Now().Add(365*24*time.Hour))
	host, port := startFakeRDP(t, fakeServerOpts{
		response: ccPacket(negRspPayload(flagRestrictedAdminModeSupported, ProtocolHybrid)),
		tlsCert:  &cert,
	})

	result := executeRDP(t, host, port, map[string]any{"require_nla": true})

	r.Equal(checkerdef.StatusUp, result.Status)
	r.Equal("nla", result.Output["selected_protocol"])
	r.Equal([]string{"RESTRICTED_ADMIN_MODE_SUPPORTED"}, result.Output["server_flags"])
	r.Equal("rdp-test", result.Output["cert_subject"])
	r.Equal("rdp-test", result.Output["cert_issuer"])
	r.Equal(true, result.Output["cert_self_signed"])
	r.NotEmpty(result.Output["cert_expires_at"])
	r.Contains(result.Metrics, "connect_ms")
	r.Contains(result.Metrics, "handshake_ms")
	r.Contains(result.Metrics, "tls_handshake_ms")
	r.InDelta(364, result.Metrics["days_remaining"], 1)
}

func TestExecute_RequireNLAButSSLSelected_Down(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	host, port := startFakeRDP(t, fakeServerOpts{
		response: ccPacket(negRspPayload(0, ProtocolSSL)),
	})

	result := executeRDP(t, host, port, map[string]any{"require_nla": true})

	r.Equal(checkerdef.StatusDown, result.Status)
	r.Contains(result.Output[checkerdef.OutputKeyError], "NLA required")
	r.Contains(result.Output[checkerdef.OutputKeyError], "tls")
}

func TestExecute_NegFailure_Down(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	host, port := startFakeRDP(t, fakeServerOpts{
		response: ccPacket(negFailurePayload(failureHybridRequiredByServer)),
	})

	result := executeRDP(t, host, port, nil)

	r.Equal(checkerdef.StatusDown, result.Status)
	r.Equal("HYBRID_REQUIRED_BY_SERVER", result.Output["failure_code"])
	r.Contains(result.Output[checkerdef.OutputKeyError], "HYBRID_REQUIRED_BY_SERVER")
}

func TestExecute_PlainCC_UpStandardSecurity(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	host, port := startFakeRDP(t, fakeServerOpts{response: ccPacket(nil)})

	result := executeRDP(t, host, port, nil)

	r.Equal(checkerdef.StatusUp, result.Status)
	r.Equal("rdp", result.Output["selected_protocol"])
	r.NotContains(result.Output, "server_flags")
	r.NotContains(result.Metrics, "days_remaining")
}

func TestExecute_PlainCCWithRequireNLA_Down(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	host, port := startFakeRDP(t, fakeServerOpts{response: ccPacket(nil)})

	result := executeRDP(t, host, port, map[string]any{"require_nla": true})

	r.Equal(checkerdef.StatusDown, result.Status)
	r.Contains(result.Output[checkerdef.OutputKeyError], "NLA required")
}

func TestExecute_CertExpiryThresholds(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		notAfter   time.Time
		extra      map[string]any
		wantStatus checkerdef.Status
	}{
		{
			name:       "inside warning window",
			notAfter:   time.Now().Add(10 * 24 * time.Hour),
			extra:      map[string]any{"warning_days": 30, "critical_days": 5},
			wantStatus: checkerdef.StatusWarning,
		},
		{
			name:       "inside critical window",
			notAfter:   time.Now().Add(10 * 24 * time.Hour),
			extra:      map[string]any{"warning_days": 30, "critical_days": 15},
			wantStatus: checkerdef.StatusDown,
		},
		{
			name:       "outside both windows",
			notAfter:   time.Now().Add(100 * 24 * time.Hour),
			extra:      map[string]any{"warning_days": 30, "critical_days": 15},
			wantStatus: checkerdef.StatusUp,
		},
		{
			name:       "expired cert with thresholds off",
			notAfter:   time.Now().Add(-24 * time.Hour),
			extra:      nil,
			wantStatus: checkerdef.StatusDown,
		},
		{
			name:       "near expiry with thresholds off",
			notAfter:   time.Now().Add(10 * 24 * time.Hour),
			extra:      nil,
			wantStatus: checkerdef.StatusUp,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)

			cert := makeTLSCert(t, tc.notAfter)
			host, port := startFakeRDP(t, fakeServerOpts{
				response: ccPacket(negRspPayload(0, ProtocolSSL)),
				tlsCert:  &cert,
			})

			result := executeRDP(t, host, port, tc.extra)

			r.Equal(tc.wantStatus, result.Status, "output: %v", result.Output)
			r.Contains(result.Metrics, "days_remaining")
		})
	}
}

func TestExecute_ConnectionRefused_Down(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	// Grab a port that nothing listens on.
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	r.NoError(err)

	host, portStr, err := net.SplitHostPort(listener.Addr().String())
	r.NoError(err)
	port, err := strconv.Atoi(portStr)
	r.NoError(err)
	r.NoError(listener.Close())

	result := executeRDP(t, host, port, nil)

	r.Equal(checkerdef.StatusDown, result.Status)
	r.Contains(result.Output[checkerdef.OutputKeyError], "connection failed")
}

func TestExecute_UnresponsiveListener_Timeout(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	host, port := startFakeRDP(t, fakeServerOpts{silent: true})

	result := executeRDP(t, host, port, map[string]any{"timeout": "300ms"})

	r.Equal(checkerdef.StatusTimeout, result.Status)
	r.Contains(result.Output[checkerdef.OutputKeyError], "timeout")
}

func TestExecute_NonRDPService_Down(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	host, port := startFakeRDP(t, fakeServerOpts{
		response: []byte("HTTP/1.1 400 Bad Request\r\nContent-Length: 0\r\n\r\n"),
	})

	result := executeRDP(t, host, port, nil)

	r.Equal(checkerdef.StatusDown, result.Status)
	r.Contains(result.Output[checkerdef.OutputKeyError], "RDP negotiation failed")
}

func TestExecute_TLSHandshakeFails_Down(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	// Server selects a TLS-based protocol but never performs the TLS
	// handshake (no tlsCert): the client's handshake read hits the held-open
	// connection and the context deadline fires — but the server holding the
	// socket without TLS records is indistinguishable from silence, so cut
	// the connection instead by closing right after the response.
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	r.NoError(err)
	t.Cleanup(func() { _ = listener.Close() })

	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}

		request := make([]byte, len(buildConnectionRequest()))
		if _, err := io.ReadFull(conn, request); err != nil {
			_ = conn.Close()

			return
		}

		_, _ = conn.Write(ccPacket(negRspPayload(0, ProtocolSSL)))
		_ = conn.Close() // abort before the TLS handshake
	}()

	host, portStr, err := net.SplitHostPort(listener.Addr().String())
	r.NoError(err)
	port, err := strconv.Atoi(portStr)
	r.NoError(err)

	result := executeRDP(t, host, port, nil)

	r.Equal(checkerdef.StatusDown, result.Status)
	r.Contains(result.Output[checkerdef.OutputKeyError], "TLS handshake failed")
}

func TestRDPChecker_Type(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	r.Equal(checkerdef.CheckTypeRDP, (&RDPChecker{}).Type())
}

func TestRDPChecker_ValidateNameSlugAutofill(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	spec := &checkerdef.CheckSpec{Config: map[string]any{"host": "rdp.example.internal"}}
	r.NoError((&RDPChecker{}).Validate(spec))
	r.Equal("RDP: rdp.example.internal", spec.Name)
	r.Equal("rdp-rdp-example-internal", spec.Slug)
}

func TestRDPConfig_Validate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		config  map[string]any
		wantErr string
	}{
		{name: "valid minimal", config: map[string]any{"host": "rdp.example.internal"}},
		{name: "valid full", config: map[string]any{
			"host": "rdp.example.internal", "port": 3390, "timeout": "10s",
			"require_nla": true, "warning_days": 30, "critical_days": 7,
		}},
		{name: "missing host", config: map[string]any{}, wantErr: "host"},
		{name: "port too large", config: map[string]any{"host": "h", "port": 70000}, wantErr: "port"},
		{name: "port negative", config: map[string]any{"host": "h", "port": -1}, wantErr: "port"},
		{name: "timeout too large", config: map[string]any{"host": "h", "timeout": "2m"}, wantErr: "timeout"},
		{name: "warning below critical", config: map[string]any{
			"host": "h", "warning_days": 5, "critical_days": 30,
		}, wantErr: "warning_days"},
		{name: "warning only is fine", config: map[string]any{"host": "h", "warning_days": 30}},
		{name: "critical only is fine", config: map[string]any{"host": "h", "critical_days": 7}},
		{name: "warning above cap", config: map[string]any{"host": "h", "warning_days": 4000}, wantErr: "warning_days"},
		{name: "critical above cap", config: map[string]any{"host": "h", "critical_days": 4000}, wantErr: "critical_days"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)

			cfg := &RDPConfig{}
			r.NoError(cfg.FromMap(tc.config))

			err := cfg.Validate()
			if tc.wantErr != "" {
				r.Error(err)
				r.Contains(err.Error(), tc.wantErr)

				return
			}

			r.NoError(err)
		})
	}
}

func TestRDPConfig_ValidateAppliesPortDefault(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	cfg := &RDPConfig{Host: "rdp.example.internal"}
	r.NoError(cfg.Validate())
	r.Equal(defaultPort, cfg.Port)
}

func TestRDPConfig_FromMapTypeErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		config  map[string]any
		wantErr string
	}{
		{name: "host not a string", config: map[string]any{"host": 42}, wantErr: "host"},
		{name: "port not a number", config: map[string]any{"port": "x"}, wantErr: "port"},
		{name: "timeout not a duration", config: map[string]any{"timeout": "zzz"}, wantErr: "timeout"},
		{name: "timeout not a string", config: map[string]any{"timeout": 5}, wantErr: "timeout"},
		{name: "require_nla not a bool", config: map[string]any{"require_nla": "yes"}, wantErr: "require_nla"},
		{name: "warning_days not a number", config: map[string]any{"warning_days": "x"}, wantErr: "warning_days"},
		{name: "critical_days not a number", config: map[string]any{"critical_days": "x"}, wantErr: "critical_days"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)

			err := (&RDPConfig{}).FromMap(tc.config)
			r.Error(err)
			r.Contains(err.Error(), tc.wantErr)
		})
	}
}

func TestRDPConfig_GetConfigRoundTrip(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	original := &RDPConfig{
		Host:         "rdp.example.internal",
		Port:         3390,
		Timeout:      10 * time.Second,
		RequireNLA:   true,
		WarningDays:  30,
		CriticalDays: 7,
	}

	restored := &RDPConfig{}
	r.NoError(restored.FromMap(original.GetConfig()))
	r.Equal(original, restored)
}

func TestGetSampleConfigs_Valid(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	checker := &RDPChecker{}
	samples := checker.GetSampleConfigs(nil)
	r.NotEmpty(samples)

	for i := range samples {
		r.NoError(checker.Validate(&samples[i]))
		r.NotEmpty(samples[i].Name)
		r.NotEmpty(samples[i].Slug)
	}
}
