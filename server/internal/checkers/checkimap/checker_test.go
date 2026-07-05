package checkimap

import (
	"context"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

func TestIMAPChecker_Type(t *testing.T) {
	t.Parallel()

	checker := &IMAPChecker{}
	r := require.New(t)
	r.Equal(checkerdef.CheckTypeIMAP, checker.Type())
}

func TestIMAPConfig_FromMap(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		configMap map[string]any
		wantErr   bool
		errMsg    string
		validate  func(*testing.T, *IMAPConfig)
	}{
		{
			name: "valid config with all fields",
			configMap: map[string]any{
				"host":            "imap.example.com",
				"port":            993,
				"tls":             true,
				"tls_verify":      true,
				"tls_server_name": "imap.example.com",
				"timeout":         "10s",
				"expect_greeting": "IMAP4rev1",
				"username":        "user",
				"password":        "pass",
			},
			validate: func(t *testing.T, cfg *IMAPConfig) {
				t.Helper()
				r := require.New(t)
				r.Equal("imap.example.com", cfg.Host)
				r.Equal(993, cfg.Port)
				r.True(cfg.TLS)
				r.True(cfg.TLSVerify)
				r.Equal("imap.example.com", cfg.TLSServerName)
				r.Equal(10*time.Second, cfg.Timeout)
				r.Equal("IMAP4rev1", cfg.ExpectGreeting)
				r.Equal("user", cfg.Username)
				r.Equal("pass", cfg.Password)
			},
		},
		{
			name: "minimal config",
			configMap: map[string]any{
				"host": "mail.example.com",
			},
			validate: func(t *testing.T, cfg *IMAPConfig) {
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
				"port": 143.0,
			},
			validate: func(t *testing.T, cfg *IMAPConfig) {
				t.Helper()
				require.New(t).Equal(143, cfg.Port)
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
			configMap: map[string]any{"host": "x", "port": "993"},
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

			cfg := &IMAPConfig{}
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

func TestIMAPConfig_Validate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		config  IMAPConfig
		wantErr bool
		errMsg  string
	}{
		{
			name:   "valid minimal",
			config: IMAPConfig{Host: "mail.example.com"},
		},
		{
			name:   "valid with port",
			config: IMAPConfig{Host: "mail.example.com", Port: 993},
		},
		{
			name:    "empty host",
			config:  IMAPConfig{},
			wantErr: true,
			errMsg:  "host: is required",
		},
		{
			name:    "port too high",
			config:  IMAPConfig{Host: "x", Port: 65536},
			wantErr: true,
			errMsg:  "port: must be between 1 and 65535, got 65536",
		},
		{
			name:    "negative port",
			config:  IMAPConfig{Host: "x", Port: -1},
			wantErr: true,
			errMsg:  "port: must be between 1 and 65535, got -1",
		},
		{
			name:    "timeout too long",
			config:  IMAPConfig{Host: "x", Timeout: 61 * time.Second},
			wantErr: true,
			errMsg:  "timeout: must be > 0 and <= 60s, got 1m1s",
		},
		{
			name:    "tls and starttls both set",
			config:  IMAPConfig{Host: "x", TLS: true, StartTLS: true},
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

func TestIMAPConfig_GetConfig(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	cfg := &IMAPConfig{
		Host:           "imap.example.com",
		Port:           993,
		TLS:            true,
		TLSVerify:      true,
		TLSServerName:  "imap.example.com",
		Timeout:        10 * time.Second,
		ExpectGreeting: "IMAP4rev1",
		Username:       "user",
		Password:       "pass",
	}

	result := cfg.GetConfig()
	r.Equal("imap.example.com", result["host"])
	r.Equal(993, result["port"])
	r.Equal(true, result["tls"])
	r.Equal(true, result["tls_verify"])
	r.Equal("imap.example.com", result["tls_server_name"])
	r.Equal("10s", result["timeout"])
	r.Equal("IMAP4rev1", result["expect_greeting"])
	r.Equal("user", result["username"])
	r.Equal("pass", result["password"])
}

func TestIMAPConfig_GetConfig_Minimal(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	cfg := &IMAPConfig{Host: "mail.example.com"}
	result := cfg.GetConfig()

	r.Equal("mail.example.com", result["host"])
	r.Nil(result["port"])
	r.Nil(result["timeout"])
	r.Nil(result["tls"])
}

// startFakeIMAPServer starts a simple IMAP server for testing.
func startFakeIMAPServer(t *testing.T, opts fakeIMAPOpts) (string, int) {
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

			go handleFakeIMAP(conn, opts)
		}
	}()

	return "127.0.0.1", port
}

type fakeIMAPOpts struct {
	greeting       string
	rejectGreeting bool
	silent         bool // accept but never write anything
	silentAfter    bool // write greeting, then go silent
	rejectLogin    bool
}

func handleFakeIMAP(conn net.Conn, opts fakeIMAPOpts) {
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
		greeting = "* OK fake.imap.local IMAP4rev1 Fake ready"
	}

	if opts.rejectGreeting {
		_, _ = fmt.Fprintf(conn, "* BYE Service not available\r\n")

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
		fields := strings.SplitN(line, " ", 2)

		if len(fields) < 2 { //nolint:mnd // tag + command
			_, _ = fmt.Fprintf(conn, "%s BAD unknown command\r\n", fields[0])

			continue
		}

		tag, rest := fields[0], fields[1]
		cmd := strings.ToUpper(strings.SplitN(rest, " ", 2)[0])

		switch cmd {
		case "STARTTLS":
			_, _ = fmt.Fprintf(conn, "%s OK Begin TLS negotiation now\r\n", tag)

			return // real TLS handshake would follow; not exercised here

		case "LOGIN":
			if opts.rejectLogin {
				_, _ = fmt.Fprintf(conn, "%s NO LOGIN failed\r\n", tag)

				continue
			}

			_, _ = fmt.Fprintf(conn, "%s OK LOGIN completed\r\n", tag)

		case "LOGOUT":
			_, _ = fmt.Fprintf(conn, "* BYE logging out\r\n%s OK LOGOUT completed\r\n", tag)

			return

		default:
			_, _ = fmt.Fprintf(conn, "%s BAD unknown command\r\n", tag)
		}
	}
}

func TestIMAPChecker_Execute(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		imapOpts       fakeIMAPOpts
		configOverride func(host string, port int) *IMAPConfig
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
			imapOpts:       fakeIMAPOpts{rejectGreeting: true},
			expectedStatus: checkerdef.StatusDown,
			checkOutput: func(t *testing.T, output map[string]any) {
				t.Helper()
				r := require.New(t)
				r.Contains(output["error"], "greeting does not start with")
			},
		},
		{
			name:     "login success",
			imapOpts: fakeIMAPOpts{},
			configOverride: func(host string, port int) *IMAPConfig {
				return &IMAPConfig{
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
			name:     "login rejected",
			imapOpts: fakeIMAPOpts{rejectLogin: true},
			configOverride: func(host string, port int) *IMAPConfig {
				return &IMAPConfig{
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
				r.Contains(output["error"], "LOGIN failed")
			},
		},
		{
			name:     "expect greeting match",
			imapOpts: fakeIMAPOpts{greeting: "* OK mail.example.com IMAP4rev1 Server Ready"},
			configOverride: func(host string, port int) *IMAPConfig {
				return &IMAPConfig{
					Host:           host,
					Port:           port,
					Timeout:        2 * time.Second,
					ExpectGreeting: "IMAP4rev1",
				}
			},
			expectedStatus: checkerdef.StatusUp,
		},
		{
			name:     "expect greeting mismatch",
			imapOpts: fakeIMAPOpts{greeting: "* OK mail.example.com Dovecot Ready"},
			configOverride: func(host string, port int) *IMAPConfig {
				return &IMAPConfig{
					Host:           host,
					Port:           port,
					Timeout:        2 * time.Second,
					ExpectGreeting: "IMAP4rev1",
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

			host, port := startFakeIMAPServer(t, tt.imapOpts)

			var cfg *IMAPConfig
			if tt.configOverride != nil {
				cfg = tt.configOverride(host, port)
			} else {
				cfg = &IMAPConfig{
					Host:    host,
					Port:    port,
					Timeout: 2 * time.Second,
				}
			}

			checker := &IMAPChecker{}
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

// TestIMAPChecker_Execute_SilentListener is the regression test for the
// o365-style hang: a listener that accepts the TCP connection and never
// writes a single byte (e.g. an implicit-TLS port hit without TLS enabled,
// so the server silently waits for a ClientHello that never comes). Without
// the SetDeadline fix, the greeting ReadLine blocks forever and this test
// hangs; with the fix it must return StatusTimeout within ~1.5s.
func TestIMAPChecker_Execute_SilentListener(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	host, port := startFakeIMAPServer(t, fakeIMAPOpts{silent: true})

	cfg := &IMAPConfig{
		Host:    host,
		Port:    port,
		Timeout: 1 * time.Second,
	}

	done := make(chan struct {
		result *checkerdef.Result
		err    error
	}, 1)

	checker := &IMAPChecker{}

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

// TestIMAPChecker_Execute_MidProtocolSilence covers silence that starts
// after a valid greeting: the LOGIN response read must also time out
// rather than hang forever.
func TestIMAPChecker_Execute_MidProtocolSilence(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	host, port := startFakeIMAPServer(t, fakeIMAPOpts{silentAfter: true})

	cfg := &IMAPConfig{
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

	checker := &IMAPChecker{}

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

func TestIMAPChecker_Execute_ConnectionRefused(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	checker := &IMAPChecker{}
	cfg := &IMAPConfig{
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

func TestIMAPChecker_Execute_InvalidHost(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	checker := &IMAPChecker{}
	cfg := &IMAPConfig{
		Host:    "this-host-does-not-exist-12345.invalid",
		Port:    143,
		Timeout: 2 * time.Second,
	}

	result, err := checker.Execute(context.Background(), cfg)
	r.NoError(err)
	r.NotNil(result)
	r.Equal(checkerdef.StatusError, result.Status)
	r.Contains(result.Output["error"], "resolve")
}

func TestIMAPChecker_Validate(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	checker := &IMAPChecker{}

	// Valid
	spec := &checkerdef.CheckSpec{Config: map[string]any{"host": "mail.example.com"}}
	r.NoError(checker.Validate(spec))
	r.Equal("IMAP: mail.example.com", spec.Name)
	r.Equal("imap-mail.example.com", spec.Slug)

	// Invalid - missing host
	spec2 := &checkerdef.CheckSpec{Config: map[string]any{}}
	r.Error(checker.Validate(spec2))
}
