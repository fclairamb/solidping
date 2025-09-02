package checkicmp

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

func TestICMPChecker_Type(t *testing.T) {
	t.Parallel()

	checker := &ICMPChecker{}
	if checker.Type() != checkerdef.CheckTypeICMP {
		t.Errorf("expected type %s, got %s", checkerdef.CheckTypeICMP, checker.Type())
	}
}

func TestICMPConfig_FromMap(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		configMap map[string]any
		wantErr   bool
		errMsg    string
		validate  func(*testing.T, *ICMPConfig)
	}{
		{
			name: "valid config with all fields",
			configMap: map[string]any{
				"host":        "example.com",
				"timeout":     "10s",
				"count":       5,
				"interval":    "2s",
				"packet_size": 64,
				"ttl":         128,
			},
			wantErr: false,
			validate: func(t *testing.T, cfg *ICMPConfig) {
				t.Helper()
				if cfg.Host != "example.com" {
					t.Errorf("expected host 'example.com', got '%s'", cfg.Host)
				}
				if cfg.Timeout != 10*time.Second {
					t.Errorf("expected timeout 10s, got %v", cfg.Timeout)
				}
				if cfg.Count != 5 {
					t.Errorf("expected count 5, got %d", cfg.Count)
				}
				if cfg.Interval != 2*time.Second {
					t.Errorf("expected interval 2s, got %v", cfg.Interval)
				}
				if cfg.PacketSize != 64 {
					t.Errorf("expected packet_size 64, got %d", cfg.PacketSize)
				}
				if cfg.TTL != 128 {
					t.Errorf("expected ttl 128, got %d", cfg.TTL)
				}
			},
		},
		{
			name: "minimal valid config",
			configMap: map[string]any{
				"host": "8.8.8.8",
			},
			wantErr: false,
			validate: func(t *testing.T, cfg *ICMPConfig) {
				t.Helper()
				if cfg.Host != "8.8.8.8" {
					t.Errorf("expected host '8.8.8.8', got '%s'", cfg.Host)
				}
			},
		},
		{
			name: "count as float64 (JSON unmarshal)",
			configMap: map[string]any{
				"host":  "example.com",
				"count": 3.0,
			},
			wantErr: false,
			validate: func(t *testing.T, cfg *ICMPConfig) {
				t.Helper()
				if cfg.Count != 3 {
					t.Errorf("expected count 3, got %d", cfg.Count)
				}
			},
		},
		{
			name: "invalid host type",
			configMap: map[string]any{
				"host": 123,
			},
			wantErr: true,
			errMsg:  "host: must be a string",
		},
		{
			name: "invalid timeout type",
			configMap: map[string]any{
				"host":    "example.com",
				"timeout": 123,
			},
			wantErr: true,
			errMsg:  "timeout: must be a string",
		},
		{
			name: "invalid timeout format",
			configMap: map[string]any{
				"host":    "example.com",
				"timeout": "invalid",
			},
			wantErr: true,
			errMsg:  "timeout: must be a valid duration string",
		},
		{
			name: "invalid count type",
			configMap: map[string]any{
				"host":  "example.com",
				"count": "5",
			},
			wantErr: true,
			errMsg:  "count: must be a number",
		},
		{
			name: "invalid interval type",
			configMap: map[string]any{
				"host":     "example.com",
				"interval": 123,
			},
			wantErr: true,
			errMsg:  "interval: must be a string",
		},
		{
			name: "invalid packet_size type",
			configMap: map[string]any{
				"host":        "example.com",
				"packet_size": "64",
			},
			wantErr: true,
			errMsg:  "packet_size: must be a number",
		},
		{
			name: "invalid ttl type",
			configMap: map[string]any{
				"host": "example.com",
				"ttl":  "64",
			},
			wantErr: true,
			errMsg:  "ttl: must be a number",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			cfg := &ICMPConfig{}
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

func TestICMPConfig_GetConfig(t *testing.T) {
	t.Parallel()

	cfg := &ICMPConfig{
		Host:       "example.com",
		Timeout:    5 * time.Second,
		Count:      3,
		Interval:   1 * time.Second,
		PacketSize: 64,
		TTL:        128,
	}

	result := cfg.GetConfig()

	if result["host"] != "example.com" {
		t.Errorf("expected host 'example.com', got '%v'", result["host"])
	}

	if result["timeout"] != "5s" {
		t.Errorf("expected timeout '5s', got '%v'", result["timeout"])
	}

	if result["count"] != 3 {
		t.Errorf("expected count 3, got %v", result["count"])
	}

	if result["interval"] != "1s" {
		t.Errorf("expected interval '1s', got '%v'", result["interval"])
	}

	if result["packet_size"] != 64 {
		t.Errorf("expected packet_size 64, got %v", result["packet_size"])
	}

	if result["ttl"] != 128 {
		t.Errorf("expected ttl 128, got %v", result["ttl"])
	}
}

func TestICMPChecker_Validate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		config  *ICMPConfig
		wantErr bool
		errMsg  string
	}{
		{
			name: "valid config",
			config: &ICMPConfig{
				Host:       "example.com",
				Timeout:    5 * time.Second,
				Count:      3,
				Interval:   1 * time.Second,
				PacketSize: 56,
				TTL:        64,
			},
			wantErr: false,
		},
		{
			name: "minimal valid config (defaults applied)",
			config: &ICMPConfig{
				Host: "example.com",
			},
			wantErr: false,
		},
		{
			name:    "empty host",
			config:  &ICMPConfig{},
			wantErr: true,
			errMsg:  "host: is required",
		},
		{
			name: "count negative",
			config: &ICMPConfig{
				Host:  "example.com",
				Count: -1,
			},
			wantErr: true,
			errMsg:  "count: must be between 1 and 10, got -1",
		},
		{
			name: "count too high",
			config: &ICMPConfig{
				Host:  "example.com",
				Count: 11,
			},
			wantErr: true,
			errMsg:  "count: must be between 1 and 10, got 11",
		},
		{
			name: "interval too short",
			config: &ICMPConfig{
				Host:     "example.com",
				Interval: 50 * time.Millisecond,
			},
			wantErr: true,
			errMsg:  "interval: must be between 100ms and 60s, got 50ms",
		},
		{
			name: "interval too long",
			config: &ICMPConfig{
				Host:     "example.com",
				Interval: 61 * time.Second,
			},
			wantErr: true,
			errMsg:  "interval: must be between 100ms and 60s, got 1m1s",
		},
		{
			name: "packet_size too small",
			config: &ICMPConfig{
				Host:       "example.com",
				PacketSize: -1,
			},
			wantErr: true,
			errMsg:  "packet_size: must be between 0 and 65507 bytes, got -1",
		},
		{
			name: "packet_size too large",
			config: &ICMPConfig{
				Host:       "example.com",
				PacketSize: 65508,
			},
			wantErr: true,
			errMsg:  "packet_size: must be between 0 and 65507 bytes, got 65508",
		},
		{
			name: "ttl negative",
			config: &ICMPConfig{
				Host: "example.com",
				TTL:  -1,
			},
			wantErr: true,
			errMsg:  "ttl: must be between 1 and 255, got -1",
		},
		{
			name: "ttl too high",
			config: &ICMPConfig{
				Host: "example.com",
				TTL:  256,
			},
			wantErr: true,
			errMsg:  "ttl: must be between 1 and 255, got 256",
		},
		{
			name: "timeout negative",
			config: &ICMPConfig{
				Host:    "example.com",
				Timeout: -1 * time.Second,
			},
			wantErr: true,
			errMsg:  "timeout: must be > 0 and <= 60s, got -1s",
		},
		{
			name: "timeout too long",
			config: &ICMPConfig{
				Host:    "example.com",
				Timeout: 61 * time.Second,
			},
			wantErr: true,
			errMsg:  "timeout: must be > 0 and <= 60s, got 1m1s",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			checker := &ICMPChecker{}
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
			} else {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
			}
		})
	}
}

