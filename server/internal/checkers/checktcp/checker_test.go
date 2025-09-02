package checktcp

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

const exampleHost = "example.com"

func TestTCPChecker_Type(t *testing.T) {
	t.Parallel()

	checker := &TCPChecker{}
	if checker.Type() != checkerdef.CheckTypeTCP {
		t.Errorf("expected type %s, got %s", checkerdef.CheckTypeTCP, checker.Type())
	}
}

func TestTCPConfig_FromMap(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		configMap map[string]any
		wantErr   bool
		errMsg    string
		validate  func(*testing.T, *TCPConfig)
	}{
		{
			name: "valid config with all fields",
			configMap: map[string]any{
				"host":            exampleHost,
				"port":            443,
				"timeout":         "10s",
				"send_data":       "GET / HTTP/1.1\r\n\r\n",
				"expect_data":     "200 OK",
				"tls":             true,
				"tls_verify":      true,
				"tls_server_name": exampleHost,
			},
			wantErr: false,
			validate: func(t *testing.T, cfg *TCPConfig) {
				t.Helper()
				if cfg.Host != exampleHost {
					t.Errorf("expected host '%s', got '%s'", exampleHost, cfg.Host)
				}
				if cfg.Port != 443 {
					t.Errorf("expected port 443, got %d", cfg.Port)
				}
				if cfg.Timeout != 10*time.Second {
					t.Errorf("expected timeout 10s, got %v", cfg.Timeout)
				}
				if cfg.SendData != "GET / HTTP/1.1\r\n\r\n" {
					t.Errorf("expected send_data 'GET / HTTP/1.1\\r\\n\\r\\n', got '%s'", cfg.SendData)
				}
				if cfg.ExpectData != "200 OK" {
					t.Errorf("expected expect_data '200 OK', got '%s'", cfg.ExpectData)
				}
				if !cfg.TLS {
					t.Error("expected tls to be true")
				}
				if !cfg.TLSVerify {
					t.Error("expected tls_verify to be true")
				}
				if cfg.TLSServerName != exampleHost {
					t.Errorf("expected tls_server_name '%s', got '%s'", exampleHost, cfg.TLSServerName)
				}
			},
		},
		{
			name: "minimal valid config",
			configMap: map[string]any{
				"host": "localhost",
				"port": 80,
			},
			wantErr: false,
			validate: func(t *testing.T, cfg *TCPConfig) {
				t.Helper()
				if cfg.Host != "localhost" {
					t.Errorf("expected host 'localhost', got '%s'", cfg.Host)
				}
				if cfg.Port != 80 {
					t.Errorf("expected port 80, got %d", cfg.Port)
				}
			},
		},
		{
			name: "port as float64 (JSON unmarshal)",
			configMap: map[string]any{
				"host": "exampleHost",
				"port": 443.0,
			},
			wantErr: false,
			validate: func(t *testing.T, cfg *TCPConfig) {
				t.Helper()
				if cfg.Port != 443 {
					t.Errorf("expected port 443, got %d", cfg.Port)
				}
			},
		},
		{
			name: "invalid host type",
			configMap: map[string]any{
				"host": 123,
				"port": 80,
			},
			wantErr: true,
			errMsg:  "host: must be a string",
		},
		{
			name: "invalid port type",
			configMap: map[string]any{
				"host": "exampleHost",
				"port": "80",
			},
			wantErr: true,
			errMsg:  "port: must be a number",
		},
		{
			name: "invalid timeout type",
			configMap: map[string]any{
				"host":    "exampleHost",
				"port":    80,
				"timeout": 123,
			},
			wantErr: true,
			errMsg:  "timeout: must be a string",
		},
		{
			name: "invalid send_data type",
			configMap: map[string]any{
				"host":      "exampleHost",
				"port":      80,
				"send_data": 123,
			},
			wantErr: true,
			errMsg:  "send_data: must be a string",
		},
		{
			name: "invalid expect_data type",
			configMap: map[string]any{
				"host":        "exampleHost",
				"port":        80,
				"expect_data": 123,
			},
			wantErr: true,
			errMsg:  "expect_data: must be a string",
		},
		{
			name: "invalid tls type",
			configMap: map[string]any{
				"host": "exampleHost",
				"port": 443,
				"tls":  "true",
			},
			wantErr: true,
			errMsg:  "tls: must be a boolean",
		},
		{
			name: "invalid tls_verify type",
			configMap: map[string]any{
				"host":       "exampleHost",
				"port":       443,
				"tls_verify": "true",
			},
			wantErr: true,
			errMsg:  "tls_verify: must be a boolean",
		},
		{
			name: "invalid tls_server_name type",
			configMap: map[string]any{
				"host":            "exampleHost",
				"port":            443,
				"tls_server_name": 123,
			},
			wantErr: true,
			errMsg:  "tls_server_name: must be a string",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			cfg := &TCPConfig{}
			err := cfg.FromMap(tt.configMap)

			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error containing '%s', got nil", tt.errMsg)
				}
				if tt.errMsg != "" && err.Error() != tt.errMsg {
					t.Errorf("expected error '%s', got '%s'", tt.errMsg, err.Error())
				}
			} else {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if tt.validate != nil {
					tt.validate(t, cfg)
				}
			}
		})
	}
}

