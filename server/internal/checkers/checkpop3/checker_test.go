package checkpop3

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"fmt"
	"math/big"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

func TestPOP3Checker_Type(t *testing.T) {
	t.Parallel()

	checker := &POP3Checker{}
	r := require.New(t)
	r.Equal(checkerdef.CheckTypePOP3, checker.Type())
}

func TestPOP3Config_FromMap(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		configMap map[string]any
		wantErr   bool
		errMsg    string
		validate  func(*testing.T, *POP3Config)
	}{
		{
			name: "valid config with all fields",
			configMap: map[string]any{
				"host":            "pop.example.com",
				"port":            995,
				"tls":             true,
				"tls_verify":      true,
				"tls_server_name": "pop.example.com",
				"timeout":         "10s",
				"expect_greeting": "POP3",
				"username":        "user",
				"password":        "pass",
			},
			validate: func(t *testing.T, cfg *POP3Config) {
				t.Helper()
				r := require.New(t)
				r.Equal("pop.example.com", cfg.Host)
				r.Equal(995, cfg.Port)
				r.True(cfg.TLS)
				r.True(cfg.TLSVerify)
				r.Equal("pop.example.com", cfg.TLSServerName)
				r.Equal(10*time.Second, cfg.Timeout)
				r.Equal("POP3", cfg.ExpectGreeting)
				r.Equal("user", cfg.Username)
				r.Equal("pass", cfg.Password)
			},
		},
		{
			name: "minimal config",
			configMap: map[string]any{
				"host": "mail.example.com",
			},
			validate: func(t *testing.T, cfg *POP3Config) {
				t.Helper()
				r := require.New(t)
				r.Equal("mail.example.com", cfg.Host)
				r.Equal(0, cfg.Port)
			},
		},
		{
			name: "port as float64",
			configMap: map[string]any{
				"host": "mail.example.com",
				"port": 110.0,
			},
			validate: func(t *testing.T, cfg *POP3Config) {
				t.Helper()
				require.New(t).Equal(110, cfg.Port)
			},
		},
		{
			name:      "invalid host type",
			configMap: map[string]any{"host": 123},
			wantErr:   true,
			errMsg:    "host: must be a string",
		},
		{
			name:      "invalid port type",
			configMap: map[string]any{"host": "x", "port": "995"},
			wantErr:   true,
			errMsg:    "port: must be a number",
		},
		{
			name:      "invalid tls type",
			configMap: map[string]any{"host": "x", "tls": "true"},
			wantErr:   true,
			errMsg:    "tls: must be a boolean",
		},
		{
			name:      "invalid starttls type",
			configMap: map[string]any{"host": "x", "starttls": "true"},
			wantErr:   true,
			errMsg:    "starttls: must be a boolean",
		},
		{
			name:      "invalid tls_verify type",
			configMap: map[string]any{"host": "x", "tls_verify": "true"},
			wantErr:   true,
			errMsg:    "tls_verify: must be a boolean",
		},
		{
			name:      "invalid tls_server_name type",
			configMap: map[string]any{"host": "x", "tls_server_name": 123},
			wantErr:   true,
			errMsg:    "tls_server_name: must be a string",
		},
		{
			name:      "invalid timeout type",
			configMap: map[string]any{"host": "x", "timeout": 10},
			wantErr:   true,
			errMsg:    "timeout: must be a string",
		},
		{
			name:      "invalid expect_greeting type",
			configMap: map[string]any{"host": "x", "expect_greeting": 123},
			wantErr:   true,
			errMsg:    "expect_greeting: must be a string",
		},
		{
			name:      "invalid username type",
			configMap: map[string]any{"host": "x", "username": 123},
			wantErr:   true,
			errMsg:    "username: must be a string",
		},
		{
			name:      "invalid password type",
			configMap: map[string]any{"host": "x", "password": 123},
			wantErr:   true,
			errMsg:    "password: must be a string",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)

			cfg := &POP3Config{}
			err := cfg.FromMap(tt.configMap)

			if tt.wantErr {
				r.Error(err)
				if tt.errMsg != "" {
					r.Equal(tt.errMsg, err.Error())
				}
			} else {
				r.NoError(err)
				if tt.validate != nil {
					tt.validate(t, cfg)
				}
			}
		})
	}
}