func TestICMPChecker_Execute(t *testing.T) {
	t.Parallel()

	// Skip test in GitHub Actions where ICMP requires elevated privileges
	if os.Getenv("GITHUB_ACTIONS") == "true" {
		t.Skip("Skipping ICMP test in GitHub Actions (requires elevated privileges)")
	}

	tests := []struct {
		name           string
		config         *ICMPConfig
		expectedStatus checkerdef.Status
		checkMetrics   bool
	}{
		{
			name: "icmp google.com via ICMP",
			config: &ICMPConfig{
				Host:    "google.com",
				Timeout: 5 * time.Second,
				Count:   2,
			},
			expectedStatus: checkerdef.StatusUp,
			checkMetrics:   true,
		},
		{
			name: "invalid host resolution",
			config: &ICMPConfig{
				Host:    "this-host-does-not-exist-12345.invalid",
				Timeout: 2 * time.Second,
				Count:   1,
			},
			expectedStatus: checkerdef.StatusDown,
			checkMetrics:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			checker := &ICMPChecker{}
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
				t.Errorf("expected status %v, got %v", tt.expectedStatus, result.Status)
				if result.Output != nil {
					if errMsg, ok := result.Output["error"]; ok {
						t.Logf("error from checker: %v", errMsg)
					}
				}
			}

			// Verify basic output fields exist for all checks
			if _, ok := result.Output["host"]; !ok {
				t.Error("expected host in output")
			}

			// Verify method field indicates ICMP-based check
			if method, ok := result.Output["method"].(string); !ok {
				t.Error("expected method in output")
			} else if method != "icmp" {
				t.Errorf("expected method to be 'icmp', got %s", method)
			}

			// Verify additional fields for successful checks
			if result.Status == checkerdef.StatusUp {
				if _, ok := result.Output["ip_version"]; !ok {
					t.Error("expected ip_version in output")
				}
				if ipVersion, ok := result.Output["ip_version"].(string); ok {
					if ipVersion != "ipv4" && ipVersion != "ipv6" {
						t.Errorf("expected ip_version to be 'ipv4' or 'ipv6', got %s", ipVersion)
					}
				}
			}

			// Verify metrics structure
			if tt.checkMetrics && result.Status == checkerdef.StatusUp {
				if result.Metrics == nil {
					t.Fatal("expected metrics to be present")
				}

				// Verify packet metrics
				packetsSent, ok := result.Metrics["packets_sent"].(int)
				if !ok {
					t.Error("expected packets_sent metric to be int")
				} else if packetsSent != tt.config.Count {
					t.Errorf("expected packets_sent to be %d, got %d", tt.config.Count, packetsSent)
				}

				packetsReceived, ok := result.Metrics["packets_received"].(int)
				if !ok {
					t.Error("expected packets_received metric to be int")
				} else if packetsReceived < 1 {
					t.Error("expected at least one packet received for StatusUp")
				}

				if _, okPct := result.Metrics["packet_loss_pct"].(float64); !okPct {
					t.Error("expected packet_loss_pct metric to be float64")
				}

				// RTT metrics should exist
				rttMin, ok := result.Metrics["rtt_ms_min"].(float64)
				if !ok {
					t.Error("expected rtt_ms_min metric to be float64")
				} else if rttMin <= 0 {
					t.Error("expected rtt_ms_min to be > 0")
				}

				rttMax, ok := result.Metrics["rtt_ms_max"].(float64)
				if !ok {
					t.Error("expected rtt_ms_max metric to be float64")
				} else if rttMax <= 0 {
					t.Error("expected rtt_ms_max to be > 0")
				}

				rttAvg, ok := result.Metrics["rtt_ms_avg"].(float64)
				if !ok {
					t.Error("expected rtt_ms_avg metric to be float64")
				} else if rttAvg <= 0 {
					t.Error("expected rtt_ms_avg to be > 0")
				}

				// Sanity check: min <= avg <= max
				if rttMin > rttAvg || rttAvg > rttMax {
					t.Errorf("RTT metrics invalid: min=%f, avg=%f, max=%f", rttMin, rttAvg, rttMax)
				}

				t.Logf("ICMP ping successful! RTT: min=%.2fms, avg=%.2fms, max=%.2fms", rttMin, rttAvg, rttMax)
			}
		})
	}
}
