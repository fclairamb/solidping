package checkdns

import (
	"context"
	"testing"
	"time"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

func TestDNSChecker_Type(t *testing.T) {
	t.Parallel()

	checker := &DNSChecker{}
	if checker.Type() != checkerdef.CheckTypeDNS {
		t.Errorf("expected type %s, got %s", checkerdef.CheckTypeDNS, checker.Type())
	}
}

func TestDNSConfig_FromMap(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		configMap map[string]any
		wantErr   bool
		errMsg    string
		validate  func(*testing.T, *DNSConfig)
	}{
		{
			name: "valid config with all fields",
			configMap: map[string]any{
				"host":            "example.com",
				"timeout":         "10s",
				"nameserver":      "8.8.8.8:53",
				"record_type":     "A",
				"expected_ips":    []any{"93.184.216.34"},
				"expected_values": nil,
			},
			wantErr: false,
			validate: func(t *testing.T, cfg *DNSConfig) {
				t.Helper()
				if cfg.Host != "example.com" {
					t.Errorf("expected host 'example.com', got '%s'", cfg.Host)
				}
				if cfg.Timeout != 10*time.Second {
					t.Errorf("expected timeout 10s, got %v", cfg.Timeout)
				}
				if cfg.Nameserver != "8.8.8.8:53" {
					t.Errorf("expected nameserver '8.8.8.8:53', got '%s'", cfg.Nameserver)
				}
				if cfg.RecordType != "A" {
					t.Errorf("expected record_type 'A', got '%s'", cfg.RecordType)
				}
				if len(cfg.ExpectedIPs) != 1 || cfg.ExpectedIPs[0] != "93.184.216.34" {
					t.Errorf("expected expected_ips ['93.184.216.34'], got %v", cfg.ExpectedIPs)
				}
			},
		},
		{
			name: "minimal valid config",
			configMap: map[string]any{
				"host": "google.com",
			},
			wantErr: false,
			validate: func(t *testing.T, cfg *DNSConfig) {
				t.Helper()
				if cfg.Host != "google.com" {
					t.Errorf("expected host 'google.com', got '%s'", cfg.Host)
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
			name: "invalid nameserver type",
			configMap: map[string]any{
				"host":       "example.com",
				"nameserver": 123,
			},
			wantErr: true,
			errMsg:  "nameserver: must be a string",
		},
		{
			name: "invalid record_type type",
			configMap: map[string]any{
				"host":        "example.com",
				"record_type": 123,
			},
			wantErr: true,
			errMsg:  "record_type: must be a string",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			cfg := &DNSConfig{}
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

func TestDNSConfig_GetConfig(t *testing.T) {
	t.Parallel()

	cfg := &DNSConfig{
		Host:           "example.com",
		Timeout:        5 * time.Second,
		Nameserver:     "8.8.8.8:53",
		RecordType:     "A",
		ExpectedIPs:    []string{"93.184.216.34"},
		ExpectedValues: nil,
	}

	result := cfg.GetConfig()

	if result["host"] != "example.com" {
		t.Errorf("expected host 'example.com', got '%v'", result["host"])
	}

	if result["timeout"] != "5s" {
		t.Errorf("expected timeout '5s', got '%v'", result["timeout"])
	}

	if result["nameserver"] != "8.8.8.8:53" {
		t.Errorf("expected nameserver '8.8.8.8:53', got '%v'", result["nameserver"])
	}

	if result["record_type"] != "A" {
		t.Errorf("expected record_type 'A', got '%v'", result["record_type"])
	}
}

func TestDNSChecker_Validate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		config  *DNSConfig
		wantErr bool
		errMsg  string
	}{
		{
			name: "valid config",
			config: &DNSConfig{
				Host:       "example.com",
				Timeout:    5 * time.Second,
				RecordType: "A",
			},
			wantErr: false,
		},
		{
			name: "minimal valid config (defaults applied)",
			config: &DNSConfig{
				Host: "example.com",
			},
			wantErr: false,
		},
		{
			name:    "empty host",
			config:  &DNSConfig{},
			wantErr: true,
			errMsg:  "host: is required",
		},
		{
			name: "timeout negative",
			config: &DNSConfig{
				Host:    "example.com",
				Timeout: -1 * time.Second,
			},
			wantErr: true,
			errMsg:  "timeout: must be > 0 and <= 60s, got -1s",
		},
		{
			name: "timeout too long",
			config: &DNSConfig{
				Host:    "example.com",
				Timeout: 61 * time.Second,
			},
			wantErr: true,
			errMsg:  "timeout: must be > 0 and <= 60s, got 1m1s",
		},
		{
			name: "invalid record type",
			config: &DNSConfig{
				Host:       "example.com",
				RecordType: "INVALID",
			},
			wantErr: true,
			errMsg:  "record_type: must be one of A, AAAA, CNAME, MX, NS, TXT, SOA, got INVALID",
		},
		{
			name: "invalid nameserver format",
			config: &DNSConfig{
				Host:       "example.com",
				Nameserver: "8.8.8.8",
			},
			wantErr: true,
			errMsg:  "nameserver: must be in format host:port, got 8.8.8.8",
		},
		{
			name: "conflicting expected values",
			config: &DNSConfig{
				Host:           "example.com",
				ExpectedIPs:    []string{"1.2.3.4"},
				ExpectedValues: []string{"example.org"},
			},
			wantErr: true,
			errMsg:  "expected_values: cannot specify both expected_ips and expected_values",
		},
		{
			name: "valid AAAA record",
			config: &DNSConfig{
				Host:       "example.com",
				RecordType: "AAAA",
			},
			wantErr: false,
		},
		{
			name: "valid MX record",
			config: &DNSConfig{
				Host:       "example.com",
				RecordType: "MX",
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			checker := &DNSChecker{}
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

func TestDNSChecker_Execute(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		config         *DNSConfig
		expectedStatus checkerdef.Status
		checkMetrics   bool
	}{
		{
			name: "A record lookup",
			config: &DNSConfig{
				Host:       "google.com",
				RecordType: "A",
				Timeout:    5 * time.Second,
			},
			expectedStatus: checkerdef.StatusUp,
			checkMetrics:   true,
		},
		{
			name: "invalid domain",
			config: &DNSConfig{
				Host:       "this-domain-does-not-exist-12345.invalid",
				RecordType: "A",
				Timeout:    5 * time.Second,
			},
			expectedStatus: checkerdef.StatusDown,
			checkMetrics:   false,
		},
		{
			name: "MX record lookup",
			config: &DNSConfig{
				Host:       "gmail.com",
				RecordType: "MX",
				Timeout:    5 * time.Second,
			},
			expectedStatus: checkerdef.StatusUp,
			checkMetrics:   true,
		},
		{
			name: "TXT record lookup",
			config: &DNSConfig{
				Host:       "google.com",
				RecordType: "TXT",
				Timeout:    5 * time.Second,
			},
			expectedStatus: checkerdef.StatusUp,
			checkMetrics:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			checker := &DNSChecker{}
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

			if tt.checkMetrics && result.Status == checkerdef.StatusUp {
				// Verify metrics exist
				if _, ok := result.Metrics["query_time_ms"]; !ok {
					t.Error("expected query_time_ms metric")
				}
				if _, ok := result.Metrics["record_count"]; !ok {
					t.Error("expected record_count metric")
				}

				// Verify output
				if _, ok := result.Output["host"]; !ok {
					t.Error("expected host in output")
				}
				if _, ok := result.Output["record_type"]; !ok {
					t.Error("expected record_type in output")
				}
			}
		})
	}
}

func TestDNSChecker_MatchValues(t *testing.T) {
	t.Parallel()

	checker := &DNSChecker{}

	tests := []struct {
		name            string
		resolved        []string
		expected        []string
		caseInsensitive bool
		want            bool
	}{
		{
			name:            "exact match",
			resolved:        []string{"1.2.3.4", "5.6.7.8"},
			expected:        []string{"1.2.3.4"},
			caseInsensitive: true,
			want:            true,
		},
		{
			name:            "no match",
			resolved:        []string{"1.2.3.4"},
			expected:        []string{"5.6.7.8"},
			caseInsensitive: true,
			want:            false,
		},
		{
			name:            "case insensitive match",
			resolved:        []string{"Example.Com"},
			expected:        []string{"example.com"},
			caseInsensitive: true,
			want:            true,
		},
		{
			name:            "case sensitive mismatch",
			resolved:        []string{"Example.Com"},
			expected:        []string{"example.com"},
			caseInsensitive: false,
			want:            false,
		},
		{
			name:            "all expected found",
			resolved:        []string{"a", "b", "c"},
			expected:        []string{"a", "b"},
			caseInsensitive: false,
			want:            true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := checker.matchValues(tt.resolved, tt.expected, tt.caseInsensitive)
			if got != tt.want {
				t.Errorf("matchValues() = %v, want %v", got, tt.want)
			}
		})
	}
}