func TestPOP3Config_Validate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		config  POP3Config
		wantErr bool
		errMsg  string
	}{
		{
			name:   "valid minimal",
			config: POP3Config{Host: "mail.example.com"},
		},
		{
			name:   "valid with port",
			config: POP3Config{Host: "mail.example.com", Port: 995},
		},
		{
			name:    "empty host",
			config:  POP3Config{},
			wantErr: true,
			errMsg:  "host: is required",
		},
		{
			name:    "port too high",
			config:  POP3Config{Host: "x", Port: 65536},
			wantErr: true,
			errMsg:  "port: must be between 1 and 65535, got 65536",
		},
		{
			name:    "negative port",
			config:  POP3Config{Host: "x", Port: -1},
			wantErr: true,
			errMsg:  "port: must be between 1 and 65535, got -1",
		},
		{
			name:    "timeout too long",
			config:  POP3Config{Host: "x", Timeout: 61 * time.Second},
			wantErr: true,
			errMsg:  "timeout: must be > 0 and <= 30s, got 1m1s",
		},
		{
			name:    "tls and starttls both set",
			config:  POP3Config{Host: "x", TLS: true, StartTLS: true},
			wantErr: true,
			errMsg:  "tls: cannot use both TLS and STARTTLS",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)

			err := tt.config.Validate()
			if tt.wantErr {
				r.Error(err)
				if tt.errMsg != "" {
					r.Equal(tt.errMsg, err.Error())
				}
			} else {
				r.NoError(err)
			}
		})
	}
}

func TestPOP3Config_GetConfig(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	cfg := &POP3Config{
		Host:           "pop.example.com",
		Port:           995,
		TLS:            true,
		TLSVerify:      true,
		TLSServerName:  "pop.example.com",
		Timeout:        10 * time.Second,
		ExpectGreeting: "POP3",
		Username:       "user",
		Password:       "pass",
	}

	result := cfg.GetConfig()
	r.Equal("pop.example.com", result["host"])
	r.Equal(995, result["port"])
	r.Equal(true, result["tls"])
	r.Equal(true, result["tls_verify"])
	r.Equal("pop.example.com", result["tls_server_name"])
	r.Equal("10s", result["timeout"])
	r.Equal("POP3", result["expect_greeting"])
	r.Equal("user", result["username"])
	r.Equal("pass", result["password"])
}

func TestPOP3Config_GetConfig_Minimal(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	cfg := &POP3Config{Host: "mail.example.com"}
	result := cfg.GetConfig()

	r.Equal("mail.example.com", result["host"])
	r.Nil(result["port"])
	r.Nil(result["timeout"])
	r.Nil(result["tls"])
}

// TestNewExecParams_ImplicitTLSDerivation covers the D1 port->TLS derivation:
// port 995 with neither tls nor starttls set implies implicit TLS (mirrors
// checksmtp/checker.go:120), an explicit starttls:true on 995 wins over the
// port-derived default, a plain port (110) stays plaintext, and an explicit
// tls:true is preserved even off the well-known port.
func TestNewExecParams_ImplicitTLSDerivation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		config      POP3Config
		wantImplTLS bool
	}{
		{
			name:        "port 995, tls and starttls unset -> implicit TLS",
			config:      POP3Config{Host: "x", Port: implicitTLSPort},
			wantImplTLS: true,
		},
		{
			name:        "port 995, starttls explicit -> explicit starttls wins",
			config:      POP3Config{Host: "x", Port: implicitTLSPort, StartTLS: true},
			wantImplTLS: false,
		},
		{
			name:        "port 110 (plaintext default) -> unchanged",
			config:      POP3Config{Host: "x", Port: defaultPort},
			wantImplTLS: false,
		},
		{
			name:        "explicit tls:true at a non-standard port is preserved",
			config:      POP3Config{Host: "x", Port: defaultPort, TLS: true},
			wantImplTLS: true,
		},
		{
			name:        "port unset, tls:true -> defaults port to 995 and implies TLS",
			config:      POP3Config{Host: "x", TLS: true},
			wantImplTLS: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			params := newExecParams(&tt.config)
			require.Equal(t, tt.wantImplTLS, params.useImplicitTLS)
		})
	}
}

// makeTestCertificate mints a self-signed key+cert pair for a loopback TLS
// listener used only to exercise the implicit-TLS dial/consumption path in
// Execute; trust is irrelevant since checks run with tls_verify unset
// (InsecureSkipVerify).
func makeTestCertificate(t *testing.T) tls.Certificate {
	t.Helper()

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)

	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: "127.0.0.1"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
	}

	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	require.NoError(t, err)

	return tls.Certificate{
		Certificate: [][]byte{der},
		PrivateKey:  key,
	}
}

