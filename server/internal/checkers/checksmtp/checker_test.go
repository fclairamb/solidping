package checksmtp

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

func TestSMTPChecker_Type(t *testing.T) {
	t.Parallel()

	checker := &SMTPChecker{}
	r := require.New(t)
	r.Equal(checkerdef.CheckTypeSMTP, checker.Type())
}

func TestSMTPConfig_FromMap(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		configMap map[string]any
		wantErr   bool
		errMsg    string
		validate  func(*testing.T, *SMTPConfig)
	}{
		{
			name: "valid config with all fields",
			configMap: map[string]any{
				"host":            "smtp.example.com",
				"port":            587,
				"timeout":         "10s",
				"starttls":        true,
				"tls_verify":      true,
				"tls_server_name": "smtp.example.com",
				"ehlo_domain":     "monitoring.example.com",
				"expect_greeting": "Postfix",
				"check_auth":      true,
			},
			validate: func(t *testing.T, cfg *SMTPConfig) {
				t.Helper()
				r := require.New(t)
				r.Equal("smtp.example.com", cfg.Host)
				r.Equal(587, cfg.Port)
				r.Equal(10*time.Second, cfg.Timeout)
				r.True(cfg.StartTLS)
				r.True(cfg.TLSVerify)
				r.Equal("smtp.example.com", cfg.TLSServerName)
				r.Equal("monitoring.example.com", cfg.EHLODomain)
				r.Equal("Postfix", cfg.ExpectGreeting)
				r.True(cfg.CheckAuth)
			},
		},
		{
			name: "minimal config",
			configMap: map[string]any{
				"host": "mail.example.com",
			},
			validate: func(t *testing.T, cfg *SMTPConfig) {
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
				"port": 587.0,
			},
			validate: func(t *testing.T, cfg *SMTPConfig) {
				t.Helper()
				require.New(t).Equal(587, cfg.Port)
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
			configMap: map[string]any{"host": "x", "port": "587"},
			wantErr:   true,
			errMsg:    "port: must be a number",
		},
		{
			name:      "invalid timeout type",
			configMap: map[string]any{"host": "x", "timeout": 10},
			wantErr:   true,
			errMsg:    "timeout: must be a string",
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
			name:      "invalid ehlo_domain type",
			configMap: map[string]any{"host": "x", "ehlo_domain": 123},
			wantErr:   true,
			errMsg:    "ehlo_domain: must be a string",
		},
		{
			name:      "invalid expect_greeting type",
			configMap: map[string]any{"host": "x", "expect_greeting": 123},
			wantErr:   true,
			errMsg:    "expect_greeting: must be a string",
		},
		{
			name:      "invalid check_auth type",
			configMap: map[string]any{"host": "x", "check_auth": "true"},
			wantErr:   true,
			errMsg:    "check_auth: must be a boolean",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)

			cfg := &SMTPConfig{}
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

func TestSMTPConfig_Validate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		config  SMTPConfig
		wantErr bool
		errMsg  string
	}{
		{
			name:   "valid minimal",
			config: SMTPConfig{Host: "mail.example.com"},
		},
		{
			name:   "valid with port",
			config: SMTPConfig{Host: "mail.example.com", Port: 587},
		},
		{
			name:    "empty host",
			config:  SMTPConfig{},
			wantErr: true,
			errMsg:  "host: is required",
		},
		{
			name:    "port too high",
			config:  SMTPConfig{Host: "x", Port: 65536},
			wantErr: true,
			errMsg:  "port: must be between 1 and 65535, got 65536",
		},
		{
			name:    "negative port",
			config:  SMTPConfig{Host: "x", Port: -1},
			wantErr: true,
			errMsg:  "port: must be between 1 and 65535, got -1",
		},
		{
			name:    "timeout too long",
			config:  SMTPConfig{Host: "x", Timeout: 61 * time.Second},
			wantErr: true,
			errMsg:  "timeout: must be > 0 and <= 60s, got 1m1s",
		},
		{
			name:    "starttls with port 465",
			config:  SMTPConfig{Host: "x", Port: 465, StartTLS: true},
			wantErr: true,
			errMsg:  "starttls: cannot use STARTTLS with port 465 (implicit TLS)",
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

func TestSMTPConfig_GetConfig(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	cfg := &SMTPConfig{
		Host:           "smtp.example.com",
		Port:           587,
		Timeout:        10 * time.Second,
		StartTLS:       true,
		TLSVerify:      true,
		TLSServerName:  "smtp.example.com",
		EHLODomain:     "monitoring.example.com",
		ExpectGreeting: "Postfix",
		CheckAuth:      true,
	}

	result := cfg.GetConfig()
	r.Equal("smtp.example.com", result["host"])
	r.Equal(587, result["port"])
	r.Equal("10s", result["timeout"])
	r.Equal(true, result["starttls"])
	r.Equal(true, result["tls_verify"])
	r.Equal("smtp.example.com", result["tls_server_name"])
	r.Equal("monitoring.example.com", result["ehlo_domain"])
	r.Equal("Postfix", result["expect_greeting"])
	r.Equal(true, result["check_auth"])
}

func TestSMTPConfig_GetConfig_Minimal(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	cfg := &SMTPConfig{Host: "mail.example.com"}
	result := cfg.GetConfig()

	r.Equal("mail.example.com", result["host"])
	r.Nil(result["port"])
	r.Nil(result["timeout"])
	r.Nil(result["starttls"])
}

// startFakeSMTPServer starts a simple SMTP server for testing.
func startFakeSMTPServer(t *testing.T, opts fakeSMTPOpts) (string, int) {
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

			go handleFakeSMTP(conn, opts)
		}
	}()

	return "127.0.0.1", port
}

type fakeSMTPOpts struct {
	greeting       string
	rejectGreeting bool
	capabilities   []string
	rejectEHLO     bool
	supportAuth    bool
	authMechanisms string
}

func handleFakeSMTP(conn net.Conn, opts fakeSMTPOpts) {
	defer func() { _ = conn.Close() }()

	greeting := opts.greeting
	if greeting == "" {
		greeting = "fake.smtp.local ESMTP Fake"
	}

	if opts.rejectGreeting {
		_, _ = fmt.Fprintf(conn, "421 Service not available\r\n")

		return
	}

	_, _ = fmt.Fprintf(conn, "220 %s\r\n", greeting)

	buf := make([]byte, 1024)

	for {
		bytesRead, err := conn.Read(buf)
		if err != nil {
			return
		}

		line := strings.TrimSpace(string(buf[:bytesRead]))

		switch {
		case strings.HasPrefix(strings.ToUpper(line), "EHLO"):
			if opts.rejectEHLO {
				_, _ = fmt.Fprintf(conn, "550 Not accepted\r\n")

				continue
			}

			writeEHLOResponse(conn, opts)

		case strings.HasPrefix(strings.ToUpper(line), "QUIT"):
			_, _ = fmt.Fprintf(conn, "221 Bye\r\n")

			return

		default:
			_, _ = fmt.Fprintf(conn, "500 Unknown command\r\n")
		}
	}
}

func writeEHLOResponse(conn net.Conn, opts fakeSMTPOpts) {
	caps := opts.capabilities
	if caps == nil {
		caps = []string{"PIPELINING", "SIZE 52428800", "8BITMIME"}
	}

	if opts.supportAuth {
		authLine := "AUTH PLAIN LOGIN"
		if opts.authMechanisms != "" {
			authLine = "AUTH " + opts.authMechanisms
		}

		caps = append(caps, authLine)
	}

	for _, cap := range caps[:len(caps)-1] {
		_, _ = fmt.Fprintf(conn, "250-%s\r\n", cap)
	}

	_, _ = fmt.Fprintf(conn, "250 %s\r\n", caps[len(caps)-1])
}

func TestSMTPChecker_Execute(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		smtpOpts       fakeSMTPOpts
		configOverride func(host string, port int) *SMTPConfig
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
				r.NotNil(output["ehlo_capabilities"])
			},
		},
		{
			name:           "greeting rejected",
			smtpOpts:       fakeSMTPOpts{rejectGreeting: true},
			expectedStatus: checkerdef.StatusDown,
			checkOutput: func(t *testing.T, output map[string]any) {
				t.Helper()
				r := require.New(t)
				r.Contains(output["error"], "greeting rejected")
			},
		},
		{
			name:           "EHLO rejected",
			smtpOpts:       fakeSMTPOpts{rejectEHLO: true},
			expectedStatus: checkerdef.StatusDown,
			checkOutput: func(t *testing.T, output map[string]any) {
				t.Helper()
				r := require.New(t)
				r.Contains(output["error"], "EHLO")
			},
		},
		{
			name:     "expect greeting match",
			smtpOpts: fakeSMTPOpts{greeting: "mail.example.com ESMTP Postfix"},
			configOverride: func(host string, port int) *SMTPConfig {
				return &SMTPConfig{
					Host:           host,
					Port:           port,
					Timeout:        2 * time.Second,
					ExpectGreeting: "Postfix",
				}
			},
			expectedStatus: checkerdef.StatusUp,
		},
		{
			name:     "expect greeting mismatch",
			smtpOpts: fakeSMTPOpts{greeting: "mail.example.com ESMTP Sendmail"},
			configOverride: func(host string, port int) *SMTPConfig {
				return &SMTPConfig{
					Host:           host,
					Port:           port,
					Timeout:        2 * time.Second,
					ExpectGreeting: "Postfix",
				}
			},
			expectedStatus: checkerdef.StatusDown,
			checkOutput: func(t *testing.T, output map[string]any) {
				t.Helper()
				r := require.New(t)
				r.Contains(output["error"], "greeting does not contain")
			},
		},
		{
			name:     "check auth success",
			smtpOpts: fakeSMTPOpts{supportAuth: true},
			configOverride: func(host string, port int) *SMTPConfig {
				return &SMTPConfig{
					Host:      host,
					Port:      port,
					Timeout:   2 * time.Second,
					CheckAuth: true,
				}
			},
			expectedStatus: checkerdef.StatusUp,
			checkOutput: func(t *testing.T, output map[string]any) {
				t.Helper()
				r := require.New(t)
				mechanisms, ok := output["auth_mechanisms"].([]string)
				r.True(ok)
				r.Contains(mechanisms, "PLAIN")
			},
		},
		{
			name:     "check auth missing",
			smtpOpts: fakeSMTPOpts{},
			configOverride: func(host string, port int) *SMTPConfig {
				return &SMTPConfig{
					Host:      host,
					Port:      port,
					Timeout:   2 * time.Second,
					CheckAuth: true,
				}
			},
			expectedStatus: checkerdef.StatusDown,
			checkOutput: func(t *testing.T, output map[string]any) {
				t.Helper()
				r := require.New(t)
				r.Contains(output["error"], "AUTH not advertised")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)

			host, port := startFakeSMTPServer(t, tt.smtpOpts)

			var cfg *SMTPConfig
			if tt.configOverride != nil {
				cfg = tt.configOverride(host, port)
			} else {
				cfg = &SMTPConfig{
					Host:    host,
					Port:    port,
					Timeout: 2 * time.Second,
				}
			}

			checker := &SMTPChecker{}
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

func TestSMTPChecker_Execute_ConnectionRefused(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	checker := &SMTPChecker{}
	cfg := &SMTPConfig{
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

func TestSMTPChecker_Execute_InvalidHost(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	checker := &SMTPChecker{}
	cfg := &SMTPConfig{
		Host:    "this-host-does-not-exist-12345.invalid",
		Port:    25,
		Timeout: 2 * time.Second,
	}

	result, err := checker.Execute(context.Background(), cfg)
	r.NoError(err)
	r.NotNil(result)
	r.Equal(checkerdef.StatusError, result.Status)
	r.Contains(result.Output["error"], "resolve")
}

func TestSMTPChecker_Validate(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	checker := &SMTPChecker{}

	// Valid
	spec := &checkerdef.CheckSpec{Config: map[string]any{"host": "mail.example.com"}}
	r.NoError(checker.Validate(spec))
	r.Equal("SMTP: mail.example.com", spec.Name)
	r.Equal("smtp-mail.example.com", spec.Slug)

	// Invalid - missing host
	spec2 := &checkerdef.CheckSpec{Config: map[string]any{}}
	r.Error(checker.Validate(spec2))
}

func TestParseEHLOResponse(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	msg := "mail.example.com\nPIPELINING\nSIZE 52428800\nSTARTTLS\n" +
		"AUTH LOGIN PLAIN CRAM-MD5\nENHANCEDSTATUSCODES\n8BITMIME"
	caps := parseEHLOResponse(msg)

	r.Contains(caps.names, "STARTTLS")
	r.Contains(caps.names, "AUTH")
	r.True(caps.hasStartTLS)
	r.True(caps.hasAuth)
	r.Equal([]string{"LOGIN", "PLAIN", "CRAM-MD5"}, caps.authMechanisms)
}