func TestTCPConfig_GetConfig(t *testing.T) {
	t.Parallel()

	cfg := &TCPConfig{
		Host:          exampleHost,
		Port:          443,
		Timeout:       5 * time.Second,
		SendData:      "GET / HTTP/1.1\r\n\r\n",
		ExpectData:    "200 OK",
		TLS:           true,
		TLSVerify:     true,
		TLSServerName: exampleHost,
	}

	result := cfg.GetConfig()

	if result["host"] != exampleHost {
		t.Errorf("expected host '%s', got '%v'", exampleHost, result["host"])
	}

	if result["port"] != 443 {
		t.Errorf("expected port 443, got %v", result["port"])
	}

	if result["timeout"] != "5s" {
		t.Errorf("expected timeout '5s', got '%v'", result["timeout"])
	}

	if result["send_data"] != "GET / HTTP/1.1\r\n\r\n" {
		t.Errorf("expected send_data 'GET / HTTP/1.1\\r\\n\\r\\n', got '%v'", result["send_data"])
	}

	if result["expect_data"] != "200 OK" {
		t.Errorf("expected expect_data '200 OK', got '%v'", result["expect_data"])
	}

	if result["tls"] != true {
		t.Errorf("expected tls true, got %v", result["tls"])
	}

	if result["tls_verify"] != true {
		t.Errorf("expected tls_verify true, got %v", result["tls_verify"])
	}

	if result["tls_server_name"] != exampleHost {
		t.Errorf("expected tls_server_name '%s', got '%v'", exampleHost, result["tls_server_name"])
	}
}