// startFakePOP3TLSServer wraps startFakePOP3Server's protocol handling behind
// a real TLS listener, so Execute must complete an actual handshake — this
// exercises the params.useImplicitTLS consumption path (dial + tls_version/
// tls_cipher output), complementing the port-995-trigger unit test above.
func startFakePOP3TLSServer(t *testing.T, opts fakePOP3Opts) (string, int) {
	t.Helper()

	cert := makeTestCertificate(t)
	tlsCfg := &tls.Config{Certificates: []tls.Certificate{cert}}

	lc := &net.ListenConfig{}

	rawListener, err := lc.Listen(context.Background(), "tcp", "127.0.0.1:0")
	require.NoError(t, err)

	listener := tls.NewListener(rawListener, tlsCfg)

	_, portStr, _ := net.SplitHostPort(rawListener.Addr().String())

	var port int

	_, _ = fmt.Sscanf(portStr, "%d", &port)

	t.Cleanup(func() { _ = listener.Close() })

	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}

			go handleFakePOP3(conn, opts)
		}
	}()

	return "127.0.0.1", port
}

// TestPOP3Checker_Execute_ImplicitTLSDial proves the useImplicitTLS flag (once
// derived) is actually consumed by dial()/Execute: connecting to a real TLS
// listener with tls:true set completes a handshake and reports tls_version/
// tls_cipher, exactly as an unset-flag port-995 check would after derivation.
func TestPOP3Checker_Execute_ImplicitTLSDial(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	host, port := startFakePOP3TLSServer(t, fakePOP3Opts{})

	cfg := &POP3Config{
		Host:    host,
		Port:    port,
		TLS:     true,
		Timeout: 2 * time.Second,
	}

	checker := &POP3Checker{}
	result, err := checker.Execute(context.Background(), cfg)
	r.NoError(err)
	r.NotNil(result)
	r.Equal(checkerdef.StatusUp, result.Status, "output: %v", result.Output)
	r.NotEmpty(result.Output["tls_version"])
	r.NotEmpty(result.Output["tls_cipher"])
}

// startFakePOP3Server starts a simple POP3 server for testing.
func startFakePOP3Server(t *testing.T, opts fakePOP3Opts) (string, int) {
	t.Helper()

	lc := &net.ListenConfig{}

	listener, err := lc.Listen(context.Background(), "tcp", "127.0.0.1:0")
	require.NoError(t, err)

	_, portStr, _ := net.SplitHostPort(listener.Addr().String())

	var port int

	_, _ = fmt.Sscanf(portStr, "%d", &port)

	t.Cleanup(func() { _ = listener.Close() })

	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}

			go handleFakePOP3(conn, opts)
		}
	}()

	return "127.0.0.1", port
}

type fakePOP3Opts struct {
	greeting       string
	rejectGreeting bool
	silent         bool // accept but never write anything
	silentAfter    bool // write greeting, then go silent
	rejectUser     bool
	rejectPass     bool
}

func handleFakePOP3(conn net.Conn, opts fakePOP3Opts) {
	defer func() { _ = conn.Close() }()

	if opts.silent {
		// Accept the connection and hold it open, never writing a byte.
		buf := make([]byte, 1024)
		for {
			if _, err := conn.Read(buf); err != nil {
				return
			}
		}
	}

	greeting := opts.greeting
	if greeting == "" {
		greeting = "+OK fake.pop3.local POP3 Fake ready"
	}

	if opts.rejectGreeting {
		_, _ = fmt.Fprintf(conn, "-ERR Service not available\r\n")

		return
	}

	_, _ = fmt.Fprintf(conn, "%s\r\n", greeting)

	if opts.silentAfter {
		// Greeting sent; now hold the conn open without responding to
		// anything the client sends.
		buf := make([]byte, 1024)
		for {
			if _, err := conn.Read(buf); err != nil {
				return
			}
		}
	}

	buf := make([]byte, 1024)

	for {
		bytesRead, err := conn.Read(buf)
		if err != nil {
			return
		}

		line := strings.TrimSpace(string(buf[:bytesRead]))
		cmd := strings.ToUpper(strings.SplitN(line, " ", 2)[0])

		switch cmd {
		case "STLS":
			_, _ = fmt.Fprintf(conn, "+OK Begin TLS negotiation\r\n")

			return // real TLS handshake would follow; not exercised here

		case "USER":
			if opts.rejectUser {
				_, _ = fmt.Fprintf(conn, "-ERR unknown user\r\n")

				continue
			}

			_, _ = fmt.Fprintf(conn, "+OK send PASS\r\n")

		case "PASS":
			if opts.rejectPass {
				_, _ = fmt.Fprintf(conn, "-ERR invalid password\r\n")

				continue
			}

			_, _ = fmt.Fprintf(conn, "+OK logged in\r\n")

		case "QUIT":
			_, _ = fmt.Fprintf(conn, "+OK bye\r\n")

			return

		default:
			_, _ = fmt.Fprintf(conn, "-ERR unknown command\r\n")
		}
	}
}

