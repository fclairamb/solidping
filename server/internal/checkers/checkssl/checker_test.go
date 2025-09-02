package checkssl

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

func TestSSLChecker_Type(t *testing.T) {
	t.Parallel()

	checker := &SSLChecker{}
	require.Equal(t, checkerdef.CheckTypeSSL, checker.Type())
}

func TestSSLChecker_Validate(t *testing.T) {
	t.Parallel()

	checker := &SSLChecker{}

	tests := []struct {
		name    string
		spec    *checkerdef.CheckSpec
		wantErr bool
	}{
		{
			name: "valid config",
			spec: &checkerdef.CheckSpec{
				Config: map[string]any{
					"host": "google.com",
				},
			},
			wantErr: false,
		},
		{
			name: "valid config with port",
			spec: &checkerdef.CheckSpec{
				Config: map[string]any{
					"host": "example.com",
					"port": float64(8443),
				},
			},
			wantErr: false,
		},
		{
			name: "valid config with timeout",
			spec: &checkerdef.CheckSpec{
				Config: map[string]any{
					"host":    "example.com",
					"timeout": "15s",
				},
			},
			wantErr: false,
		},
		{
			name: "invalid timeout too large",
			spec: &checkerdef.CheckSpec{
				Config: map[string]any{
					"host":    "example.com",
					"timeout": "120s",
				},
			},
			wantErr: true,
		},
		{
			name: "missing host",
			spec: &checkerdef.CheckSpec{
				Config: map[string]any{},
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := checker.Validate(tt.spec)
			if tt.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
				require.NotEmpty(t, tt.spec.Name)
				require.NotEmpty(t, tt.spec.Slug)
			}
		})
	}
}

func TestSSLChecker_Execute(t *testing.T) {
	t.Parallel()

	if testing.Short() {
		t.Skip("skipping test in short mode (requires network)")
	}

	checker := &SSLChecker{}
	ctx := context.Background()

	config := &SSLConfig{
		Host:          "google.com",
		ThresholdDays: 7,
	}

	result, err := checker.Execute(ctx, config)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, checkerdef.StatusUp, result.Status)

	require.Contains(t, result.Output, "subject")
	require.Contains(t, result.Output, "issuer")
	require.Contains(t, result.Output, "not_after")
	require.Contains(t, result.Output, "days_remaining")
	require.Contains(t, result.Output, "tls_version")
	require.Contains(t, result.Output, "dns_names")

	daysRemaining, ok := result.Metrics["days_remaining"].(int)
	require.True(t, ok, "days_remaining metric should be an int")
	require.Greater(t, daysRemaining, 7)
}

func TestSSLConfig_FromMap(t *testing.T) {
	t.Parallel()

	cfg := &SSLConfig{}
	err := cfg.FromMap(map[string]any{
		"host":           "example.com",
		"port":           float64(8443),
		"threshold_days": float64(14),
		"timeout":        "15s",
		"server_name":    "other.example.com",
	})

	r := require.New(t)
	r.NoError(err)
	r.Equal("example.com", cfg.Host)
	r.Equal(8443, cfg.Port)
	r.Equal(14, cfg.ThresholdDays)
	r.Equal(15*time.Second, cfg.Timeout)
	r.Equal("other.example.com", cfg.ServerName)
}

func TestSSLConfig_GetConfig(t *testing.T) {
	t.Parallel()

	cfg := &SSLConfig{
		Host:          "example.com",
		Port:          8443,
		ThresholdDays: 14,
		Timeout:       15 * time.Second,
		ServerName:    "other.example.com",
	}

	m := cfg.GetConfig()

	r := require.New(t)
	r.Equal("example.com", m["host"])
	r.Equal(8443, m["port"])
	r.Equal(14, m["threshold_days"])
	r.Equal("15s", m["timeout"])
	r.Equal("other.example.com", m["server_name"])
}

func TestSSLConfig_GetConfig_Defaults(t *testing.T) {
	t.Parallel()

	cfg := &SSLConfig{
		Host: "example.com",
	}

	m := cfg.GetConfig()

	r := require.New(t)
	r.Equal("example.com", m["host"])
	r.NotContains(m, "port")
	r.NotContains(m, "server_name")
}