func TestTCPChecker_Validate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		config  *TCPConfig
		wantErr bool
		errMsg  string
	}{
		{
			name: "valid config",
			config: &TCPConfig{
				Host:    "exampleHost",
				Port:    443,
				Timeout: 5 * time.Second,
			},
			wantErr: false,
		},
		{
			name: "minimal valid config",
			config: &TCPConfig{
				Host: "exampleHost",
				Port: 80,
			},
			wantErr: false,
		},
		{
			name:    "empty host",
			config:  &TCPConfig{Port: 80},
			wantErr: true,
			errMsg:  "host: is required",
		},
		{
			name:    "empty port",
			config:  &TCPConfig{Host: "exampleHost"},
			wantErr: true,
			errMsg:  "port: is required",
		},
		{
			name: "port too low",
			config: &TCPConfig{
				Host: "exampleHost",
				Port: 0,
			},
			wantErr: true,
			errMsg:  "port: is required",
		},
		{
			name: "port negative",
			config: &TCPConfig{
				Host: "exampleHost",
				Port: -1,
			},
			wantErr: true,
			errMsg:  "port: must be between 1 and 65535, got -1",
		},
		{
			name: "port too high",
			config: &TCPConfig{
				Host: "exampleHost",
				Port: 65536,
			},
			wantErr: true,
			errMsg:  "port: must be between 1 and 65535, got 65536",
		},
		{
			name: "timeout negative",
			config: &TCPConfig{
				Host:    "exampleHost",
				Port:    80,
				Timeout: -1 * time.Second,
			},
			wantErr: true,
			errMsg:  "timeout: must be > 0 and <= 60s, got -1s",
		},
		{
			name: "timeout too long",
			config: &TCPConfig{
				Host:    "exampleHost",
				Port:    80,
				Timeout: 61 * time.Second,
			},
			wantErr: true,
			errMsg:  "timeout: must be > 0 and <= 60s, got 1m1s",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			checker := &TCPChecker{}
			var configMap map[string]any
			if tt.config != nil {
				configMap = tt.config.GetConfig()
			}
			spec := &checkerdef.CheckSpec{
				Config: configMap,
			}
			err := checker.Validate(spec)

			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error containing '%s', got nil", tt.errMsg)
				}
				if tt.errMsg != "" && err.Error() != tt.errMsg {
					t.Errorf("expected error '%s', got '%s'", tt.errMsg, err.Error())
				}
			} else if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestTCPChecker_Execute(t *testing.T) {
	t.Parallel()

	// Start a test HTTP server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("HTTP/1.1 200 OK\r\n\r\nHello"))
	}))
	t.Cleanup(server.Close)

	// Extract host and port from server URL
	_, portStr, _ := net.SplitHostPort(server.Listener.Addr().String())

	var port int

	_, _ = fmt.Sscanf(portStr, "%d", &port)

	tests := []struct {
		name           string
		config         *TCPConfig
		expectedStatus checkerdef.Status
		checkMetrics   bool
		checkOutput    func(*testing.T, map[string]any)
	}{
		{
			name: "successful connection to test server",
			config: &TCPConfig{
				Host:    "127.0.0.1",
				Port:    port,
				Timeout: 2 * time.Second,
			},
			expectedStatus: checkerdef.StatusUp,
			checkMetrics:   true,
			checkOutput: func(t *testing.T, output map[string]any) {
				t.Helper()
				if _, ok := output["host"]; !ok {
					t.Error("expected host in output")
				}
				if portVal, ok := output["port"]; !ok || portVal != port {
					t.Errorf("expected port %d in output, got %v", port, portVal)
				}
			},
		},
		{
			name: "connection with send and expect data",
			config: &TCPConfig{
				Host:       "127.0.0.1",
				Port:       port,
				Timeout:    2 * time.Second,
				SendData:   "GET / HTTP/1.1\r\nHost: localhost\r\n\r\n",
				ExpectData: "Hello",
			},
			expectedStatus: checkerdef.StatusUp,
			checkMetrics:   true,
			checkOutput: func(t *testing.T, output map[string]any) {
				t.Helper()
				if _, ok := output["received_data"]; !ok {
					t.Error("expected received_data in output")
				}
			},
		},
		{
			name: "connection refused",
			config: &TCPConfig{
				Host:    "127.0.0.1",
				Port:    1, // Port 1 should be closed
				Timeout: 1 * time.Second,
			},
			expectedStatus: checkerdef.StatusDown,
			checkMetrics:   false,
			checkOutput: func(t *testing.T, output map[string]any) {
				t.Helper()
				if _, ok := output["error"]; !ok {
					t.Error("expected error in output")
				}
			},
		},
		{
			name: "invalid host resolution",
			config: &TCPConfig{
				Host:    "this-host-does-not-exist-12345.invalid",
				Port:    80,
				Timeout: 2 * time.Second,
			},
			expectedStatus: checkerdef.StatusError,
			checkMetrics:   false,
			checkOutput: func(t *testing.T, output map[string]any) {
				t.Helper()
				if _, ok := output["error"]; !ok {
					t.Error("expected error in output")
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			checker := &TCPChecker{}
			ctx := context.Background()

			result, err := checker.Execute(ctx, tt.config)
			if err != nil {
				if tt.expectedStatus != checkerdef.StatusError {
					t.Errorf("unexpected error: %v", err)
				}
				if result != nil {
					t.Errorf("result should be nil when error is returned")
				}
				return
			}
			if result == nil {
				t.Fatal("Execute returned nil result without error")
			}

			if result.Status != tt.expectedStatus {
				t.Errorf("expected status %v, got %v (output: %v)", tt.expectedStatus, result.Status, result.Output)
			}

			// Check metrics if expected
			if tt.checkMetrics {
				if _, ok := result.Metrics["connection_time_ms"]; !ok {
					t.Error("expected connection_time_ms metric")
				}
				if _, ok := result.Metrics["total_time_ms"]; !ok {
					t.Error("expected total_time_ms metric")
				}
			}

			// Run custom output checks
			if tt.checkOutput != nil {
				tt.checkOutput(t, result.Output)
			}
		})
	}
}