func TestPOP3Checker_Execute(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		pop3Opts       fakePOP3Opts
		configOverride func(host string, port int) *POP3Config
		expectedStatus checkerdef.Status
		checkOutput    func(*testing.T, map[string]any)
	}{
		{
			name:           "successful basic connection",
			expectedStatus: checkerdef.StatusUp,
			checkOutput: func(t *testing.T, output map[string]any) {
				t.Helper()
				r := require.New(t)
				r.NotEmpty(output["host"])
				r.NotNil(output["port"])
				r.NotEmpty(output["greeting"])
			},
		},
		{
			name:           "greeting rejected",
			pop3Opts:       fakePOP3Opts{rejectGreeting: true},
			expectedStatus: checkerdef.StatusDown,
			checkOutput: func(t *testing.T, output map[string]any) {
				t.Helper()
				r := require.New(t)
				r.Contains(output["error"], "greeting does not start with")
			},
		},
		{
			name:     "auth success",
			pop3Opts: fakePOP3Opts{},
			configOverride: func(host string, port int) *POP3Config {
				return &POP3Config{
					Host:     host,
					Port:     port,
					Timeout:  2 * time.Second,
					Username: "user",
					Password: "pass",
				}
			},
			expectedStatus: checkerdef.StatusUp,
			checkOutput: func(t *testing.T, output map[string]any) {
				t.Helper()
				r := require.New(t)
				r.Equal(true, output["authenticated"])
			},
		},
		{
			name:     "USER rejected",
			pop3Opts: fakePOP3Opts{rejectUser: true},
			configOverride: func(host string, port int) *POP3Config {
				return &POP3Config{
					Host:     host,
					Port:     port,
					Timeout:  2 * time.Second,
					Username: "user",
					Password: "pass",
				}
			},
			expectedStatus: checkerdef.StatusDown,
			checkOutput: func(t *testing.T, output map[string]any) {
				t.Helper()
				r := require.New(t)
				r.Contains(output["error"], "USER rejected")
			},
		},
		{
			name:     "PASS rejected",
			pop3Opts: fakePOP3Opts{rejectPass: true},
			configOverride: func(host string, port int) *POP3Config {
				return &POP3Config{
					Host:     host,
					Port:     port,
					Timeout:  2 * time.Second,
					Username: "user",
					Password: "pass",
				}
			},
			expectedStatus: checkerdef.StatusDown,
			checkOutput: func(t *testing.T, output map[string]any) {
				t.Helper()
				r := require.New(t)
				r.Contains(output["error"], "PASS rejected")
			},
		},
		{
			name:     "expect greeting match",
			pop3Opts: fakePOP3Opts{greeting: "+OK mail.example.com POP3 Server Ready"},
			configOverride: func(host string, port int) *POP3Config {
				return &POP3Config{
					Host:           host,
					Port:           port,
					Timeout:        2 * time.Second,
					ExpectGreeting: "POP3",
				}
			},
			expectedStatus: checkerdef.StatusUp,
		},
		{
			name:     "expect greeting mismatch",
			pop3Opts: fakePOP3Opts{greeting: "+OK mail.example.com Dovecot Ready"},
			configOverride: func(host string, port int) *POP3Config {
				return &POP3Config{
					Host:           host,
					Port:           port,
					Timeout:        2 * time.Second,
					ExpectGreeting: "POP3",
				}
			},
			expectedStatus: checkerdef.StatusDown,
			checkOutput: func(t *testing.T, output map[string]any) {
				t.Helper()
				r := require.New(t)
				r.Contains(output["error"], "greeting does not contain")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)

			host, port := startFakePOP3Server(t, tt.pop3Opts)

			var cfg *POP3Config
			if tt.configOverride != nil {
				cfg = tt.configOverride(host, port)
			} else {
				cfg = &POP3Config{
					Host:    host,
					Port:    port,
					Timeout: 2 * time.Second,
				}
			}

			checker := &POP3Checker{}
			result, err := checker.Execute(context.Background(), cfg)
			r.NoError(err)
			r.NotNil(result)
			r.Equal(tt.expectedStatus, result.Status, "output: %v", result.Output)

			if tt.checkOutput != nil {
				tt.checkOutput(t, result.Output)
			}
		})
	}
}

