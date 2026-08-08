package checkclickhouse_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkers/checkclickhouse"
)

func TestFromMap(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		configMap map[string]any
		wantErr   string
		assert    func(*require.Assertions, *checkclickhouse.ClickHouseConfig)
	}{
		{
			name: "full config",
			configMap: map[string]any{
				"host":       "ch.example.com",
				"port":       9440,
				"username":   "monitor",
				"password":   "s3cret",
				"database":   "metrics",
				"secure":     true,
				"tls_verify": true,
				"timeout":    "5s",
				"query":      "SELECT count() FROM system.parts",
			},
			assert: func(r *require.Assertions, cfg *checkclickhouse.ClickHouseConfig) {
				r.Equal("ch.example.com", cfg.Host)
				r.Equal(9440, cfg.Port)
				r.Equal("monitor", cfg.Username)
				r.Equal("s3cret", cfg.Password)
				r.Equal("metrics", cfg.Database)
				r.True(cfg.Secure)
				r.True(cfg.TLSVerify)
				r.Equal(5*time.Second, cfg.Timeout)
				r.Equal("SELECT count() FROM system.parts", cfg.Query)
			},
		},
		{
			// A config round-tripped through JSON returns numbers as float64.
			name:      "port as float64",
			configMap: map[string]any{"host": "ch.example.com", "port": float64(9001)},
			assert: func(r *require.Assertions, cfg *checkclickhouse.ClickHouseConfig) {
				r.Equal(9001, cfg.Port)
			},
		},
		{
			name:      "empty config leaves zero values",
			configMap: map[string]any{},
			assert: func(r *require.Assertions, cfg *checkclickhouse.ClickHouseConfig) {
				r.Empty(cfg.Host)
				r.Zero(cfg.Port)
				r.False(cfg.Secure)
			},
		},
		{
			name:      "host wrong type",
			configMap: map[string]any{"host": 42},
			wantErr:   "host",
		},
		{
			name:      "port wrong type",
			configMap: map[string]any{"port": "9000"},
			wantErr:   "port",
		},
		{
			name:      "secure wrong type",
			configMap: map[string]any{"secure": "yes"},
			wantErr:   "secure",
		},
		{
			name:      "timeout not a duration",
			configMap: map[string]any{"timeout": "soon"},
			wantErr:   "timeout",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)

			cfg := &checkclickhouse.ClickHouseConfig{}
			err := cfg.FromMap(tt.configMap)

			if tt.wantErr != "" {
				r.Error(err)
				r.Contains(err.Error(), tt.wantErr)

				return
			}

			r.NoError(err)
			tt.assert(r, cfg)
		})
	}
}

func TestValidate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		config  checkclickhouse.ClickHouseConfig
		wantErr string
	}{
		{
			name:   "minimal valid config",
			config: checkclickhouse.ClickHouseConfig{Host: "ch.example.com"},
		},
		{
			name:   "secure with verification",
			config: checkclickhouse.ClickHouseConfig{Host: "ch.example.com", Secure: true, TLSVerify: true},
		},
		{
			name:    "host required",
			config:  checkclickhouse.ClickHouseConfig{},
			wantErr: "host",
		},
		{
			name:    "port out of range",
			config:  checkclickhouse.ClickHouseConfig{Host: "ch.example.com", Port: 70000},
			wantErr: "port",
		},
		{
			name:    "timeout above maximum",
			config:  checkclickhouse.ClickHouseConfig{Host: "ch.example.com", Timeout: time.Minute},
			wantErr: "timeout",
		},
		{
			name:    "tls_verify without secure",
			config:  checkclickhouse.ClickHouseConfig{Host: "ch.example.com", TLSVerify: true},
			wantErr: "tls_verify",
		},
		{
			name:    "query must be a SELECT",
			config:  checkclickhouse.ClickHouseConfig{Host: "ch.example.com", Query: "DROP TABLE events"},
			wantErr: "query",
		},
		{
			name:   "query is case-insensitive and trimmed",
			config: checkclickhouse.ClickHouseConfig{Host: "ch.example.com", Query: "  select 1"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)

			err := tt.config.Validate()

			if tt.wantErr == "" {
				r.NoError(err)

				return
			}

			r.Error(err)
			r.Contains(err.Error(), tt.wantErr)
		})
	}
}

// GetConfig must omit anything left at its default so stored configs stay
// minimal, and FromMap must read back what GetConfig wrote.
func TestGetConfigRoundTrip(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	defaults := &checkclickhouse.ClickHouseConfig{Host: "ch.example.com", Port: 9000, Query: "SELECT 1"}
	r.Equal(map[string]any{"host": "ch.example.com"}, defaults.GetConfig())

	// With TLS on, 9440 (not 9000) is the implied default that gets omitted.
	secure := &checkclickhouse.ClickHouseConfig{Host: "ch.example.com", Port: 9440, Secure: true}
	r.Equal(map[string]any{"host": "ch.example.com", "secure": true}, secure.GetConfig())

	original := &checkclickhouse.ClickHouseConfig{
		Host:      "ch.example.com",
		Port:      9001,
		Username:  "monitor",
		Password:  "s3cret",
		Database:  "metrics",
		Secure:    true,
		TLSVerify: true,
		Timeout:   7 * time.Second,
		Query:     "SELECT 2",
	}

	restored := &checkclickhouse.ClickHouseConfig{}
	r.NoError(restored.FromMap(original.GetConfig()))
	r.Equal(original, restored)
}

func TestSecretFields(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	cfg := &checkclickhouse.ClickHouseConfig{}
	r.Equal([]string{"password"}, cfg.SecretFields())
}