// TestPOP3Checker_Execute_SilentListener is the regression test for a
// silent server (e.g. an implicit-TLS port hit without TLS enabled, so the
// server silently waits for a ClientHello that never comes). Without the
// SetDeadline fix, the greeting ReadLine blocks forever and this test
// hangs; with the fix it must return StatusTimeout within ~1.5s.
func TestPOP3Checker_Execute_SilentListener(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	host, port := startFakePOP3Server(t, fakePOP3Opts{silent: true})

	cfg := &POP3Config{
		Host:    host,
		Port:    port,
		Timeout: 1 * time.Second,
	}

	done := make(chan struct {
		result *checkerdef.Result
		err    error
	}, 1)

	checker := &POP3Checker{}

	go func() {
		result, err := checker.Execute(context.Background(), cfg)
		done <- struct {
			result *checkerdef.Result
			err    error
		}{result, err}
	}()

	select {
	case out := <-done:
		r.NoError(out.err)
		r.NotNil(out.result)
		r.Equal(checkerdef.StatusTimeout, out.result.Status, "output: %v", out.result.Output)
	case <-time.After(1500 * time.Millisecond):
		t.Fatal("Execute did not return within 1.5s of a 1s timeout against a silent listener")
	}
}

// TestPOP3Checker_Execute_MidProtocolSilence covers silence that starts
// after a valid greeting: the USER response read must also time out
// rather than hang forever.
func TestPOP3Checker_Execute_MidProtocolSilence(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	host, port := startFakePOP3Server(t, fakePOP3Opts{silentAfter: true})

	cfg := &POP3Config{
		Host:     host,
		Port:     port,
		Timeout:  1 * time.Second,
		Username: "user",
		Password: "pass",
	}

	done := make(chan struct {
		result *checkerdef.Result
		err    error
	}, 1)

	checker := &POP3Checker{}

	go func() {
		result, err := checker.Execute(context.Background(), cfg)
		done <- struct {
			result *checkerdef.Result
			err    error
		}{result, err}
	}()

	select {
	case out := <-done:
		r.NoError(out.err)
		r.NotNil(out.result)
		r.Equal(checkerdef.StatusTimeout, out.result.Status, "output: %v", out.result.Output)
	case <-time.After(1500 * time.Millisecond):
		t.Fatal("Execute did not return within 1.5s of a 1s timeout against mid-protocol silence")
	}
}

func TestPOP3Checker_Execute_ConnectionRefused(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	checker := &POP3Checker{}
	cfg := &POP3Config{
		Host:    "127.0.0.1",
		Port:    1,
		Timeout: 1 * time.Second,
	}

	result, err := checker.Execute(context.Background(), cfg)
	r.NoError(err)
	r.NotNil(result)
	r.Equal(checkerdef.StatusDown, result.Status)
	r.Contains(result.Output["error"], "connection failed")
}

func TestPOP3Checker_Execute_InvalidHost(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	checker := &POP3Checker{}
	cfg := &POP3Config{
		Host:    "this-host-does-not-exist-12345.invalid",
		Port:    110,
		Timeout: 2 * time.Second,
	}

	result, err := checker.Execute(context.Background(), cfg)
	r.NoError(err)
	r.NotNil(result)
	r.Equal(checkerdef.StatusError, result.Status)
	r.Contains(result.Output["error"], "resolve")
}

func TestPOP3Checker_Validate(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	checker := &POP3Checker{}

	// Valid
	spec := &checkerdef.CheckSpec{Config: map[string]any{"host": "mail.example.com"}}
	r.NoError(checker.Validate(spec))
	r.Equal("POP3: mail.example.com", spec.Name)
	r.Equal("pop3-mail.example.com", spec.Slug)

	// Invalid - missing host
	spec2 := &checkerdef.CheckSpec{Config: map[string]any{}}
	r.Error(checker.Validate(spec2))
}
